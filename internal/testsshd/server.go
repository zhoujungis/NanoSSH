// Package testsshd 是一个进程内的最小 SSH 服务端，仅用于测试与本地探针。
//
// 存在的理由：开发机上没有 Docker，WSL 里也没装 sshd，
// 而 NanoSSH 的核心（PTY 尺寸传递、字节流反压）必须端到端验证。
// 这个服务端零外部依赖、随进程启动，让 "连接 → shell → stty size" 闭环可重复执行。
//
// 它刻意不实现完整的 SSH 语义：只覆盖 NanoSSH 用到的那几条请求。
package testsshd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Server 是一个运行在环回地址上的最小 SSH 服务端。
type Server struct {
	// User / Password 是唯一被接受的凭据。
	User     string
	Password string

	ln   net.Listener
	conf *ssh.ServerConfig

	mu       sync.Mutex
	lastTerm string
	lastRows int
	lastCols int
	ptys     int
	sftps    int
	conns    int
	directs  int
	// keys 是允许登录的公钥集合。为空时只接受密码，便于区分两条认证路径。
	keys []ssh.PublicKey
	// lastAuth 记录最近一次成功的认证方式（password / publickey），供测试断言。
	lastAuth string
}

// New 启动服务端并监听 127.0.0.1 上的随机端口。
// 调用方用 Addr() 取回实际地址，用 Close() 结束。
func New(user, password string) (*Server, error) {
	return NewAt("127.0.0.1:0", user, password)
}

// NewAt 在指定地址上启动（addr 形如 127.0.0.1:0，端口为 0 时由系统分配）。
func NewAt(addr, user, password string) (*Server, error) {
	s := &Server{User: user, Password: password}
	if err := s.rotateLocked(); err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s.ln = ln
	go s.acceptLoop()
	return s, nil
}

// RotateHostKey 更换主机密钥，用于模拟"同一主机换了密钥"这一指纹变化场景。
// 地址不变，因此 known_hosts 里的旧记录会与新密钥冲突。
func (s *Server) RotateHostKey() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rotateLocked()
}

// AuthorizeKey 追加一把允许登录的公钥。
func (s *Server) AuthorizeKey(key ssh.PublicKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
}

// rotateLocked 重建 ServerConfig（ssh.ServerConfig 无法移除已添加的主机密钥）。
func (s *Server) rotateLocked() error {
	hostKey, err := ephemeralHostKey()
	if err != nil {
		return err
	}
	conf := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if meta.User() == s.User && string(pass) == s.Password {
				s.mu.Lock()
				s.lastAuth = "password"
				s.mu.Unlock()
				return nil, nil
			}
			return nil, fmt.Errorf("testsshd: 认证失败")
		},
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != s.User {
				return nil, fmt.Errorf("testsshd: 未知用户 %s", meta.User())
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, k := range s.keys {
				if bytes.Equal(k.Marshal(), key.Marshal()) {
					s.lastAuth = "publickey"
					return nil, nil
				}
			}
			return nil, fmt.Errorf("testsshd: 公钥未被授权")
		},
	}
	conf.AddHostKey(hostKey)
	s.conf = conf
	return nil
}

func (s *Server) config() *ssh.ServerConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conf
}

// Addr 返回 host:port 形式的实际监听地址。
func (s *Server) Addr() string { return s.ln.Addr().String() }

// HostPort 分别返回主机与端口。
func (s *Server) HostPort() (string, int) {
	host, portStr, _ := net.SplitHostPort(s.ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return host, port
}

// LastPTY 返回最近一次 pty-req 解析出的 TERM 与行列数。
func (s *Server) LastPTY() (term string, rows, cols int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTerm, s.lastRows, s.lastCols
}

// PTYCount 返回收到的 pty-req 次数。
func (s *Server) PTYCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ptys
}

// SFTPCount 返回成功启动的 sftp 子系统次数，用于验证"复用同一连接开通道"。
func (s *Server) SFTPCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sftps
}

// ConnCount 返回完成握手的连接数，用于验证 SFTP 复用了同一条连接而非重新拨号。
func (s *Server) ConnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns
}

// LastAuth 返回最近一次成功的认证方式（password / publickey），空表示尚无成功认证。
func (s *Server) LastAuth() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAuth
}

