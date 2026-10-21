// Command sshprobe 是 NanoSSH 的无 UI 端到端探针。
//
// 它存在的意义：所有内核能力都必须能在不打开窗口的前提下被验证，
// 否则"UI 能点通"会掩盖底层协议错误（典型如 PTY 行列顺序、UTF-8 截断）。
//
// 用法：
//
//	sshprobe exec  -host H [-port 22] -user U -password P -cmd "uname -a"
//	sshprobe shell -host H [-port 22] -user U -password P [-resize 30x100]
//	sshprobe serve [-user nano] [-password nano]   # 起一个进程内测试 sshd
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"flag"

	"nanossh/internal/sshcore"
	"nanossh/internal/store"
	"nanossh/internal/testsshd"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "exec":
		runExec(os.Args[2:])
	case "shell":
		runShell(os.Args[2:])
	case "serve":
		runServe(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `NanoSSH 探针

子命令：
  exec   连接并执行一条命令，退出码透传
  shell  连接并打开交互式 PTY（stdin/stdout 直通）
  serve  在本机起一个最小测试 sshd，供上面两条命令练手

常用参数：-host -port -user -password -knownhosts -trust -term -rows -cols -timeout
`)
}

// connFlags 是三条子命令共用的连接参数。
type connFlags struct {
	host       string
	port       int
	user       string
	password   string
	knownHosts string
	term       string
	rows       int
	cols       int
	timeout    time.Duration
	// trust 为 true 时把首次见到的/变化后的主机密钥写入 known_hosts；
	// 否则首次仅本次信任、指纹变化一律阻断。
	trust bool
}

func (c *connFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.host, "host", "", "目标主机（必填）")
	fs.IntVar(&c.port, "port", 22, "目标端口")
	fs.StringVar(&c.user, "user", "", "登录用户名（必填）")
	fs.StringVar(&c.password, "password", "", "登录密码")
	fs.StringVar(&c.knownHosts, "knownhosts", "", "known_hosts 路径，默认 <数据目录>/known_hosts")
	fs.StringVar(&c.term, "term", "xterm-256color", "TERM 值")
	fs.IntVar(&c.rows, "rows", 24, "初始 PTY 行数")
	fs.IntVar(&c.cols, "cols", 80, "初始 PTY 列数")
	fs.DurationVar(&c.timeout, "timeout", 15*time.Second, "建连与握手超时")
	fs.BoolVar(&c.trust, "trust", false, "自动信任并保存主机密钥（仅用于测试）")
}

func (c *connFlags) connect() (*sshcore.Client, error) {
	if c.host == "" || c.user == "" {
		return nil, fmt.Errorf("-host 与 -user 必填")
	}

	knownHostsPath := c.knownHosts
	if knownHostsPath == "" {
		p, err := store.Resolve()
		if err != nil {
			return nil, err
		}
		if err := p.Ensure(); err != nil {
			return nil, err
		}
		knownHostsPath = p.KnownHosts
	}

	prompt := func(info sshcore.HostKeyInfo, change *sshcore.HostKeyChange) (sshcore.HostKeyDecision, error) {
		if change != nil {
			fmt.Fprintf(os.Stderr, "[探针] 主机密钥发生变化：%s:%d\n", info.Host, info.Port)
			for _, old := range change.OldFingerprints {
				fmt.Fprintf(os.Stderr, "       旧: %s\n", old)
			}
			fmt.Fprintf(os.Stderr, "       新: %s (%s)\n", info.Fingerprint, info.KeyType)
			if !c.trust {
				return sshcore.HostKeyReject, nil
			}
			return sshcore.HostKeyAcceptAndSave, nil
		}
		fmt.Fprintf(os.Stderr, "[探针] 首次连接 %s:%d，主机密钥 %s %s\n",
			info.Host, info.Port, info.KeyType, info.Fingerprint)
		if c.trust {
			return sshcore.HostKeyAcceptAndSave, nil
		}
		return sshcore.HostKeyAcceptOnce, nil
	}

	srv := sshcore.Server{
		Name:     "probe",
		Host:     c.host,
		Port:     c.port,
		User:     c.user,
		Auth:     sshcore.AuthPassword,
		Terminal: c.term,
	}

	return sshcore.Connect(context.Background(), sshcore.ConnectOptions{
		Server:         srv,
		Auth:           sshcore.AuthInput{Password: c.password},
		KnownHostsPath: knownHostsPath,
		HostKeyPrompt:  prompt,
		Timeout:        c.timeout,
	})
}

