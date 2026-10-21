package shell

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"nanossh/internal/sshcore"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// 终端数据流的两个关键常量。
//
// ★ 反压是低延迟的真正来源，而不是"把数据尽快推给前端"。
// 后端一次只发一段，前端在 xterm.js 的 write 回调（= 渲染完成）里回 ack，
// 后端收到 ack 才继续读下一段。此时 x/crypto/ssh 的 channel 窗口自然把压力
// 传导回远端 shell 的 write()，形成无损、正确的端到端反压。
const (
	termMaxChunk   = 16 << 10             // 单次事件上限：16 KiB
	termMaxDelay   = 8 * time.Millisecond // 攒批上限：8ms，避免小包（如按键回显）拖慢
	termAckTimeout = 30 * time.Second     // 前端异常时兜底，防止读协程永久挂起
)

// shellSession 是一条已认证连接 + 其上运行的交互式 shell。
type shellSession struct {
	id     string
	server sshcore.Server
	client *sshcore.Client
	ctx    context.Context
	// emit 是"发往 WebView 的事件"通道。默认是 Wails 的 EventsEmit，
	// 这里做成可替换的函数，让字节搬运与反压逻辑能在无 UI 的情况下被测试。
	emit emitFunc

	mu    sync.Mutex
	shell *sshcore.Shell
	// sftp 是挂在同一条连接上的文件通道，与 shell 并存、随会话一起关闭。
	sftp *sshcore.SFTP
	rows int
	cols int

	// ackCh 容量为 1：前端可能先于后端等待而回 ack。
	ackCh chan struct{}
	done  chan struct{}

	closeOnce sync.Once
}

// emitFunc 与 Wails 的 runtime.EventsEmit 同签名，便于直接赋值替换。
type emitFunc func(ctx context.Context, eventName string, data ...any)

func newShellSession(
	ctx context.Context,
	id string,
	server sshcore.Server,
	client *sshcore.Client,
	emit emitFunc,
) *shellSession {
	return &shellSession{
		id:     id,
		server: server,
		client: client,
		ctx:    ctx,
		emit:   emit,
		ackCh:  make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
}

// ShellInfo 是 StartShell 的返回：前端据此初始化 xterm 尺寸。
type ShellInfo struct {
	SessionID string `json:"sessionId"`
	Rows      int    `json:"rows"`
	Cols      int    `json:"cols"`
}

// StartShellRequest 是启动交互式 shell 的输入。
type StartShellRequest struct {
	SessionID string `json:"sessionId"`
	Rows      int    `json:"rows"`
	Cols      int    `json:"cols"`
	Term      string `json:"term,omitempty"`
}

// StartShell 在已建立的连接上请求 PTY 并启动 shell，随后开始按帧推送数据。
//
// ★ 参数顺序：本 API 与 sshcore 一致，均为 (rows, cols)——行在前、列在后。
// 前端 xterm 的 cols/rows 与之对应关系见 TerminalView.ts。
func (a *App) StartShell(req StartShellRequest) (*ShellInfo, error) {
	sess, err := a.session(req.SessionID)
	if err != nil {
		return nil, err
	}

	rows, cols := req.Rows, req.Cols
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}

	sh, err := sess.client.StartShell(sshcore.ShellOptions{Term: req.Term, Rows: rows, Cols: cols})
	if err != nil {
		return nil, err
	}

	sess.mu.Lock()
	sess.shell = sh
	sess.rows, sess.cols = rows, cols
	sess.mu.Unlock()

	go a.pumpSession(sess)

	return &ShellInfo{SessionID: sess.id, Rows: rows, Cols: cols}, nil
}

// WriteStdin 把键盘输入送到远端。输入量级极小，可安全走 JSON 文本。
func (a *App) WriteStdin(sessionID, data string) error {
	sess, err := a.session(sessionID)
	if err != nil {
		return err
	}
	sess.mu.Lock()
	sh := sess.shell
	sess.mu.Unlock()
	if sh == nil {
		return errors.New("terminal: 会话尚未启动 shell")
	}
	if data == "" {
		return nil
	}
	_, err = sh.Stdin().Write([]byte(data))
	return err
}