// Close 停止监听。
func (s *Server) Close() error { return s.ln.Close() }

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serveConn(conn)
	}
}

func (s *Server) serveConn(nConn net.Conn) {
	defer nConn.Close()

	sshConn, chans, reqs, err := ssh.NewServerConn(nConn, s.config())
	if err != nil {
		return
	}
	s.mu.Lock()
	s.conns++
	s.mu.Unlock()
	defer sshConn.Close()

	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		switch newChan.ChannelType() {
		case "session":
			ch, chReqs, err := newChan.Accept()
			if err != nil {
				continue
			}
			go s.serveSession(ch, chReqs)

		case "direct-tcpip":
			// 本地端口转发（-L）走这里；目标是真实 TCP 服务，
			// 转发测试必须覆盖"字节真的到了目标服务"这条路径。
			// 拨号可能卡住，放进协程，不挡后面通道的建立。
			go s.serveDirect(newChan)

		default:
			newChan.Reject(ssh.UnknownChannelType, "不支持的通道类型")
		}
	}
}

// DirectCount 返回成功建立的 direct-tcpip 通道数，用于验证转发复用同一条连接。
func (s *Server) DirectCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.directs
}

// serveDirect 处理 direct-tcpip：解析目标地址、拨号、双向搬运字节。
func (s *Server) serveDirect(newChan ssh.NewChannel) {
	var p struct {
		DestAddr   string
		DestPort   uint32
		OriginAddr string
		OriginPort uint32
	}
	if err := ssh.Unmarshal(newChan.ExtraData(), &p); err != nil {
		newChan.Reject(ssh.ConnectionFailed, "无法解析目标地址")
		return
	}
	target := net.JoinHostPort(p.DestAddr, strconv.FormatUint(uint64(p.DestPort), 10))
	conn, err := net.Dial("tcp", target)
	if err != nil {
		newChan.Reject(ssh.ConnectionFailed, "连接目标失败: "+err.Error())
		return
	}
	ch, reqs, err := newChan.Accept()
	if err != nil {
		conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)

	s.mu.Lock()
	s.directs++
	s.mu.Unlock()

	go func() {
		defer ch.Close()
		defer conn.Close()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			io.Copy(conn, ch)
		}()
		go func() {
			defer wg.Done()
			io.Copy(ch, conn)
		}()
		wg.Wait()
	}()
}

func (s *Server) serveSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()

	for req := range reqs {
		switch req.Type {
		case "pty-req":
			// RFC 4254 §6.2：string term, uint32 width(chars), uint32 height(rows), ...
			// 线上是"宽在前、高在后"，这里如实解析，正是为了钉死这一顺序。
			var p struct {
				Term  string
				Cols  uint32
				Rows  uint32
				Wpx   uint32
				Hpx   uint32
				Modes string
			}
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				req.Reply(false, nil)
				continue
			}
			s.mu.Lock()
			s.lastTerm, s.lastRows, s.lastCols = p.Term, int(p.Rows), int(p.Cols)
			s.ptys++
			s.mu.Unlock()
			req.Reply(true, nil)

		case "window-change":
			// RFC 4254 §6.7：uint32 width(chars), uint32 height(rows), uint32 wpx, uint32 hpx
			var p struct {
				Cols uint32
				Rows uint32
				Wpx  uint32
				Hpx  uint32
			}
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				req.Reply(false, nil)
				continue
			}
			s.mu.Lock()
			s.lastRows, s.lastCols = int(p.Rows), int(p.Cols)
			s.mu.Unlock()
			req.Reply(true, nil)

		case "env":
			req.Reply(true, nil)

		case "shell":
			req.Reply(true, nil)
			// 不 return：真实的 sshd 在 shell 运行期间仍会继续处理
			// window-change 等请求，这里必须同样并发服务，
			// 否则 resize 会被无声吞掉。
			go func() {
				defer ch.Close()
				s.runREPL(ch)
				// 真实 sshd 在交互式 shell 退出后同样会回 exit-status，
				// 缺了它客户端会报 "exited without exit status"。
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			}()

		case "exec":
			var p struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			go func(cmd string) {
				defer ch.Close()
				s.runExec(ch, cmd)
			}(p.Command)

		case "subsystem":
			// RFC 4254 §6.7：string subsystem 名。只认 sftp。
			var p struct{ Name string }
			if err := ssh.Unmarshal(req.Payload, &p); err != nil || p.Name != "sftp" {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			// 余下通道请求（exit-status 等）不再关心，但必须读干净，
			// 否则 x/crypto 的请求队列会顶满并阻塞对端。
			go ssh.DiscardRequests(reqs)
			s.serveSFTP(ch)
			return

		default:
			req.Reply(false, nil)
		}
	}
}

