package sshcore

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Algo 是密钥算法。取值范围刻意收窄：只做这三种，不装作支持更多。
type Algo string

const (
	AlgoEd25519 Algo = "ed25519"
	AlgoRSA     Algo = "rsa"
	AlgoECDSA   Algo = "ecdsa"
)

// rsaBits 是新建 RSA 密钥的位数。
// 3072 是当前的合理默认：2048 已被多数基线要求淘汰，4096 生成要数秒，
// 对一个"点一下就建好密钥"的交互来说太慢。
const rsaBits = 3072

var (
	// ErrKeyEncrypted 表示私钥受口令保护，调用方需要先取得 passphrase。
	ErrKeyEncrypted = errors.New("sshcore: 私钥已加密，需要口令（passphrase）")
	// ErrKeyFIDOUnsupported 表示这是 FIDO 安全密钥（sk-*）。
	// 这类密钥的私钥句柄永远留在硬件里，Go 的 ssh 库不支持，必须明确告知用户，
	// 而不是把"解析失败"当作"文件损坏"。
	ErrKeyFIDOUnsupported = errors.New("sshcore: 不支持 FIDO 安全密钥（sk-ssh-ed25519 / sk-ecdsa-sha2-nistp256），请改用普通私钥或 ssh-agent")
)

// KeyMaterial 是一次密钥生成的产物。
type KeyMaterial struct {
	// PrivatePEM 是 OpenSSH 原生私钥文本（"openssh-key-v1"），
	// 与 `ssh-keygen -t ed25519` 产出的文件同构，可直接被系统 ssh 使用。
	PrivatePEM []byte
	// PublicLine 是 authorized_keys 格式的单行公钥，便于一键粘贴到服务端。
	PublicLine string
	// Fingerprint 是 "SHA256:..." 指纹，与 `ssh-keygen -lf` 输出一致。
	Fingerprint string
	// Algo 是生成时使用的算法。
	Algo Algo
	// KeyType 是 SSH 协议里的算法名，如 ssh-ed25519 / ssh-rsa / ecdsa-sha2-nistp256。
	KeyType string
}

// GenerateKey 生成一对新密钥。
// comment 会被写进私钥头部与 authorized_keys 行尾（通常是 user@host）。
// passphrase 非空时私钥受口令保护——落盘的是加密后的 OpenSSH 格式，与 ssh-keygen 一致。
func GenerateKey(alg Algo, comment, passphrase string) (*KeyMaterial, error) {
	priv, err := generatePrivateKey(alg)
	if err != nil {
		return nil, err
	}

	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, comment)
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, comment, []byte(passphrase))
	}
	if err != nil {
		return nil, fmt.Errorf("sshcore: 序列化私钥失败: %w", err)
	}

	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("sshcore: 构造签名器失败: %w", err)
	}
	pub := signer.PublicKey()

	return &KeyMaterial{
		PrivatePEM: pem.EncodeToMemory(block),
		PublicLine: PublicKeyLine(pub),
		Fingerprint: Fingerprint(pub),
		Algo:        alg,
		KeyType:     pub.Type(),
	}, nil
}

func generatePrivateKey(alg Algo) (any, error) {
	switch alg {
	case AlgoEd25519:
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("sshcore: 生成 ed25519 密钥失败: %w", err)
		}
		return priv, nil

	case AlgoRSA:
		priv, err := rsa.GenerateKey(rand.Reader, rsaBits)
		if err != nil {
			return nil, fmt.Errorf("sshcore: 生成 RSA 密钥失败: %w", err)
		}
		return priv, nil

	case AlgoECDSA:
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("sshcore: 生成 ECDSA 密钥失败: %w", err)
		}
		return priv, nil

	default:
		return nil, fmt.Errorf("sshcore: 未知密钥算法 %q（仅支持 ed25519 / rsa / ecdsa）", alg)
	}
}

// ParsePrivateKey 解析 OpenSSH 私钥。
//
// 三种失败路径被刻意区分，因为它们对用户意味着完全不同的事：
// 需要口令（ErrKeyEncrypted）、硬件密钥不支持（ErrKeyFIDOUnsupported）、文件本身有问题。
func ParsePrivateKey(pemBytes []byte, passphrase string) (ssh.Signer, error) {
	// FIDO 密钥的判断必须在解出的 PEM 载荷里做：OpenSSH 私钥把算法名作为明文字符串
	// 存在二进制体内，而外层是 base64，所以在 PEM 文本上搜算法名是搜不到的。
	if block, _ := pem.Decode(pemBytes); block != nil {
		if bytes.Contains(block.Bytes, []byte("sk-ssh-ed25519")) ||
			bytes.Contains(block.Bytes, []byte("sk-ecdsa-sha2")) {
			return nil, ErrKeyFIDOUnsupported
		}
	}

	signer, err := ssh.ParsePrivateKey(pemBytes)
	if err == nil {
		return signer, nil
	}

	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		if passphrase == "" {
			return nil, ErrKeyEncrypted
		}
		withPass, perr := ssh.ParsePrivateKeyWithPassphrase(pemBytes, []byte(passphrase))
		if perr != nil {
			return nil, fmt.Errorf("sshcore: 口令不正确，无法解开私钥: %w", perr)
		}
		return withPass, nil
	}

	return nil, fmt.Errorf("sshcore: 解析私钥失败: %w", err)
}

// Fingerprint 返回 "SHA256:..." 指纹，与 `ssh-keygen -lf` 一致，用户可交叉验证。
func Fingerprint(pub ssh.PublicKey) string { return ssh.FingerprintSHA256(pub) }

// PublicKeyLine 返回 authorized_keys 格式的单行公钥（不含末尾换行）。
func PublicKeyLine(pub ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
}

// DecodePublicKeyLine 解析 authorized_keys 的单行公钥。
func DecodePublicKeyLine(line string) (ssh.PublicKey, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, fmt.Errorf("sshcore: 解析公钥失败: %w", err)
	}
	return pub, nil
}

// PublicKeyFromPrivatePEM 由私钥文本导出公钥行与指纹。
// 用于导入已有私钥时填充 KeyRef 的公钥与指纹字段。
func PublicKeyFromPrivatePEM(pemBytes []byte, passphrase string) (line, fingerprint, keyType string, err error) {
	signer, err := ParsePrivateKey(pemBytes, passphrase)
	if err != nil {
		return "", "", "", err
	}
	pub := signer.PublicKey()
	return PublicKeyLine(pub), Fingerprint(pub), pub.Type(), nil
}

// AlgoOf 由 SSH 算法名反查 Algo，用于导入时推断算法。
func AlgoOf(keyType string) Algo {
	switch {
	case strings.HasPrefix(keyType, "ssh-ed25519"):
		return AlgoEd25519
	case strings.HasPrefix(keyType, "ssh-rsa"), strings.HasPrefix(keyType, "rsa-sha2-"):
		return AlgoRSA
	case strings.HasPrefix(keyType, "ecdsa-sha2-"):
		return AlgoECDSA
	default:
		return Algo(keyType)
	}
}
