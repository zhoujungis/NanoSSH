package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nanossh/internal/sshcore"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "servers.json")
	return New(path), path
}

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	s, _ := newStore(t)

	f, err := s.Load()
	if err != nil {
		t.Fatalf("文件不存在时 Load 不应报错: %v", err)
	}
	if f.Version != CurrentVersion {
		t.Fatalf("版本 = %d，期望 %d", f.Version, CurrentVersion)
	}
	if len(f.Servers) != 0 {
		t.Fatalf("期望空清单，实际 %d 条", len(f.Servers))
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s, path := newStore(t)

	in := &File{
		Servers: []sshcore.Server{
			{ID: NewID(), Name: "VPS-01", Country: "US", Host: "1.2.3.4", Port: 22, User: "root", Auth: sshcore.AuthPassword},
			{ID: NewID(), Name: "VPS-02", Country: "HK", Host: "5.6.7.8", Port: 2222, User: "ubuntu", Auth: sshcore.AuthKey,
				Forwards: []sshcore.ForwardRule{{ID: NewID(), Kind: sshcore.ForwardLocal, LocalAddr: "127.0.0.1:8080", RemoteAddr: "127.0.0.1:80", AutoStart: true}}},
		},
		Settings: Settings{FontSize: 14, Scrollback: 10000},
	}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("servers.json 未生成: %v", err)
	}

	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if len(out.Servers) != 2 {
		t.Fatalf("服务器数 = %d，期望 2", len(out.Servers))
	}
	if out.Servers[0].Name != "VPS-01" || out.Servers[0].Country != "US" {
		t.Fatalf("第 1 条记录不符: %+v", out.Servers[0])
	}
	if len(out.Servers[1].Forwards) != 1 || out.Servers[1].Forwards[0].Kind != sshcore.ForwardLocal {
		t.Fatalf("转发规则未正确往返: %+v", out.Servers[1].Forwards)
	}
	if out.Settings.FontSize != 14 || out.Settings.Scrollback != 10000 {
		t.Fatalf("设置未正确往返: %+v", out.Settings)
	}
}

// TestSaveKeepsBackup 验证覆盖写时保留上一版，供手工回滚。
func TestSaveKeepsBackup(t *testing.T) {
	s, path := newStore(t)

	first := &File{Servers: []sshcore.Server{{ID: "A", Name: "第一版"}}}
	if err := s.Save(first); err != nil {
		t.Fatalf("首次 Save 失败: %v", err)
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Fatal("首次写入不应产生 .bak")
	}

	second := &File{Servers: []sshcore.Server{{ID: "A", Name: "第二版"}}}
	if err := s.Save(second); err != nil {
		t.Fatalf("二次 Save 失败: %v", err)
	}

	raw, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("未生成 .bak: %v", err)
	}
	if !strings.Contains(string(raw), "第一版") {
		t.Fatalf(".bak 未保留上一版内容: %s", raw)
	}
}

// TestSaveLeavesNoTempFiles 验证"临时文件 → rename"不留残渣。
func TestSaveLeavesNoTempFiles(t *testing.T) {
	s, path := newStore(t)
	dir := filepath.Dir(path)

	for i := 0; i < 3; i++ {
		if err := s.Save(&File{Servers: []sshcore.Server{{ID: "A", Name: "n"}}}); err != nil {
			t.Fatalf("Save 失败: %v", err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("残留临时文件: %s", e.Name())
		}
	}
}

// TestVersionZeroTreatedAsV1 验证没有 version 字段的历史文件可被平滑升级。
func TestVersionZeroTreatedAsV1(t *testing.T) {
	s, path := newStore(t)
	legacy := `{"servers":[{"id":"A","name":"老文件","host":"1.1.1.1","port":22,"user":"root","auth":"password"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := s.Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if f.Version != CurrentVersion {
		t.Fatalf("版本 = %d，期望升级到 %d", f.Version, CurrentVersion)
	}
	if len(f.Servers) != 1 || f.Servers[0].Name != "老文件" {
		t.Fatalf("历史数据未保留: %+v", f.Servers)
	}
}

// TestRejectFutureVersion 验证来自更高版本的配置不会被静默降级写坏。
func TestRejectFutureVersion(t *testing.T) {
	s, path := newStore(t)
	if err := os.WriteFile(path, []byte(`{"version":999,"servers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("高于当前支持的版本必须报错")
	}
}

func TestNewIDShapeAndUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	prev := ""
	for i := 0; i < 1000; i++ {
		id := NewID()
		if len(id) != 26 {
			t.Fatalf("ULID 长度 = %d，期望 26（%q）", len(id), id)
		}
		for _, r := range id {
			if !strings.ContainsRune(crockford, r) {
				t.Fatalf("ULID 含非法字符 %q: %q", r, id)
			}
		}
		if seen[id] {
			t.Fatalf("ULID 重复: %q", id)
		}
		seen[id] = true

		// 时间戳编码进前 10 个字符（48 位时间戳填满 char0..char9），
		// 因此前缀必然单调不减；剩余 16 个字符是随机位，不参与排序保证。
		if prev != "" && id[:10] < prev[:10] {
			t.Fatalf("ULID 时间前缀倒退: %q < %q", id[:10], prev[:10])
		}
		prev = id
	}
}

func TestPathsResolveWithOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NANOSSH_DATA_DIR", dir)

	p, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if p.Dir != dir {
		t.Fatalf("Dir = %q，期望 %q", p.Dir, dir)
	}
	if filepath.Base(p.Servers) != "servers.json" || filepath.Base(p.KnownHosts) != "known_hosts" {
		t.Fatalf("路径不符合预期: %+v", p)
	}
	if err := p.Ensure(); err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	for _, d := range []string{p.Keys, p.Logs} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Fatalf("目录未创建: %s (%v)", d, err)
		}
	}
}
