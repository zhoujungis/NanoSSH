package sshcore_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"nanossh/internal/sshcore"
)

// TestHostKeyTOFU 覆盖信任模型的三档行为：
// 首次询问并保存 → 之后不再询问 → 指纹变化必须硬阻断。
func TestHostKeyTOFU(t *testing.T) {
	s := startServer(t)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")

	var prompts int
	var lastChange *sshcore.HostKeyChange
	prompt := func(info sshcore.HostKeyInfo, change *sshcore.HostKeyChange) (sshcore.HostKeyDecision, error) {
		prompts++
		lastChange = change
		if change != nil {
			// 指纹变化：本用例的预期是被调用方阻断，这里先返回拒绝。
			return sshcore.HostKeyReject, nil
		}
		return sshcore.HostKeyAcceptAndSave, nil
	}

	// ① 首次连接：未知主机 → 询问一次并写入 known_hosts。
	cli, err := connectWith(t, s, knownHosts, prompt)
	if err != nil {
		t.Fatalf("首次连接失败: %v", err)
	}
	cli.Close()
	if prompts != 1 {
		t.Fatalf("首次连接应询问 1 次，实际 %d", prompts)
	}

	// ② 再次连接：已记录且匹配 → 不再询问。
	cli2, err := connectWith(t, s, knownHosts, prompt)
	if err != nil {
		t.Fatalf("已知主机连接失败: %v", err)
	}
	cli2.Close()
	if prompts != 1 {
		t.Fatalf("已知主机不应再次询问，累计询问 %d 次", prompts)
	}

	// ③ 服务端更换主机密钥 → 同一地址、不同指纹 → 必须阻断。
	if err := s.RotateHostKey(); err != nil {
		t.Fatalf("更换主机密钥失败: %v", err)
	}
	if _, err := connectWith(t, s, knownHosts, prompt); err == nil {
		t.Fatal("指纹变化时必须阻断连接，但本次连接成功了")
	} else if !errors.Is(err, sshcore.ErrHostKeyRejected) && !strings.Contains(err.Error(), "主机密钥未获信任") {
		t.Fatalf("指纹变化应产生 ErrHostKeyRejected，实际为: %v", err)
	}
	if prompts != 2 {
		t.Fatalf("指纹变化应询问 1 次，累计询问 %d 次", prompts)
	}
	if lastChange == nil {
		t.Fatal("指纹变化时回调未收到 HostKeyChange")
	}
	if len(lastChange.OldFingerprints) == 0 {
		t.Fatal("HostKeyChange 未携带旧指纹，UI 无法展示差异")
	}
	if lastChange.Info.Fingerprint == "" {
		t.Fatal("HostKeyChange 未携带新指纹")
	}
}

// TestHostKeyAcceptOnceNotPersisted 验证"仅本次信任"不会写入 known_hosts——
// 下一次连接仍会询问，这是用户可撤销的信任。
func TestHostKeyAcceptOnceNotPersisted(t *testing.T) {
	s := startServer(t)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")

	var prompts int
	prompt := func(sshcore.HostKeyInfo, *sshcore.HostKeyChange) (sshcore.HostKeyDecision, error) {
		prompts++
		return sshcore.HostKeyAcceptOnce, nil
	}

	for i := 1; i <= 2; i++ {
		cli, err := connectWith(t, s, knownHosts, prompt)
		if err != nil {
			t.Fatalf("第 %d 次连接失败: %v", i, err)
		}
		cli.Close()
	}
	if prompts != 2 {
		t.Fatalf("仅本次信任不应落盘，两次连接都应询问，实际询问 %d 次", prompts)
	}
}

// TestHostKeyRejectUnknownHost 验证未知主机在无信任回调时会被阻断（默认安全）。
func TestHostKeyRejectUnknownHost(t *testing.T) {
	s := startServer(t)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")

	if _, err := connectWith(t, s, knownHosts, nil); err == nil {
		t.Fatal("无信任回调时未知主机必须被阻断，但连接成功了")
	}
}