// ResizeShell 变更远端 PTY 尺寸（rows 行、cols 列）。
// window-change 是"发后不管"，调用方应做去抖（前端 150ms）。
func (a *App) ResizeShell(sessionID string, rows, cols int) error {
	sess, err := a.session(sessionID)
	if err != nil {
		return err
	}
	sess.mu.Lock()
	sh := sess.shell
	sess.mu.Unlock()
	if sh == nil {
		return errors.New("terminal: 会话尚未启动 shell")
	}
	if err := sh.Resize(rows, cols); err != nil {
		return err
	}
	sess.mu.Lock()
	sess.rows, sess.cols = rows, cols
	sess.mu.Unlock()
	return nil
}

// AckTermData 是前端在 xterm.js 渲染完成后的回执，用于放开后端继续读取。
func (a *App) AckTermData(sessionID string) {
	sess, err := a.session(sessionID)
	if err != nil {
		return
	}
	select {
	case sess.ackCh <- struct{}{}:
	default: // 已经有未消费的 ack，忽略重复回执
	}
}

// CloseSession 关闭 shell 与底层连接，可重复调用。
func (a *App) CloseSession(sessionID string) error {
	a.mu.Lock()
	sess, ok := a.sessions[sessionID]
	delete(a.sessions, sessionID)
	a.mu.Unlock()
	if !ok {
		return nil
	}
	// 会话没了，挂在它上面的转发规则也一并清掉（含监听端口）。
	a.dropForwards(sessionID)
	sess.close()
	return nil
}

func (a *App) session(id string) (*shellSession, error) {
	a.mu.Lock()
	sess, ok := a.sessions[id]
	a.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("session: 未找到会话 %s", id)
	}
	return sess, nil
}

// pumpSession 是后端的核心读取循环：合并 stdout/stderr，攒批后推给前端，等 ack 再继续。
//
// ★ 绝不做行切分、绝不做 UTF-8 解码：一个汉字可能跨 chunk 边界，
// 中途 string(buf) 必然产生 U+FFFD。全程 []byte 直通，交由前端 xterm.js 解码。
func (a *App) pumpSession(sess *shellSession) {
	sess.mu.Lock()
	sh := sess.shell
	sess.mu.Unlock()
	if sh == nil {
		return
	}

	chunks := make(chan []byte, 8)
	var pumps sync.WaitGroup

	pump := func(rd interface {
		Read([]byte) (int, error)
	}) {
		defer pumps.Done()
		buf := make([]byte, 32<<10)
		for {
			n, err := rd.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				select {
				case chunks <- cp:
				case <-sess.done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}

	pumps.Add(2)
	go pump(sh.Stdout())
	go pump(sh.Stderr())
	go func() {
		pumps.Wait()
		close(chunks)
	}()

	var pending []byte
	timer := time.NewTimer(termMaxDelay)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	flush := func() {
		if len(pending) == 0 {
			return
		}
		data := pending
		pending = nil
		sess.emit(sess.ctx, "term:data:"+sess.id, data)
		select {
		case <-sess.ackCh:
		case <-sess.done:
		case <-time.After(termAckTimeout):
			wruntime.LogWarning(sess.ctx, "terminal: 等待前端回执超时，放行继续读取")
		}
		// 等待期间若通道已被排空，重置计时器，避免下一次读立刻触发 flush。
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}

	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				flush()
				sess.terminate("会话已结束")
				return
			}
			pending = append(pending, chunk...)
			if len(pending) >= termMaxChunk {
				flush()
				continue
			}
			timer.Reset(termMaxDelay)
		case <-timer.C:
			flush()
		case <-sess.done:
			return
		}
	}
}

// terminate 通知前端"这条会话结束了"，并释放资源。reason 面向用户。
func (s *shellSession) terminate(reason string) {
	s.close()
	s.emit(s.ctx, "term:exit:"+s.id, reason)
}

// close 释放 shell 与连接，并唤醒所有等待者。
func (s *shellSession) close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.mu.Lock()
		sh := s.shell
		ft := s.sftp
		s.mu.Unlock()
		if ft != nil {
			ft.Close()
		}
		if sh != nil {
			sh.Close()
		}
		s.client.Close()
	})
}
