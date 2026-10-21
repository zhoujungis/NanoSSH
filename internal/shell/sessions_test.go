package shell

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"nanossh/internal/sshcore"
	"nanossh/internal/store"
	"nanossh/internal/testsshd"
)

// passwordServer 拼一条指向靶机、用密码认证的服务器记录。
func passwordServer(host string, port int) sshcore.Server {
	return sshcore.Server{
		Name:    "靶机",
		Host:    host,
		Port:    port,
		User:    "tester",
		Auth:    sshcore.AuthPassword,
		Country: "CN",
	}
}

// eventCollector 捕获 App 发往前端的事件，替代 Wails 的 runtime.EventsEmit。
//
// 这是把 pumpSession 的"字节搬运 + 反压"逻辑、以及两条询问链路纳入测试的关键：
// 只要 emit 是可替换的，就不需要 WebView 也能跑通完整链路。
type eventCollector struct {
	mu     sync.Mutex
	data   map[string][]byte
	exits  map[string][]string
	hosts  []HostKeyPromptPayload
	passes []PassphrasePromptPayload
	// transfers 按传输 ID 记录收到的进度快照，用于断言传输终态。
	transfers map[string][]TransferStatus
	// forwards 按规则 ID 记录收到的转发状态快照。
	forwards map[string][]ForwardStatus

	// answerHostKey 决定如何回复主机密钥询问；留空视为"信任并保存"。
	answerHostKey func(HostKeyPromptPayload) string
	// answerPassphrase 决定如何回复口令询问；留空视为"取消"——
	// 这样测试里没预期的询问不会把连接永久挂住。
	answerPassphrase func(PassphrasePromptPayload) (passphrase string, cancelled bool)
}

func newCollector() *eventCollector {
	return &eventCollector{
		data:      make(map[string][]byte),
		exits:     make(map[string][]string),
		transfers: make(map[string][]TransferStatus),
		forwards:  make(map[string][]ForwardStatus),
	}
}

// handler 返回一个符合 emitFunc 签名的函数，可直接赋给 App.emit。
//
// ★ 关键点：收到终端数据后立即回 ack。这正是"前端渲染完成再回执"的等价物，
// 少了它，pumpSession 在发出第一段数据后就会永久等待（反压无法闭环）。
func (c *eventCollector) handler(a *App) emitFunc {
	return func(_ context.Context, name string, args ...any) {
		switch {
		case strings.HasPrefix(name, "term:data:"):
			sid := strings.TrimPrefix(name, "term:data:")
			if b, ok := firstArg[[]byte](args); ok {
				c.mu.Lock()
				c.data[sid] = append(c.data[sid], b...)
				c.mu.Unlock()
			}
			a.AckTermData(sid)

		case strings.HasPrefix(name, "term:exit:"):
			sid := strings.TrimPrefix(name, "term:exit:")
			if reason, ok := firstArg[string](args); ok {
				c.mu.Lock()
				c.exits[sid] = append(c.exits[sid], reason)
				c.mu.Unlock()
			}

		case name == "hostkey:prompt":
			p, ok := firstArg[HostKeyPromptPayload](args)
			if !ok {
				return
			}
			c.mu.Lock()
			c.hosts = append(c.hosts, p)
			fn := c.answerHostKey
			c.mu.Unlock()

			decision := "save"
			if fn != nil {
				decision = fn(p)
			}
			_ = a.HostKeyDecision(p.RequestID, decision)

		case name == "passphrase:prompt":
			p, ok := firstArg[PassphrasePromptPayload](args)
			if !ok {
				return
			}
			c.mu.Lock()
			c.passes = append(c.passes, p)
			fn := c.answerPassphrase
			c.mu.Unlock()

			passphrase, cancelled := "", true
			if fn != nil {
				passphrase, cancelled = fn(p)
			}
			_ = a.PassphraseDecision(p.RequestID, passphrase, cancelled)

		case name == "sftp:transfer":
			p, ok := firstArg[TransferStatus](args)
			if !ok {
				return
			}
			c.mu.Lock()
			c.transfers[p.ID] = append(c.transfers[p.ID], p)
			c.mu.Unlock()

		case name == "forward:status":
			p, ok := firstArg[ForwardStatus](args)
			if !ok {
				return
			}
			c.mu.Lock()
			c.forwards[p.ID] = append(c.forwards[p.ID], p)
			c.mu.Unlock()
		}
	}
}

func firstArg[T any](args []any) (T, bool) {
	var zero T
	if len(args) != 1 {
		return zero, false
	}
	v, ok := args[0].(T)
	return v, ok
}

func (c *eventCollector) text(sid string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.data[sid])
}

func (c *eventCollector) exitReasons(sid string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.exits[sid]...)
}

func (c *eventCollector) hostPromptCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.hosts)
}

func (c *eventCollector) passphrasePromptCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.passes)
}

// transferFinal 返回某个传输的最后一条进度快照（即终态）；从未收到事件时 ok 为 false。
func (c *eventCollector) transferFinal(id string) (TransferStatus, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := c.transfers[id]
	if len(snap) == 0 {
		return TransferStatus{}, false
	}
	return snap[len(snap)-1], true
}

// transferCount 返回某个传输已推送的进度事件条数。
func (c *eventCollector) transferCount(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.transfers[id])
}

