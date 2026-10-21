package shell

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nanossh/internal/sshcore"
	"nanossh/internal/store"
)

// TestConnectWithManagedKey 是一条完整的 P2 主链路：
// 用 NanoSSH 自己生成密钥 → 把公钥交给服务端授权 → 保存服务器（auth=key）→ 连接成功。
func TestConnectWithManagedKey(t *testing.T) {
	srv := newRelay(t, "tester", "unused-password")
	a, c := newTestApp(t)

	ref, err := a.GenerateKey(GenerateKeyRequest{Name: "测试密钥"})
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}

	// 私钥文件必须真的落盘。
	// 注意：Windows 上 os.WriteFile 的 0600 只是"意图"，实际访问控制来自
	// %APPDATA% 目录继承的 ACL（本身即按用户隔离），因此这里不断言 Mode().Perm()。
	if _, err := os.Stat(ref.Path); err != nil {
		t.Fatalf("私钥文件不存在: %v", err)
	}

	// 用公钥行喂给服务端——这一步同时验证了"复制的公钥可用"。
	pub, err := sshcore.DecodePublicKeyLine(ref.PublicKey)
	if err != nil {
		t.Fatalf("解析公钥行失败: %v", err)
	}
	srv.AuthorizeKey(pub)

	host, port := srv.HostPort()
	srvRec, err := a.SaveServer(sshcore.Server{
		Name: "密钥靶机", Host: host, Port: port, User: "tester",
		Auth: sshcore.AuthKey, KeyID: ref.ID,
	})
	if err != nil {
		t.Fatalf("保存服务器失败: %v", err)
	}

	conn, err := a.Connect(ConnectRequest{ServerID: srvRec.ID})
	if err != nil {
		t.Fatalf("密钥登录失败: %v", err)
	}
	defer a.CloseSession(conn.SessionID)

	if got := srv.LastAuth(); got != "publickey" {
		t.Fatalf("认证方式 = %q，期望 publickey", got)
	}
	if c.passphrasePromptCount() != 0 {
		t.Fatalf("未加密的密钥不应询问口令，实际询问 %d 次", c.passphrasePromptCount())
	}
}

