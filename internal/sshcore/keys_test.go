package sshcore

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestGenerateKeyRoundTrip 逐一验证三种算法的"生成 → 序列化 → 解析 → 指纹一致"。
// 这是计划里点名的不确定项：MarshalPrivateKey 在不同密钥类型上的边界行为。
func TestGenerateKeyRoundTrip(t *testing.T) {
	for _, alg := range []Algo{AlgoEd25519, AlgoRSA, AlgoECDSA} {
		t.Run(string(alg), func(t *testing.T) {
			km, err := GenerateKey(alg, "nanossh@test", "")
			if err != nil {
				t.Fatalf("生成密钥失败: %v", err)
			}

			if km.Algo != alg {
				t.Fatalf("Algo = %q，期望 %q", km.Algo, alg)
			}
			if !strings.HasPrefix(km.Fingerprint, "SHA256:") {
				t.Fatalf("指纹格式异常: %q", km.Fingerprint)
			}
			if !strings.HasPrefix(km.PublicLine, km.KeyType) {
				t.Fatalf("authorized_keys 行 %q 未以算法名 %q 开头", km.PublicLine, km.KeyType)
			}

			signer, err := ParsePrivateKey(km.PrivatePEM, "")
			if err != nil {
				t.Fatalf("回读私钥失败: %v", err)
			}
			if got := Fingerprint(signer.PublicKey()); got != km.Fingerprint {
				t.Fatalf("回读指纹 %q != 生成指纹 %q", got, km.Fingerprint)
			}

			// 公钥行必须能独立解析回同一把密钥——用户会把它粘贴到服务端，
			// 一旦这行与私钥不匹配，就会出现"认证失败但看起来一切正常"。
			pub, err := DecodePublicKeyLine(km.PublicLine)
			if err != nil {
				t.Fatalf("解析 authorized_keys 行失败: %v", err)
			}
			if Fingerprint(pub) != km.Fingerprint {
				t.Fatalf("公钥行指纹 %q != 私钥指纹 %q", Fingerprint(pub), km.Fingerprint)
			}
		})
	}
}

// TestGenerateKeyWithPassphrase 覆盖带口令的往返，以及"口令不对"必须报错。
func TestGenerateKeyWithPassphrase(t *testing.T) {
	km, err := GenerateKey(AlgoEd25519, "nanossh@test", "correct horse")
	if err != nil {
		t.Fatalf("生成带口令密钥失败: %v", err)
	}

	if _, err := ParsePrivateKey(km.PrivatePEM, ""); !errors.Is(err, ErrKeyEncrypted) {
		t.Fatalf("未提供口令时应返回 ErrKeyEncrypted，实际: %v", err)
	}

	signer, err := ParsePrivateKey(km.PrivatePEM, "correct horse")
	if err != nil {
		t.Fatalf("提供正确口令后仍解析失败: %v", err)
	}
	if Fingerprint(signer.PublicKey()) != km.Fingerprint {
		t.Fatalf("带口令回读的指纹不一致")
	}

	if _, err := ParsePrivateKey(km.PrivatePEM, "wrong horse"); err == nil {
		t.Fatal("错误口令应报错")
	} else if !strings.Contains(err.Error(), "口令不正确") {
		t.Fatalf("错误口令的报错文案不明确: %v", err)
	}
}

// TestParsePrivateKeyRejectsFIDO 确认 sk-* 硬件密钥被明确识别，
// 而不是被当成"文件损坏"——两者的用户处置完全不同。
func TestParsePrivateKeyRejectsFIDO(t *testing.T) {
	blob := pem.EncodeToMemory(&pem.Block{
		Type:  "OPENSSH PRIVATE KEY",
		Bytes: append([]byte{0, 0, 0, 0x2f}, []byte("sk-ssh-ed25519@openssh.com\x00\x00\x00\x00")...),
	})
	if _, err := ParsePrivateKey(blob, ""); !errors.Is(err, ErrKeyFIDOUnsupported) {
		t.Fatalf("FIDO 私钥应返回 ErrKeyFIDOUnsupported，实际: %v", err)
	}
}

// TestPublicKeyFromPrivatePEM 覆盖"导入已有私钥"这条路径：
// 只需要私钥文本，就要能补齐 KeyRef 的公钥行、指纹与算法。
func TestPublicKeyFromPrivatePEM(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "imported@test")
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(block)

	line, fp, keyType, err := PublicKeyFromPrivatePEM(raw, "")
	if err != nil {
		t.Fatalf("导入私钥失败: %v", err)
	}
	if keyType != "ssh-ed25519" {
		t.Fatalf("KeyType = %q，期望 ssh-ed25519", keyType)
	}
	if AlgoOf(keyType) != AlgoEd25519 {
		t.Fatalf("AlgoOf(%q) = %q，期望 ed25519", keyType, AlgoOf(keyType))
	}
	pub, err := DecodePublicKeyLine(line)
	if err != nil {
		t.Fatal(err)
	}
	if Fingerprint(pub) != fp {
		t.Fatalf("公钥指纹与返回的指纹不一致")
	}
}

// TestGenerateKeyRejectsUnknownAlgo 钉死"只支持三种算法"这条边界。
func TestGenerateKeyRejectsUnknownAlgo(t *testing.T) {
	if _, err := GenerateKey(Algo("dsa"), "x", ""); err == nil {
		t.Fatal("未知算法应报错")
	}
}
