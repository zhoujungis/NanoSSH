package sshcore

import (
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"
)

// ShellOptions 描述一个交互式 shell 的请求参数。
type ShellOptions struct {
	// Term 覆盖 TERM 值；空则用服务器的 terminal 配置，再空则 xterm-256color。
	Term string
	// Rows 是初始行数（PTY 的高）。
	Rows int
	// Cols 是初始列数（PTY 的宽）。
	Cols int
	// Modes 是额外的终端模式。
	Modes ssh.TerminalModes
}

// Shell 是一个已启动 PTY 的交互式会话。
//
// ★ 参数顺序陷阱：本 API 一律按 (rows, cols) 命名——行在前、列在后。
// 底层 x/crypto 的 RequestPty(term, h, w) 与 WindowChange(h, w) 同样是"行在前、列在后"，
// 但 RFC 4254 的 pty-req / window-change 线上编码是"宽在前、高在后"。
// 这一层错位正是 vim / htop / less 全屏错乱的经典根因，改动时务必对照单测。
type Shell struct {
	sess   *ssh.Session
	stdin  io.WriteCloser
	stdout io.Reader
	stderr io.Reader

	rows, cols int
}

// StartShell 在已有连接上请求 PTY 并启动交互式 shell。
func (c *Client) StartShell(opts ShellOptions) (*Shell, error) {
	term := opts.Term
	if term == "" {
		term = c.server.Terminal
	}
	if term == "" {
		term = defaultTerminal
	}
	rows, cols := opts.Rows, opts.Cols
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}

	sess, err := c.ssh.NewSession()
	if err != nil {
		return nil, fmt.Errorf("sshcore: 新建会话失败: %w", err)
	}

	if err := sess.RequestPty(term, rows, cols, opts.Modes); err != nil {
		sess.Close()
		return nil, fmt.Errorf("sshcore: 请求 PTY（%dx%d）失败: %w", cols, rows, err)
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, fmt.Errorf("sshcore: 获取 stdin 失败: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, fmt.Errorf("sshcore: 获取 stdout 失败: %w", err)
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		sess.Close()
		return nil, fmt.Errorf("sshcore: 获取 stderr 失败: %w", err)
	}

	if err := sess.Shell(); err != nil {
		sess.Close()
		return nil, fmt.Errorf("sshcore: 启动 shell 失败: %w", err)
	}

	return &Shell{
		sess:   sess,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		rows:   rows,
		cols:   cols,
	}, nil
}

// Stdin 是写入远端键盘输入的通道。
func (s *Shell) Stdin() io.Writer { return s.stdin }

// Stdout 是远端标准输出。
//
// ★ 后端绝不做行切分、绝不做 UTF-8 解码：一个字符可能跨 chunk 边界，
// 中途 string(buf) 必然产生 U+FFFD。全程 []byte 直通，交由前端 xterm.js 解码。
func (s *Shell) Stdout() io.Reader { return s.stdout }

// Stderr 是远端标准错误。PTY 下它与 stdout 已合并，但仍需各自读干净以免阻塞。
//
// 注意：stdout / stderr 是两个独立 reader，跨流顺序无法保证。
// 上层应把两者写入同一个带缓冲通道，由单一消费者排序输出。
func (s *Shell) Stderr() io.Reader { return s.stderr }

// Size 返回当前 PTY 的行列数。
func (s *Shell) Size() (rows, cols int) { return s.rows, s.cols }

// Resize 变更远端 PTY 尺寸，参数按 (rows, cols)——行在前、列在后。
//
// ★ window-change 请求是"发后不管"：x/crypto 以 wantReply=false 发送，
// 因此本函数返回只代表字节已写入连接，不代表远端已应用。
// 频繁 resize（拖动窗口）时应在上层做去抖，避免堆积无用请求。
func (s *Shell) Resize(rows, cols int) error {
	if rows <= 0 || cols <= 0 {
		return fmt.Errorf("sshcore: 非法 PTY 尺寸 rows=%d cols=%d", rows, cols)
	}
	if err := s.sess.WindowChange(rows, cols); err != nil {
		return fmt.Errorf("sshcore: 变更窗口尺寸（%dx%d）失败: %w", cols, rows, err)
	}
	s.rows, s.cols = rows, cols
	return nil
}

// Wait 阻塞至远端 shell 结束。
func (s *Shell) Wait() error { return s.sess.Wait() }

// Close 关闭会话。
func (s *Shell) Close() error { return s.sess.Close() }

// ExecResult 是一次非交互命令的执行结果。
type ExecResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// RunCommand 在远端执行一条非交互命令并收集输出。
//
// ★ 只用于探针与批处理。后端做缓冲收集，因此不适用于大输出；
// 大输出场景必须走流式读取（见 Shell / SFTP）。
func (c *Client) RunCommand(cmd string) (*ExecResult, error) {
	sess, err := c.ssh.NewSession()
	if err != nil {
		return nil, fmt.Errorf("sshcore: 新建会话失败: %w", err)
	}
	defer sess.Close()

	res := &ExecResult{}
	sess.Stdout = &limitedBuffer{buf: &res.Stdout}
	sess.Stderr = &limitedBuffer{buf: &res.Stderr}

	err = sess.Run(cmd)
	if err == nil {
		return res, nil
	}

	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitStatus()
		return res, nil
	}
	return res, fmt.Errorf("sshcore: 执行命令失败: %w", err)
}

// limitedBuffer 限制单次命令收集的输出量，避免远端失控输出吃光内存。
type limitedBuffer struct {
	buf *[]byte
}

const maxCommandOutput = 8 << 20 // 8 MiB

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := maxCommandOutput - len(*b.buf)
	if remaining <= 0 {
		return len(p), nil // 静默丢弃，但不阻断远端
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	*b.buf = append(*b.buf, p...)
	return len(p), nil
}
