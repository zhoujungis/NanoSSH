package sshcore

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	defaultDialTimeout = 15 * time.Second
	defaultKeepAlive   = 30 * time.Second
	defaultTerminal    = "xterm-256color"
	defaultPort        = 22
)

// ConnectOptions 是一次连接的完整输入。
type ConnectOptions struct {
	Server         Server
	Auth           AuthInput
	KnownHostsPath string
	// HostKeyPrompt 为 nil 时，未知主机与指纹变化一律阻断。
	HostKeyPrompt HostKeyPrompt
	// Timeout 覆盖建连与握手超时，缺省 15s。
	Timeout time.Duration
	// KeepAliveInterval 覆盖心跳间隔，缺省 30s；<0 关闭心跳。
	// 心跳是必需的：NAT/防火墙会静默掐断空闲连接，表现为"卡住但不报错"，
	// 而 ssh.ClientConfig.Timeout 只管建连阶段。
	KeepAliveInterval time.Duration
}

// Client 是一条已建立并完成认证的 SSH 连接。
type Client struct {
	server Server
	ssh    *ssh.Client

	// authCloser 是认证阶段占用的外部资源（目前只有 ssh-agent 连接），
	// 随连接一起释放，避免每次连接都留下一个打开的命名管道句柄。
	authCloser io.Closer

	keepAliveInterval time.Duration
	stopOnce          sync.Once
	stop              chan struct{}
	closeOnce         sync.Once
	closeErr          error
}

// Server 返回该连接对应的服务器配置。
func (c *Client) Server() Server { return c.server }

// SSH 暴露底层 *ssh.Client，供 sftp / forward 使用。
func (c *Client) SSH() *ssh.Client { return c.ssh }

// Connect 建立连接：TCP 拨号 → 握手（含主机密钥校验）→ 认证 → 启动心跳。
func Connect(ctx context.Context, opts ConnectOptions) (*Client, error) {
	s := opts.Server
	if s.Port == 0 {
		s.Port = defaultPort
	}
	if s.Terminal == "" {
		s.Terminal = defaultTerminal
	}
	if s.Host == "" {
		return nil, fmt.Errorf("sshcore: 服务器 %s 缺少主机地址", s.Name)
	}
	if s.User == "" {
		return nil, fmt.Errorf("sshcore: 服务器 %s 缺少用户名", s.Name)
	}

	hostKey, err := NewHostKeyCallback(opts.KnownHostsPath, opts.HostKeyPrompt)
	if err != nil {
		return nil, err
	}
	methods, authCloser, err := authMethods(s, opts.Auth)
	if err != nil {
		return nil, err
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultDialTimeout
	}

	cfg := &ssh.ClientConfig{
		User:            s.User,
		Auth:            methods,
		HostKeyCallback: hostKey,
		Timeout:         timeout,
	}

	addr := s.Addr()
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("sshcore: 连接 %s 失败: %w", addr, err)
	}

	// 握手耗时不可控（DNS 反查、算法协商、主机密钥交互都可能卡住），
	// 用 ctx 兜底：一旦取消就关闭底层连接，让握手立刻返回。
	handshakeDone := make(chan struct{})
	defer close(handshakeDone)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-handshakeDone:
		}
	}()

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		if authCloser != nil {
			authCloser.Close()
		}
		return nil, fmt.Errorf("sshcore: 与 %s 握手失败: %w", addr, err)
	}

	c := &Client{
		server:            s,
		ssh:               ssh.NewClient(sshConn, chans, reqs),
		authCloser:        authCloser,
		keepAliveInterval: opts.KeepAliveInterval,
		stop:              make(chan struct{}),
	}
	if opts.KeepAliveInterval == 0 {
		c.keepAliveInterval = defaultKeepAlive
	}
	if c.keepAliveInterval > 0 {
		go c.keepAliveLoop()
	}
	return c, nil
}

// keepAliveLoop 周期性发送 keepalive@openssh.com；一旦失败即认定为连接已死。
func (c *Client) keepAliveLoop() {
	ticker := time.NewTicker(c.keepAliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			if _, _, err := c.ssh.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				c.Close()
				return
			}
		}
	}
}

// Closed 返回一个在连接关闭时被关闭的通道，供上层感知断开。
func (c *Client) Closed() <-chan struct{} { return c.stop }

// Close 关闭连接，可重复调用。
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.stopOnce.Do(func() { close(c.stop) })
		c.closeErr = c.ssh.Close()
		if c.authCloser != nil {
			c.authCloser.Close()
		}
	})
	return c.closeErr
}
