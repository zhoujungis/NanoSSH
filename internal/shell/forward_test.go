package shell

import (
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startEcho 在环回地址上起一个回显服务，作为转发的目标端。
//
// 目标必须是真实的 TCP 服务：转发的价值就在"字节真的到了目标服务"，
// 假 echo 会把 direct-tcpip 这条最关键的路径漏测掉。
func startEcho(t *testing.T) (string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动回显服务失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()

	host, port := ln.Addr().(*net.TCPAddr).IP.String(), ln.Addr().(*net.TCPAddr).Port
	return host, port
}

// freePort 找一个当前空闲的本地端口。选择与占用之间有微小竞态，
// 测试里可接受——真实用户遇到端口冲突走的是另一条报错路径。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("探测空闲端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// dialEcho 连上转发端口并完成一次收发往返，返回来回的字节数。
func dialEcho(t *testing.T, port int, payload []byte) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		t.Fatalf("连接转发端口失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	go func() { _, _ = conn.Write(payload) }()

	got := make([]byte, 0, len(payload))
	buf := make([]byte, 32<<10)
	for len(got) < len(payload) {
		n, err := conn.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	if len(got) != len(payload) {
		t.Fatalf("回显字节数 = %d，期望 %d", len(got), len(payload))
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Fatalf("回显第 %d 字节不一致", i)
		}
	}
}

// TestForwardRoundTripAndStats 覆盖转发闭环：
// 添加规则 → 本地连接进来 → 字节经 SSH 通道抵达目标服务并回显 → 统计与事件对得上。
func TestForwardRoundTripAndStats(t *testing.T) {
	a, _, sid, c := startConnOnly(t)
	targetHost, targetPort := startEcho(t)
	localPort := freePort(t)

	st, err := a.ForwardAdd(sid, localPort, targetHost, targetPort)
	if err != nil {
		t.Fatalf("添加转发失败: %v", err)
	}
	if st.State != "running" {
		t.Fatalf("新规则状态 = %q，期望 running", st.State)
	}
	if st.ListenPort != localPort || st.RemotePort != targetPort {
		t.Fatalf("规则回显不符: %+v", st)
	}

	payload := make([]byte, 100_000)
	for i := range payload {
		payload[i] = byte(i*13 + 5)
	}
	dialEcho(t, localPort, payload)

	// 统计最终要与真实流量一致：两个方向各走了 len(payload) 字节。
	waitFor(t, "转发统计归位", func() bool {
		list, err := a.ForwardList(sid)
		if err != nil || len(list) != 1 {
			return false
		}
		got := list[0]
		return got.TotalConns == 1 && got.BytesOut == int64(len(payload)) && got.BytesIn == int64(len(payload))
	})

	// 连接关掉后活动连接数要归零，不能留下幽灵计数。
	waitFor(t, "活动连接数归零", func() bool {
		list, err := a.ForwardList(sid)
		return err == nil && len(list) == 1 && list[0].Conns == 0
	})

	// 前端能看到的事件里必须有带流量的快照。
	waitFor(t, "统计事件推送", func() bool {
		snap, ok := c.forwardFinal(st.ID)
		return ok && snap.BytesOut > 0
	})
}

// TestForwardReusesSameConnection 钉死 P4 的核心架构约束：
// 转发通道必须挂在同一条已认证连接上，而不是再拨一次号、再握一次手。
func TestForwardReusesSameConnection(t *testing.T) {
	a, srv, sid, c := startPair(t)
	targetHost, targetPort := startEcho(t)
	localPort := freePort(t)

	if _, err := a.ForwardAdd(sid, localPort, targetHost, targetPort); err != nil {
		t.Fatalf("添加转发失败: %v", err)
	}
	dialEcho(t, localPort, []byte("ping"))

	if got := srv.ConnCount(); got != 1 {
		t.Fatalf("转发应复用同一条连接，实际握手了 %d 条", got)
	}
	if got := srv.DirectCount(); got != 1 {
		t.Fatalf("应只开了 1 条 direct-tcpip 通道，实际 %d 条", got)
	}

	// 终端不受影响：转发、终端、文件共存于一个会话是这套设计的前提。
	if err := a.WriteStdin(sid, "echo forward-parallel\n"); err != nil {
		t.Fatalf("终端写入失败: %v", err)
	}
	waitFor(t, "终端与转发并存", func() bool {
		return strings.Contains(c.text(sid), "forward-parallel")
	})
}

// TestForwardStopStart 覆盖启停：停止后本地端口不再收连接，重新启动后恢复。
func TestForwardStopStart(t *testing.T) {
	a, _, sid, _ := startConnOnly(t)
	targetHost, targetPort := startEcho(t)
	localPort := freePort(t)

	st, err := a.ForwardAdd(sid, localPort, targetHost, targetPort)
	if err != nil {
		t.Fatalf("添加转发失败: %v", err)
	}

	stopped, err := a.ForwardStop(st.ID)
	if err != nil {
		t.Fatalf("停止转发失败: %v", err)
	}
	if stopped.State != "stopped" {
		t.Fatalf("停止后状态 = %q，期望 stopped", stopped.State)
	}
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort)), time.Second); err == nil {
		conn.Close()
		t.Fatal("停止后本地端口不应再接受连接")
	}

	// 重复停止是幂等的。
	if _, err := a.ForwardStop(st.ID); err != nil {
		t.Fatalf("重复停止不应报错: %v", err)
	}

	running, err := a.ForwardStart(st.ID)
	if err != nil {
		t.Fatalf("重新启动失败: %v", err)
	}
	if running.State != "running" {
		t.Fatalf("重启后状态 = %q，期望 running", running.State)
	}
	dialEcho(t, localPort, []byte("back"))
}