// TestPassphraseKeyPromptedOnce 是计划里的验收项："带 passphrase 的密钥重启应用后只问一次"。
// 这里用"把已保存口令删掉"来模拟重启后的状态，验证：问一次 → 落库 → 再连不再问。
func TestPassphraseKeyPromptedOnce(t *testing.T) {
	const passphrase = "correct horse battery"

	srv := newRelay(t, "tester", "unused-password")
	a, c := newTestApp(t)

	ref, err := a.GenerateKey(GenerateKeyRequest{Name: "加密密钥", Passphrase: passphrase})
	if err != nil {
		t.Fatalf("生成带口令密钥失败: %v", err)
	}
	pub, err := sshcore.DecodePublicKeyLine(ref.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	srv.AuthorizeKey(pub)

	// 模拟"重启后凭据管理器里还没有这把密钥的口令"。
	if err := a.secrets.Delete(store.PassphraseTarget(ref.ID)); err != nil {
		t.Fatal(err)
	}

	c.answerPassphrase = func(p PassphrasePromptPayload) (string, bool) {
		if p.Retry {
			t.Errorf("首次询问不该带 retry 标记")
		}
		return passphrase, false
	}

	host, port := srv.HostPort()
	srvRec, err := a.SaveServer(sshcore.Server{
		Name: "加密靶机", Host: host, Port: port, User: "tester",
		Auth: sshcore.AuthKey, KeyID: ref.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	conn, err := a.Connect(ConnectRequest{ServerID: srvRec.ID})
	if err != nil {
		t.Fatalf("第一次连接失败: %v", err)
	}
	_ = a.CloseSession(conn.SessionID)

	if c.passphrasePromptCount() != 1 {
		t.Fatalf("第一次连接应询问 1 次口令，实际 %d 次", c.passphrasePromptCount())
	}
	stored, err := a.secrets.Get(store.PassphraseTarget(ref.ID))
	if err != nil {
		t.Fatalf("口令未落库: %v", err)
	}
	if stored != passphrase {
		t.Fatalf("落库口令 = %q，期望 %q", stored, passphrase)
	}

	// 第二次连接：凭据管理器里已有口令，不该再打扰用户。
	conn2, err := a.Connect(ConnectRequest{ServerID: srvRec.ID})
	if err != nil {
		t.Fatalf("第二次连接失败: %v", err)
	}
	defer a.CloseSession(conn2.SessionID)

	if c.passphrasePromptCount() != 1 {
		t.Fatalf("第二次连接不应再询问口令，实际共询问 %d 次", c.passphrasePromptCount())
	}
}

// TestPassphraseWrongThenCorrect 验证口令打错后可以在同一次连接里重试，
// 而不是让用户重新走一遍连接流程。
func TestPassphraseWrongThenCorrect(t *testing.T) {
	const passphrase = "right-one"

	srv := newRelay(t, "tester", "unused-password")
	a, c := newTestApp(t)

	ref, err := a.GenerateKey(GenerateKeyRequest{Name: "加密密钥", Passphrase: passphrase})
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := sshcore.DecodePublicKeyLine(ref.PublicKey)
	srv.AuthorizeKey(pub)
	_ = a.secrets.Delete(store.PassphraseTarget(ref.ID))

	calls := 0
	c.answerPassphrase = func(p PassphrasePromptPayload) (string, bool) {
		calls++
		if calls == 1 {
			if p.Retry {
				t.Error("第一次询问不该带 retry 标记")
			}
			return "wrong-one", false
		}
		if !p.Retry {
			t.Error("重试时前端应收到 retry 标记，才能提示用户口令不对")
		}
		return passphrase, false
	}

	host, port := srv.HostPort()
	srvRec, err := a.SaveServer(sshcore.Server{
		Name: "加密靶机", Host: host, Port: port, User: "tester",
		Auth: sshcore.AuthKey, KeyID: ref.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	conn, err := a.Connect(ConnectRequest{ServerID: srvRec.ID})
	if err != nil {
		t.Fatalf("口令修正后仍连接失败: %v", err)
	}
	defer a.CloseSession(conn.SessionID)

	if calls != 2 {
		t.Fatalf("应询问 2 次（先错后对），实际 %d 次", calls)
	}
}

// TestPassphraseCancelled 用户放弃输入口令时连接必须干净地中止。
func TestPassphraseCancelled(t *testing.T) {
	srv := newRelay(t, "tester", "unused-password")
	a, c := newTestApp(t)

	ref, err := a.GenerateKey(GenerateKeyRequest{Name: "加密密钥", Passphrase: "p"})
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := sshcore.DecodePublicKeyLine(ref.PublicKey)
	srv.AuthorizeKey(pub)
	_ = a.secrets.Delete(store.PassphraseTarget(ref.ID))

	c.answerPassphrase = func(PassphrasePromptPayload) (string, bool) { return "", true }

	host, port := srv.HostPort()
	srvRec, err := a.SaveServer(sshcore.Server{
		Name: "加密靶机", Host: host, Port: port, User: "tester",
		Auth: sshcore.AuthKey, KeyID: ref.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.Connect(ConnectRequest{ServerID: srvRec.ID}); err == nil {
		t.Fatal("取消口令输入后应中止连接")
	}
	if srv.LastAuth() == "publickey" {
		t.Fatal("取消口令后不该完成认证")
	}
}

// TestPasswordSavedAndReused 密码认证：成功连一次后落库，之后不再需要前端提供。
func TestPasswordSavedAndReused(t *testing.T) {
	srv := newRelay(t, "tester", "s3cret")
	a, _ := newTestApp(t)

	host, port := srv.HostPort()
	rec, err := a.SaveServer(passwordServer(host, port))
	if err != nil {
		t.Fatal(err)
	}

	if a.HasPassword(rec.ID) {
		t.Fatal("尚未连过，不该报告已有保存的密码")
	}

	conn, err := a.Connect(ConnectRequest{ServerID: rec.ID, Password: "s3cret"})
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	_ = a.CloseSession(conn.SessionID)

	if !a.HasPassword(rec.ID) {
		t.Fatal("连接成功后密码应已落库")
	}
	stored, err := a.secrets.Get(store.PasswordTarget(rec.ID))
	if err != nil {
		t.Fatal(err)
	}
	if stored != "s3cret" {
		t.Fatalf("口令落库 = %q，期望 s3cret", stored)
	}

	// 第二次连接完全不传密码，必须用已保存的密码连上。
	conn2, err := a.Connect(ConnectRequest{ServerID: rec.ID})
	if err != nil {
		t.Fatalf("复用已保存密码失败: %v", err)
	}
	defer a.CloseSession(conn2.SessionID)
	if srv.LastAuth() != "password" {
		t.Fatalf("认证方式 = %q，期望 password", srv.LastAuth())
	}
}

// TestForgotPasswordClearsSecret 是"清除已保存密码"：用户在别处改了远端密码时的唯一出口。
func TestForgotPasswordClearsSecret(t *testing.T) {
	srv := newRelay(t, "tester", "s3cret")
	a, _ := newTestApp(t)

	host, port := srv.HostPort()
	rec, err := a.SaveServer(passwordServer(host, port))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.secrets.Set(store.PasswordTarget(rec.ID), "s3cret"); err != nil {
		t.Fatal(err)
	}
	if !a.HasPassword(rec.ID) {
		t.Fatal("前置条件失败：应有保存的密码")
	}

	if err := a.ForgetPassword(rec.ID); err != nil {
		t.Fatalf("清除密码失败: %v", err)
	}
	if a.HasPassword(rec.ID) {
		t.Fatal("清除后不该再报告已有密码")
	}
	// 清除后连接（不带密码）必须明确报"尚未提供密码"，而不是拿空密码去撞认证。
	if _, err := a.Connect(ConnectRequest{ServerID: rec.ID}); err == nil {
		t.Fatal("无密码时连接应报错")
	}
}

// TestStalePasswordSelfHeals 远端密码已改（本地存的是旧的）时的自愈路径：
// 认证失败要顺手清掉失效密码，否则用户会被永久卡在"点了就失败"。
func TestStalePasswordSelfHeals(t *testing.T) {
	srv := newRelay(t, "tester", "s3cret")
	a, _ := newTestApp(t)

	host, port := srv.HostPort()
	rec, err := a.SaveServer(passwordServer(host, port))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.secrets.Set(store.PasswordTarget(rec.ID), "old-password"); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Connect(ConnectRequest{ServerID: rec.ID}); err == nil {
		t.Fatal("错误密码不该连上")
	}
	if a.HasPassword(rec.ID) {
		t.Fatal("认证失败后应清掉失效密码，让下次连接重新询问")
	}
}

// TestServerDeleteRemovesSecret 删服务器必须连带清掉它的密码，不留孤儿凭据。
func TestServerDeleteRemovesSecret(t *testing.T) {
	a, _ := newTestApp(t)

	rec, err := a.SaveServer(passwordServer("127.0.0.1", 22))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.secrets.Set(store.PasswordTarget(rec.ID), "pw"); err != nil {
		t.Fatal(err)
	}

	if err := a.DeleteServer(rec.ID); err != nil {
		t.Fatalf("删除服务器失败: %v", err)
	}
	if _, err := a.secrets.Get(store.PasswordTarget(rec.ID)); !errors.Is(err, store.ErrSecretNotFound) {
		t.Fatalf("删除服务器后密码应被清除，实际: %v", err)
	}
}

// TestImportKeyFromFile 覆盖"从 ~/.ssh 导入已有私钥"这条验收路径。
func TestImportKeyFromFile(t *testing.T) {
	a, _ := newTestApp(t)

	// 用 sshcore 造一把独立于 NanoSSH 管理目录的密钥，模拟用户的 ~/.ssh/id_ed25519。
	km, err := sshcore.GenerateKey(sshcore.AlgoEd25519, "user@laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(src, km.PrivatePEM, 0o600); err != nil {
		t.Fatal(err)
	}

	ref, err := a.ImportKey(ImportKeyRequest{SourcePath: src})
	if err != nil {
		t.Fatalf("导入私钥失败: %v", err)
	}

	// 名字应默认取源文件名，指纹与公钥必须与源密钥一致。
	if ref.Name != "id_ed25519" {
		t.Fatalf("默认名称 = %q，期望 id_ed25519", ref.Name)
	}
	if ref.Fingerprint != km.Fingerprint {
		t.Fatalf("指纹 = %q，期望 %q", ref.Fingerprint, km.Fingerprint)
	}
	if ref.PublicKey != km.PublicLine {
		t.Fatalf("公钥行与源密钥不一致")
	}
	if ref.Type != string(sshcore.AlgoEd25519) {
		t.Fatalf("算法 = %q，期望 ed25519", ref.Type)
	}

	// 导入的私钥内容必须与源文件完全一致（原样搬运，不重新编码）。
	got, err := os.ReadFile(ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(km.PrivatePEM) {
		t.Fatal("导入后的私钥内容与源文件不一致")
	}

	keys, err := a.ListKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].ID != ref.ID {
		t.Fatalf("ListKeys 结果与导入结果不符: %+v", keys)
	}
}

// TestImportEncryptedKeyNeedsPassphrase 导入受口令保护的密钥时，
// 口令缺失/错误都必须明确失败，绝不留下"记录有了但指纹是空的"半成品。
func TestImportEncryptedKeyNeedsPassphrase(t *testing.T) {
	a, _ := newTestApp(t)

	km, err := sshcore.GenerateKey(sshcore.AlgoEd25519, "user@laptop", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(src, km.PrivatePEM, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.ImportKey(ImportKeyRequest{SourcePath: src}); !errors.Is(err, sshcore.ErrKeyEncrypted) {
		t.Fatalf("未提供口令时应返回 ErrKeyEncrypted，实际: %v", err)
	}
	if _, err := a.ImportKey(ImportKeyRequest{SourcePath: src, Passphrase: "wrong"}); err == nil {
		t.Fatal("错误口令应导入失败")
	}

	ref, err := a.ImportKey(ImportKeyRequest{SourcePath: src, Passphrase: "s3cret"})
	if err != nil {
		t.Fatalf("正确口令应导入成功: %v", err)
	}
	if ref.Fingerprint != km.Fingerprint {
		t.Fatalf("指纹 = %q，期望 %q", ref.Fingerprint, km.Fingerprint)
	}
	// 口令顺手存下，避免第一次连接又弹一次框。
	if stored, err := a.secrets.Get(store.PassphraseTarget(ref.ID)); err != nil || stored != "s3cret" {
		t.Fatalf("导入时未保存口令: %q / %v", stored, err)
	}

	// 失败的导入不该在列表里留下记录。
	keys, err := a.ListKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("只应有 1 把成功导入的密钥，实际 %d 把", len(keys))
	}
}

// TestDeleteKeyBlockedWhileInUse 仍被服务器引用的密钥必须拒绝删除，
// 且报错要指名道姓说清是哪些服务器在用。
func TestDeleteKeyBlockedWhileInUse(t *testing.T) {
	a, _ := newTestApp(t)

	ref, err := a.GenerateKey(GenerateKeyRequest{Name: "在用密钥"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveServer(sshcore.Server{
		Name: "生产机", Host: "10.0.0.1", Port: 22, User: "root",
		Auth: sshcore.AuthKey, KeyID: ref.ID,
	}); err != nil {
		t.Fatal(err)
	}

	err = a.DeleteKey(ref.ID)
	if err == nil {
		t.Fatal("被引用的密钥不该允许删除")
	}
	if !strings.Contains(err.Error(), "生产机") {
		t.Fatalf("报错应指出具体是哪台服务器在用，实际: %v", err)
	}
	if _, statErr := os.Stat(ref.Path); statErr != nil {
		t.Fatal("被拒绝删除时私钥文件必须原样保留")
	}

	// 解除引用后即可删除，文件与口令一并清掉。
	if err := a.DeleteServer(mustListServers(t, a)[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteKey(ref.ID); err != nil {
		t.Fatalf("解除引用后应能删除: %v", err)
	}
	if _, statErr := os.Stat(ref.Path); !os.IsNotExist(statErr) {
		t.Fatalf("删除后私钥文件应消失，实际: %v", statErr)
	}
}

// TestConnectKeyMissingKey 服务器引用了一把不存在的密钥，必须给出可理解的报错。
func TestConnectKeyMissingKey(t *testing.T) {
	a, _ := newTestApp(t)

	rec, err := a.SaveServer(sshcore.Server{
		Name: "坏配置", Host: "127.0.0.1", Port: 22, User: "root",
		Auth: sshcore.AuthKey, KeyID: "01NOTEXIST",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Connect(ConnectRequest{ServerID: rec.ID})
	if err == nil {
		t.Fatal("引用不存在的密钥时应报错")
	}
	if !strings.Contains(err.Error(), "未找到") {
		t.Fatalf("报错文案不够明确: %v", err)
	}
}

// TestAgentWithoutRunningAgent 在 Windows 上真实走一遍命名管道路径。
// 机器上没有运行 ssh-agent 时，必须给出"哪个服务没起"级别的提示，
// 而不是把底层 Win32 错误直接甩给用户。
func TestAgentWithoutRunningAgent(t *testing.T) {
	if os.Getenv("SSH_AUTH_SOCK") != "" {
		t.Skip("本机似乎有可用的 ssh-agent，跳过")
	}
	a, _ := newTestApp(t)

	rec, err := a.SaveServer(sshcore.Server{
		Name: "agent 靶机", Host: "127.0.0.1", Port: 22, User: "root",
		Auth: sshcore.AuthAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Connect(ConnectRequest{ServerID: rec.ID})
	if err == nil {
		t.Skip("本机 ssh-agent 可用且认证意外成功，跳过")
	}
	if !strings.Contains(err.Error(), "ssh-agent") {
		t.Fatalf("agent 不可用时的报错应明确提到 ssh-agent，实际: %v", err)
	}
}

func mustListServers(t *testing.T, a *App) []sshcore.Server {
	t.Helper()
	list, err := a.ListServers()
	if err != nil {
		t.Fatal(err)
	}
	return list
}
