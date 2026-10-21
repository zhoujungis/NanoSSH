package sshcore

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostKeyDecision 是 TOFU 对话框返回的信任决策。
type HostKeyDecision int

const (
	// HostKeyReject 拒绝并中断连接。
	HostKeyReject HostKeyDecision = iota
	// HostKeyAcceptOnce 仅本次信任，不写入 known_hosts。
	HostKeyAcceptOnce
	// HostKeyAcceptAndSave 信任并保存到 known_hosts。
	HostKeyAcceptAndSave
)

// HostKeyInfo 描述服务端在握手时出示的主机公钥。
type HostKeyInfo struct {
	Host    string
	Port    int
	KeyType string
	// Fingerprint 是 SHA256:... 形式，与 `ssh-keygen -lf` 输出一致，用户可交叉验证。
	Fingerprint string
}

// HostKeyChange 描述"指纹变化"：主机在 known_hosts 中已有记录，但本次出示的公钥不匹配。
// 处理策略是硬阻断——没有"总是忽略"档位，用户必须显式选择替换。
type HostKeyChange struct {
	Info HostKeyInfo
	// OldFingerprints 是 known_hosts 中记录的旧指纹，可能多条。
	OldFingerprints []string
}

// HostKeyPrompt 在首次见到未知主机（TOFU），或发现指纹变化时被调用。
// change 为 nil 表示未知主机；非 nil 表示指纹变化。
type HostKeyPrompt func(info HostKeyInfo, change *HostKeyChange) (HostKeyDecision, error)

// ErrHostKeyRejected 表示用户在信任决策中断开了连接。
var ErrHostKeyRejected = errors.New("sshcore: 主机密钥未获信任，连接已中断")

// NewHostKeyCallback 构造 ssh.HostKeyCallback，行为对齐 OpenSSH：
//   - 已记录且匹配 → 放行；
//   - 被吊销（@revoked）→ 硬阻断，不询问；
//   - 未知主机 → TOFU，交由 prompt 决定；
//   - 指纹变化 → 硬阻断，仅在用户显式选择"替换"时才改写记录。
func NewHostKeyCallback(knownHostsPath string, prompt HostKeyPrompt) (ssh.HostKeyCallback, error) {
	if knownHostsPath == "" {
		return nil, errors.New("sshcore: known_hosts 路径为空")
	}
	// knownhosts.New 要求文件可读，首连前确保文件存在。
	if err := ensureFile(knownHostsPath); err != nil {
		return nil, fmt.Errorf("sshcore: 准备 known_hosts: %w", err)
	}
	inner, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("sshcore: 解析 known_hosts: %w", err)
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		host, port := splitHostPort(hostname, remote)
		info := HostKeyInfo{
			Host:        host,
			Port:        port,
			KeyType:     key.Type(),
			Fingerprint: ssh.FingerprintSHA256(key),
		}

		err := inner(hostname, remote, key)
		if err == nil {
			return nil
		}

		var revoked *knownhosts.RevokedError
		if errors.As(err, &revoked) {
			return fmt.Errorf("sshcore: 主机 %s 的密钥已被 @revoked 吊销: %w", hostname, err)
		}

		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			return err
		}

		// Want 非空 => 主机已知但与本次出示的公钥不匹配，即指纹变化。
		if len(keyErr.Want) > 0 {
			change := &HostKeyChange{Info: info}
			for _, wk := range keyErr.Want {
				change.OldFingerprints = append(change.OldFingerprints, ssh.FingerprintSHA256(wk.Key))
			}
			if prompt == nil {
				return fmt.Errorf("sshcore: 主机 %s 指纹变化，已阻断（无信任回调）", hostname)
			}
			decision, perr := prompt(info, change)
			if perr != nil {
				return perr
			}
			// 指纹变化不接受"仅本次"——只能显式替换，否则一律阻断。
			if decision != HostKeyAcceptAndSave {
				return fmt.Errorf("%w（指纹变化）", ErrHostKeyRejected)
			}
			if err := writeKnownHost(knownHostsPath, hostname, key, true); err != nil {
				return fmt.Errorf("sshcore: 替换 known_hosts 条目失败: %w", err)
			}
			return nil
		}

		// Want 为空 => 未知主机，标准 TOFU。
		if prompt == nil {
			return fmt.Errorf("sshcore: 未知主机 %s（无信任回调）", hostname)
		}
		decision, perr := prompt(info, nil)
		if perr != nil {
			return perr
		}
		switch decision {
		case HostKeyAcceptAndSave:
			if err := writeKnownHost(knownHostsPath, hostname, key, false); err != nil {
				return fmt.Errorf("sshcore: 写入 known_hosts 失败: %w", err)
			}
			return nil
		case HostKeyAcceptOnce:
			return nil
		default:
			return ErrHostKeyRejected
		}
	}, nil
}

// splitHostPort 从回调参数里还原出主机与端口。
// hostname 通常是 "host:port"，而 remote 携带实际网络地址。
func splitHostPort(hostname string, remote net.Addr) (string, int) {
	if h, p, err := net.SplitHostPort(hostname); err == nil {
		port := 22
		fmt.Sscanf(p, "%d", &port)
		return h, port
	}
	if remote != nil {
		if h, p, err := net.SplitHostPort(remote.String()); err == nil {
			port := 22
			fmt.Sscanf(p, "%d", &port)
			return h, port
		}
	}
	return hostname, 22
}

// writeKnownHost 将一条记录写入 known_hosts（标准 OpenSSH 格式，可与系统文件互拷）。
// replace 为 true 时先移除同一地址上的旧条目，用于响应指纹变化。
func writeKnownHost(path, addr string, key ssh.PublicKey, replace bool) error {
	normalized := knownhosts.Normalize(addr)
	var kept []string
	if raw, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if replace {
				fields := strings.Fields(trimmed)
				if len(fields) > 0 && hostPatternMatches(fields[0], normalized) {
					continue // 丢弃旧条目（含冲突指纹）
				}
			}
			kept = append(kept, line)
		}
	}

	var b strings.Builder
	for _, line := range kept {
		b.WriteString(strings.TrimRight(line, "\r"))
		b.WriteString("\n")
	}
	b.WriteString(knownhosts.Line([]string{normalized}, key))
	b.WriteString("\n")

	return atomicWriteFile(path, []byte(b.String()), 0o600)
}

// hostPatternMatches 判断 known_hosts 首列（逗号分隔的 host 模式）是否命中目标地址。
// 哈希形式（|1|…）无法反查，一律视为不命中并保留。
func hostPatternMatches(patterns, normalized string) bool {
	for _, p := range strings.Split(patterns, ",") {
		if strings.HasPrefix(p, "|") {
			continue
		}
		if p == normalized {
			return true
		}
		// [host]:port 形式在无端口时为仅主机名。
		if v := strings.TrimSuffix(strings.TrimPrefix(p, "["), "]"); v == normalized {
			return true
		}
	}
	return false
}

func ensureFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// atomicWriteFile 以"临时文件 → fsync → rename"落盘，避免半截文件。
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后此调用无害

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