// TestForwardPortInUse 本地端口被别的程序占着时必须明确报错，
// 且不留半成品规则——否则面板上会躺着一条永远起不来的规则。
func TestForwardPortInUse(t *testing.T) {
	a, _, sid, _ := startConnOnly(t)
	targetHost, targetPort := startEcho(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占端口失败: %v", err)
	}
	defer ln.Close()
	busy := ln.Addr().(*net.TCPAddr).Port

	if _, err := a.ForwardAdd(sid, busy, targetHost, targetPort); err == nil {
		t.Fatal("端口被占用时添加转发应失败")
	} else if !strings.Contains(err.Error(), "监听") {
		t.Fatalf("报错应说明是监听失败，实际: %v", err)
	}

	list, err := a.ForwardList(sid)
	if err != nil {
		t.Fatalf("列出转发失败: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("失败的添加不应留下规则，实际 %d 条", len(list))
	}
}

// TestForwardRejectsBadInput 覆盖输入校验与端口重复：
// 非法端口/主机要挡在门外，同一会话内两条规则不得抢同一个本地端口。
func TestForwardRejectsBadInput(t *testing.T) {
	a, _, sid, _ := startConnOnly(t)
	targetHost, targetPort := startEcho(t)
	localPort := freePort(t)

	cases := []struct {
		name       string
		listenPort int
		host       string
		remotePort int
	}{
		{"本地端口过小", 0, targetHost, targetPort},
		{"本地端口过大", 70000, targetHost, targetPort},
		{"目标端口非法", localPort, targetHost, -1},
		{"目标主机为空", localPort, "  ", targetPort},
		{"目标主机带分隔符", localPort, "evil/host", targetPort},
	}
	for _, tc := range cases {
		if _, err := a.ForwardAdd(sid, tc.listenPort, tc.host, tc.remotePort); err == nil {
			t.Fatalf("%s：非法输入应被拒绝", tc.name)
		}
	}

	if _, err := a.ForwardAdd(sid, localPort, targetHost, targetPort); err != nil {
		t.Fatalf("添加转发失败: %v", err)
	}
	if _, err := a.ForwardAdd(sid, localPort, targetHost, targetPort); err == nil {
		t.Fatal("同一会话内重复占用本地端口应被拒绝")
	} else if !strings.Contains(err.Error(), strconv.Itoa(localPort)) {
		t.Fatalf("报错应指出冲突的端口，实际: %v", err)
	}
}

// TestForwardRemovedOnSessionClose 会话关闭时转发规则必须整体消失、
// 本地端口必须被释放——否则关掉标签后端口还被一个死会话占着。
func TestForwardRemovedOnSessionClose(t *testing.T) {
	a, _, sid, c := startConnOnly(t)
	targetHost, targetPort := startEcho(t)
	localPort := freePort(t)

	st, err := a.ForwardAdd(sid, localPort, targetHost, targetPort)
	if err != nil {
		t.Fatalf("添加转发失败: %v", err)
	}
	dialEcho(t, localPort, []byte("hi"))

	if err := a.CloseSession(sid); err != nil {
		t.Fatalf("关闭会话失败: %v", err)
	}

	list, err := a.ForwardList(sid)
	if err != nil {
		t.Fatalf("列出转发失败: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("会话关闭后不该还有规则，实际 %d 条", len(list))
	}
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort)), time.Second); err == nil {
		conn.Close()
		t.Fatal("会话关闭后本地端口应被释放")
	}

	// 前端会收到 removed 状态，用于把面板上的行清掉。
	snap, ok := c.forwardFinal(st.ID)
	if !ok || snap.State != "removed" {
		t.Fatalf("应推送 removed 终态，实际 %+v / %v", snap, ok)
	}
}