// serveSFTP 在通道上跑一个真实的 SFTP 服务端（可见范围是进程可见的真实文件系统）。
//
// 用 pkg/sftp 的服务端实现而不是自造协议：若测试端与客户端共用同一套自研代码，
// 协议 bug 会被两边"互相迁就"，测试就白测了。
func (s *Server) serveSFTP(ch ssh.Channel) {
	s.mu.Lock()
	s.sftps++
	s.mu.Unlock()

	srv, err := sftp.NewServer(ch)
	if err != nil {
		return
	}
	_ = srv.Serve()
	_ = srv.Close()
}

// runREPL 是一个极小的行式 shell，用来验证字节流与 PTY 尺寸。
// 支持：stty size / echo X / exit / 其他则回显确认。
func (s *Server) runREPL(ch ssh.Channel) {
	io.WriteString(ch, "nanossh-test-sshd\r\n")

	buf := make([]byte, 0, 256)
	tmp := make([]byte, 512)
	for {
		n, err := ch.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for {
				idx := indexByte(buf, '\n')
				if idx < 0 {
					break
				}
				line := strings.TrimRight(string(buf[:idx]), "\r")
				buf = buf[idx+1:]
				if !s.handleLine(ch, line) {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// handleLine 处理一行输入，返回 false 表示应结束会话。
func (s *Server) handleLine(ch ssh.Channel, line string) bool {
	cmd := strings.TrimSpace(line)
	switch {
	case cmd == "":
		// 空行不回显
	case cmd == "exit" || cmd == "logout" || cmd == "quit":
		io.WriteString(ch, "bye\r\n")
		return false
	case cmd == "stty size":
		s.mu.Lock()
		rows, cols := s.lastRows, s.lastCols
		s.mu.Unlock()
		fmt.Fprintf(ch, "%d %d\r\n", rows, cols)
	case strings.HasPrefix(cmd, "echo "):
		io.WriteString(ch, strings.TrimPrefix(cmd, "echo ")+"\r\n")
	default:
		fmt.Fprintf(ch, "ok: %s\r\n", cmd)
	}
	return true
}

// runExec 执行一条非交互命令。
func (s *Server) runExec(ch ssh.Channel, command string) {
	cmd := strings.TrimSpace(command)

	// 退出码 0 的正常路径。
	switch {
	case cmd == "stty size":
		s.mu.Lock()
		rows, cols := s.lastRows, s.lastCols
		s.mu.Unlock()
		fmt.Fprintf(ch, "%d %d\n", rows, cols)

	case strings.HasPrefix(cmd, "echo "):
		io.WriteString(ch, strings.TrimPrefix(cmd, "echo ")+"\n")

	case strings.HasPrefix(cmd, "flood "):
		// flood <MiB>：输出大块数据，用于验证前端不卡死、后端内存不涨。
		mib, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(cmd, "flood ")))
		if err != nil || mib <= 0 {
			fmt.Fprintln(ch.Stderr(), "flood: 需要一个正整数 MiB")
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
			return
		}
		block := make([]byte, 64<<10)
		for i := range block {
			block[i] = 'a'
		}
		for written := 0; written < mib<<20; {
			n := len(block)
			if mib<<20-written < n {
				n = mib<<20 - written
			}
			if _, err := ch.Write(block[:n]); err != nil {
				return
			}
			written += n
		}

	case strings.HasPrefix(cmd, "exit "):
		code, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(cmd, "exit ")))
		ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
		return

	default:
		fmt.Fprintf(ch, "ok: %s\n", cmd)
	}

	ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

// ephemeralHostKey 每次启动生成一把临时 ed25519 主机密钥，避免测试依赖固定文件。
func ephemeralHostKey() (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}