func runExec(args []string) {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	var cf connFlags
	cf.bind(fs)
	var cmd string
	fs.StringVar(&cmd, "cmd", "", "要执行的命令")
	_ = fs.Parse(args)

	if cmd == "" {
		cmd = strings.Join(fs.Args(), " ")
	}
	if cmd == "" {
		fmt.Fprintln(os.Stderr, "exec 需要 -cmd 或位置参数给出命令")
		os.Exit(2)
	}

	cli, err := cf.connect()
	if err != nil {
		fmt.Fprintf(os.Stderr, "连接失败: %v\n", err)
		os.Exit(1)
	}
	defer cli.Close()

	res, err := cli.RunCommand(cmd)
	os.Stdout.Write(res.Stdout)
	os.Stderr.Write(res.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "执行失败: %v\n", err)
		os.Exit(1)
	}
	os.Exit(res.ExitCode)
}

func runShell(args []string) {
	fs := flag.NewFlagSet("shell", flag.ExitOnError)
	var cf connFlags
	cf.bind(fs)
	var resize string
	fs.StringVar(&resize, "resize", "", "启动后立刻发送一次 window-change，形如 30x100")
	_ = fs.Parse(args)

	cli, err := cf.connect()
	if err != nil {
		fmt.Fprintf(os.Stderr, "连接失败: %v\n", err)
		os.Exit(1)
	}
	defer cli.Close()

	sh, err := cli.StartShell(sshcore.ShellOptions{Term: cf.term, Rows: cf.rows, Cols: cf.cols})
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开 shell 失败: %v\n", err)
		os.Exit(1)
	}
	defer sh.Close()

	if resize != "" {
		rows, cols, err := parseSize(resize)
		if err != nil {
			fmt.Fprintf(os.Stderr, "-resize 解析失败: %v\n", err)
			os.Exit(2)
		}
		if err := sh.Resize(rows, cols); err != nil {
			fmt.Fprintf(os.Stderr, "resize 失败: %v\n", err)
			os.Exit(1)
		}
		curRows, curCols := sh.Size()
		fmt.Fprintf(os.Stderr, "[探针] 已发送 window-change: rows=%d cols=%d\n", curRows, curCols)
	}

	go func() {
		_, _ = io.Copy(sh.Stdin(), os.Stdin)
	}()
	go func() {
		_, _ = io.Copy(os.Stderr, sh.Stderr())
	}()
	_, _ = io.Copy(os.Stdout, sh.Stdout())

	if err := sh.Wait(); err != nil {
		fmt.Fprintf(os.Stderr, "\n[探针] 会话结束: %v\n", err)
		os.Exit(1)
	}
}

// parseSize 解析 "行x列"，如 30x100。
func parseSize(s string) (rows, cols int, err error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(s)), "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("需要 ROWSxCOLS 形式，收到 %q", s)
	}
	rows, err = strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, err
	}
	cols, err = strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, err
	}
	if rows <= 0 || cols <= 0 {
		return 0, 0, fmt.Errorf("行列必须为正数")
	}
	return rows, cols, nil
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	user := fs.String("user", "nano", "测试账号用户名")
	password := fs.String("password", "nano", "测试账号密码")
	addr := fs.String("addr", "127.0.0.1:0", "监听地址，端口为 0 时由系统分配")
	_ = fs.Parse(args)

	srv, err := testsshd.NewAt(*addr, *user, *password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "启动测试 sshd 失败: %v\n", err)
		os.Exit(1)
	}
	defer srv.Close()

	host, port := srv.HostPort()
	fmt.Printf("测试 sshd 已启动：%s:%d  user=%s password=%s\n", host, port, *user, *password)
	fmt.Printf("试一下：  sshprobe exec -host %s -port %d -user %s -password %s -trust -cmd \"stty size\"\n",
		host, port, *user, *password)
	fmt.Println("按 Ctrl-C 结束。")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	fmt.Println("\n已停止。")
}
