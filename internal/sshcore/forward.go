package sshcore

import (
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/ssh"
)

// ForwardStats 是一条本地端口转发的运行时计数。
type ForwardStats struct {
	// Active 是当前正在搬运字节的连接数。
	Active int64 `json:"active"`
	// Total 是累计接受过的连接数。
	Total int64 `json:"total"`
	// In 是远端流向本地的字节数，Out 是本地流向远端的字节数。
	In  int64 `json:"in"`
	Out int64 `json:"out"`
}

// Forward 是一条本地端口转发（ssh -L 语义）：
// 在本地监听一个端口，每个进入的 TCP 连接都在 SSH 连接上打开一个
// direct-tcpip 通道，通向 remoteAddr。
//
// ★ 与 SFTP 同理，它复用 Client 的同一条连接：不拨号、不握手、不认证，
// 转发流量只是这条连接上的另外几个通道，终端与文件面板照常并存。
type Forward struct {
	ssh        *ssh.Client
	remoteAddr string
	ln         net.Listener

	mu    sync.Mutex
	pairs map[*splicePair]struct{}

	active atomic.Int64
	total  atomic.Int64
	in     atomic.Int64
	out    atomic.Int64

	done      chan struct{}
	closeOnce sync.Once
}

// splicePair 是一对被打通的连接，关掉任一端都会让两个方向的搬运同时收工。
type splicePair struct {
	local  net.Conn
	remote net.Conn
	once   sync.Once
}

func (p *splicePair) close() {
	p.once.Do(func() {
		p.local.Close()
		p.remote.Close()
	})
}

// Forward 在 localAddr 上监听，并把进入的连接经 direct-tcpip 转发到 remoteAddr。
// localAddr 的主机部分建议用 127.0.0.1——默认不该把服务暴露到局域网。
func (c *Client) Forward(localAddr, remoteAddr string) (*Forward, error) {
	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		return nil, fmt.Errorf("sshcore: 监听 %s 失败: %w", localAddr, err)
	}
	f := &Forward{
		ssh:        c.ssh,
		remoteAddr: remoteAddr,
		ln:         ln,
		pairs:      make(map[*splicePair]struct{}),
		done:       make(chan struct{}),
	}
	go f.acceptLoop()
	return f, nil
}

// Addr 返回实际绑定的监听地址（端口可能由系统分配）。
func (f *Forward) Addr() string { return f.ln.Addr().String() }

// Done 在转发被关闭后关闭，供上层感知"这条转发已经收工"。
func (f *Forward) Done() <-chan struct{} { return f.done }

// Stats 返回当前计数快照。
func (f *Forward) Stats() ForwardStats {
	return ForwardStats{
		Active: f.active.Load(),
		Total:  f.total.Load(),
		In:     f.in.Load(),
		Out:    f.out.Load(),
	}
}

// Close 停止监听并掐断所有在途连接，可重复调用。
func (f *Forward) Close() error {
	f.closeOnce.Do(func() {
		close(f.done)
		f.ln.Close()
		f.mu.Lock()
		pairs := make([]*splicePair, 0, len(f.pairs))
		for p := range f.pairs {
			pairs = append(pairs, p)
		}
		f.mu.Unlock()
		for _, p := range pairs {
			p.close()
		}
	})
	return nil
}

func (f *Forward) acceptLoop() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return // 监听已关闭
		}
		go f.handle(conn)
	}
}

// handle 为一个本地连接开一条 direct-tcpip 通道并双向搬运字节。
func (f *Forward) handle(local net.Conn) {
	remote, err := f.ssh.Dial("tcp", f.remoteAddr)
	if err != nil {
		// 目标不可达：掐断本地连接，客户端看到的语义与 OpenSSH 一致。
		local.Close()
		return
	}

	p := &splicePair{local: local, remote: remote}
	f.mu.Lock()
	select {
	case <-f.done:
		f.mu.Unlock()
		p.close()
		return
	default:
	}
	f.pairs[p] = struct{}{}
	f.mu.Unlock()

	f.total.Add(1)
	f.active.Add(1)
	defer func() {
		f.active.Add(-1)
		f.mu.Lock()
		delete(f.pairs, p)
		f.mu.Unlock()
		p.close()
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		splice(remote, local, func(n int64) { f.out.Add(n) })
		// 写完就关写端，让远端看到 EOF，长连接才能优雅收尾。
		remote.Close()
	}()
	go func() {
		defer wg.Done()
		splice(local, remote, func(n int64) { f.in.Add(n) })
		local.Close()
	}()
	wg.Wait()
}

// splice 把 src 搬到 dst，每块把字节数累加进计数器。
//
// 与 sftp.go 的 shuttle 同一套思路：按块搬运、不缓存整段、
// 一端出错就停手（defer 里统一关连接）。
func splice(dst io.Writer, src io.Reader, add func(int64)) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
			add(int64(n))
		}
		if err != nil {
			return
		}
	}
}
