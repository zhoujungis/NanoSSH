package sshcore_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nanossh/internal/sshcore"
	"nanossh/internal/testsshd"
)

const (
	testUser = "nano"
	testPass = "nano"
)

// startServer 起一个进程内测试 sshd。
func startServer(t *testing.T) *testsshd.Server {
	t.Helper()
	s, err := testsshd.New(testUser, testPass)
	if err != nil {
		t.Fatalf("启动测试 sshd 失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// connectWith 用给定的信任决策连接测试服务器。
func connectWith(t *testing.T, s *testsshd.Server, knownHostsPath string, prompt sshcore.HostKeyPrompt) (*sshcore.Client, error) {
	t.Helper()
	host, port := s.HostPort()
	return sshcore.Connect(context.Background(), sshcore.ConnectOptions{
		Server: sshcore.Server{
			Name: "探针靶机",
			Host: host,
			Port: port,
			User: testUser,
			Auth: sshcore.AuthPassword,
		},
		Auth:           sshcore.AuthInput{Password: testPass},
		KnownHostsPath: knownHostsPath,
		HostKeyPrompt:  prompt,
		Timeout:        5 * time.Second,
	})
}

// connectOnce 建立一条"主机密钥仅本次信任"的连接。
func connectOnce(t *testing.T, s *testsshd.Server) *sshcore.Client {
	t.Helper()
	cli, err := connectWith(t, s, filepath.Join(t.TempDir(), "known_hosts"), func(sshcore.HostKeyInfo, *sshcore.HostKeyChange) (sshcore.HostKeyDecision, error) {
		return sshcore.HostKeyAcceptOnce, nil
	})
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	t.Cleanup(func() { cli.Close() })
	return cli
}

// TestStartShellPTYParamOrder 钉死 rows/cols 的传递顺序——本项目最容易错、后果最明显的一点。
//
// x/crypto 的 RequestPty(term, h, w) 语义是"行在前、列在后"，
// 而 RFC 4254 的 pty-req 线上编码是"宽在前、高在后"。
// 测试服务端按 RFC 原样解析，因此一旦封装层把两个值传反，
// 这里会读到 rows=100 / cols=30，vim、htop、less 的全屏就会错行。
func TestStartShellPTYParamOrder(t *testing.T) {
	s := startServer(t)
	cli := connectOnce(t, s)

	sh, err := cli.StartShell(sshcore.ShellOptions{Term: "xterm-256color", Rows: 30, Cols: 100})
	if err != nil {
		t.Fatalf("StartShell 失败: %v", err)
	}
	defer sh.Close()

	term, rows, cols := s.LastPTY()
	if term != "xterm-256color" || rows != 30 || cols != 100 {
		t.Fatalf("pty-req 参数错位：服务端解析到 term=%q rows=%d cols=%d，期望 term=\"xterm-256color\" rows=30 cols=100\n"+
			"若 rows/cols 恰好互换，说明传给 RequestPty 的顺序反了", term, rows, cols)
	}
}

// TestShellResize 验证 window-change 同样使用"行在前、列在后"。
// 注意 window-change 是 wantReply=false 的"发后不管"请求，
// 客户端返回不代表服务端已应用，因此这里必须轮询而非立即断言。
func TestShellResize(t *testing.T) {
	s := startServer(t)
	cli := connectOnce(t, s)

	sh, err := cli.StartShell(sshcore.ShellOptions{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("StartShell 失败: %v", err)
	}
	defer sh.Close()

	if err := sh.Resize(41, 123); err != nil {
		t.Fatalf("Resize 失败: %v", err)
	}

	waitForPTY(t, s, 41, 123)

	if r, c := sh.Size(); r != 41 || c != 123 {
		t.Fatalf("本地尺寸未同步：rows=%d cols=%d，期望 41/123", r, c)
	}
}

// waitForPTY 轮询等待服务端解析到期望尺寸。
func waitForPTY(t *testing.T, s *testsshd.Server, wantRows, wantCols int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var gotRows, gotCols int
	for time.Now().Before(deadline) {
		_, gotRows, gotCols = s.LastPTY()
		if gotRows == wantRows && gotCols == wantCols {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("window-change 未生效：服务端 rows=%d cols=%d，期望 rows=%d cols=%d\n"+
		"若 rows/cols 恰好互换，说明传给 WindowChange 的顺序反了", gotRows, gotCols, wantRows, wantCols)
}

// TestShellSttySizeRoundTrip 端到端验证"设尺寸 → 远端 stty size 读回"闭环。
func TestShellSttySizeRoundTrip(t *testing.T) {
	s := startServer(t)
	cli := connectOnce(t, s)

	sh, err := cli.StartShell(sshcore.ShellOptions{Rows: 33, Cols: 111})
	if err != nil {
		t.Fatalf("StartShell 失败: %v", err)
	}
	defer sh.Close()

	if _, err := io.WriteString(sh.Stdin(), "stty size\n"); err != nil {
		t.Fatalf("写入 stdin 失败: %v", err)
	}

	line, err := waitForLine(sh.Stdout(), "33 111", 5*time.Second)
	if err != nil {
		t.Fatalf("读取 stty size 输出失败: %v", err)
	}
	if line != "33 111" {
		t.Fatalf("stty size 返回 %q，期望 \"33 111\"", line)
	}
}

// TestShellUTF8Passthrough 验证后端全程 []byte 直通、不做 UTF-8 解码。
// 一旦在 Go 侧调用 string(buf) 切分，多字节字符就会变成 U+FFFD。
func TestShellUTF8Passthrough(t *testing.T) {
	s := startServer(t)
	cli := connectOnce(t, s)

	sh, err := cli.StartShell(sshcore.ShellOptions{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("StartShell 失败: %v", err)
	}
	defer sh.Close()

	const want = "你好，世界 한국어 日本語 emoji🙂"
	if _, err := io.WriteString(sh.Stdin(), "echo "+want+"\n"); err != nil {
		t.Fatalf("写入 stdin 失败: %v", err)
	}

	line, err := waitForLine(sh.Stdout(), "世界", 5*time.Second)
	if err != nil {
		t.Fatalf("读取回显失败: %v", err)
	}
	if line != want {
		t.Fatalf("回显内容被破坏：%q，期望 %q", line, want)
	}
	if strings.ContainsRune(line, '\uFFFD') {
		t.Fatalf("回显中出现 U+FFFD，说明中途做了 UTF-8 解码或截断：%q", line)
	}
}

// TestRunCommand 验证非交互执行与退出码透传。
func TestRunCommand(t *testing.T) {
	s := startServer(t)
	cli := connectOnce(t, s)

	res, err := cli.RunCommand("echo hello")
	if err != nil {
		t.Fatalf("RunCommand 失败: %v", err)
	}
	if got := string(res.Stdout); got != "hello\n" {
		t.Fatalf("stdout = %q，期望 \"hello\\n\"", got)
	}
	if res.ExitCode != 0 {
		t.Fatalf("退出码 = %d，期望 0", res.ExitCode)
	}

	res, err = cli.RunCommand("exit 3")
	if err != nil {
		t.Fatalf("RunCommand(exit 3) 失败: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("退出码 = %d，期望 3", res.ExitCode)
	}
}

// TestRunCommandLargeOutput 验证大块输出（8 MiB）能被完整、连续地读出。
// P1 验收里"cat 20MB 文件前端不卡死"就是这条的放大版。
func TestRunCommandLargeOutput(t *testing.T) {
	const mib = 8
	s := startServer(t)
	cli := connectOnce(t, s)

	res, err := cli.RunCommand(fmt.Sprintf("flood %d", mib))
	if err != nil {
		t.Fatalf("RunCommand(flood) 失败: %v", err)
	}
	if len(res.Stdout) != mib<<20 {
		t.Fatalf("收到 %d 字节，期望 %d", len(res.Stdout), mib<<20)
	}
	if res.ExitCode != 0 {
		t.Fatalf("退出码 = %d，期望 0", res.ExitCode)
	}
}

// waitForLine 从 r 上逐行读取，直到出现包含 want 的行或超时。
// 注意用 bufio.Scanner：服务端输出以 \r\n 结尾，Scanner 会剥掉 \r。
func waitForLine(r io.Reader, want string, timeout time.Duration) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			if strings.Contains(line, want) {
				ch <- result{line: line}
				return
			}
		}
		if err := sc.Err(); err != nil {
			ch <- result{err: err}
			return
		}
		ch <- result{err: fmt.Errorf("在到达 EOF 前未出现 %q", want)}
	}()

	select {
	case res := <-ch:
		return res.line, res.err
	case <-time.After(timeout):
		return "", fmt.Errorf("等待 %q 超时（%s）", want, timeout)
	}
}