// forwardFinal 返回某个转发规则的最后一条状态快照；从未收到事件时 ok 为 false。
func (c *eventCollector) forwardFinal(id string) (ForwardStatus, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := c.forwards[id]
	if len(snap) == 0 {
		return ForwardStatus{}, false
	}
	return snap[len(snap)-1], true
}

// waitFor 轮询等待条件成立；超时即判失败，用于把"死锁"变成可读的测试失败。
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待 %q 超时", what)
}

// newTestApp 构造一个指向临时数据目录、事件与凭据后端都被替换掉的 App。
// 凭据后端换成内存实现，测试绝不往真实的 Windows 凭据管理器里写东西。
func newTestApp(t *testing.T) (*App, *eventCollector) {
	t.Helper()
	t.Setenv("NANOSSH_DATA_DIR", t.TempDir())

	a := NewApp()
	a.Startup(context.Background())
	a.secrets = store.NewMemSecretStore()

	c := newCollector()
	a.emit = c.handler(a)
	return a, c
}

// newRelay 启动一个进程内 SSH 靶机。
func newRelay(t *testing.T, user, password string) *testsshd.Server {
	t.Helper()
	srv, err := testsshd.New(user, password)
	if err != nil {
		t.Fatalf("启动测试靶机失败: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// startPair 完成"连接 + 启动 shell"，返回可直接读写 stdio 的会话。
func startPair(t *testing.T) (*App, *testsshd.Server, string, *eventCollector) {
	t.Helper()
	srv := newRelay(t, "tester", "s3cret")
	a, c := newTestApp(t)
	host, port := srv.HostPort()

	rec, err := a.SaveServer(passwordServer(host, port))
	if err != nil {
		t.Fatalf("保存服务器失败: %v", err)
	}

	conn, err := a.Connect(ConnectRequest{ServerID: rec.ID, Password: "s3cret"})
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}

	if _, err := a.StartShell(StartShellRequest{
		SessionID: conn.SessionID,
		Rows:      24,
		Cols:      80,
	}); err != nil {
		t.Fatalf("启动 shell 失败: %v", err)
	}

	t.Cleanup(func() { _ = a.CloseSession(conn.SessionID) })
	return a, srv, conn.SessionID, c
}

// TestTerminalSlice 覆盖 P1 垂直切片的后端闭环：
// 连接（含 TOFU）→ shell → 字节流过反压链路 → 初始尺寸 → resize → exit。
func TestTerminalSlice(t *testing.T) {
	a, srv, sid, c := startPair(t)

	if c.hostPromptCount() != 1 {
		t.Fatalf("未知主机应触发 1 次 TOFU 询问，实际 %d 次", c.hostPromptCount())
	}

	// 1) 反压链路打通：靶机 banner 必须真正抵达收集器，且没有死锁。
	waitFor(t, "靶机 banner", func() bool {
		return strings.Contains(c.text(sid), "nanossh-test-sshd")
	})

	// 2) 初始 PTY 尺寸必须如实传到远端。
	if err := a.WriteStdin(sid, "stty size\n"); err != nil {
		t.Fatalf("写入 stdin 失败: %v", err)
	}
	waitFor(t, "初始 PTY 尺寸 24 80", func() bool {
		return strings.Contains(c.text(sid), "24 80")
	})

	// 3) resize 后再确认：这是 (rows, cols) 顺序陷阱的回归点。
	// window-change 是"发后不管"，先等靶机记录到新尺寸，再查询，避免竞态。
	if err := a.ResizeShell(sid, 30, 100); err != nil {
		t.Fatalf("变更窗口尺寸失败: %v", err)
	}
	waitFor(t, "靶机记录新 PTY 尺寸", func() bool {
		_, rows, cols := srv.LastPTY()
		return rows == 30 && cols == 100
	})
	if err := a.WriteStdin(sid, "stty size\n"); err != nil {
		t.Fatalf("写入 stdin 失败: %v", err)
	}
	waitFor(t, "新 PTY 尺寸 30 100", func() bool {
		return strings.Contains(c.text(sid), "30 100")
	})

	// 4) exit → term:exit 事件（会话结束必须被显式通知前端）。
	if err := a.WriteStdin(sid, "exit\n"); err != nil {
		t.Fatalf("写入 stdin 失败: %v", err)
	}
	waitFor(t, "bye", func() bool {
		return strings.Contains(c.text(sid), "bye")
	})
	waitFor(t, "term:exit 事件", func() bool {
		return len(c.exitReasons(sid)) > 0
	})
}

// TestTerminalBackpressureNoDataLoss 用单行 40000 字节回显压过 termMaxChunk(16 KiB)，
// 强制走多轮 flush → ack。若反压实现有丢字节或自锁，这里会超时或长度不足。
func TestTerminalBackpressureNoDataLoss(t *testing.T) {
	a, _, sid, c := startPair(t)

	const payload = 40000
	if err := a.WriteStdin(sid, "echo "+strings.Repeat("x", payload)+"\n"); err != nil {
		t.Fatalf("写入 stdin 失败: %v", err)
	}

	waitFor(t, "40000 字节完整回显", func() bool {
		return strings.Count(c.text(sid), "x") >= payload
	})
}
