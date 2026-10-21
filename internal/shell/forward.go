package shell

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"nanossh/internal/sshcore"
	"nanossh/internal/store"
)

// 端口转发绑定层（P4）。
//
// 与 SFTP 同一套思路：sshcore 负责 direct-tcpip 协议，本文件只做三件事——
// 把规则变成监听、把状态变成事件、把生命周期与会话绑在一起。
// 转发复用会话的同一条已认证连接：不拨号、不握手，终端与文件面板照常并存。

// ForwardRule 是一条本地端口转发规则（ssh -L 语义）：
// 本地 127.0.0.1:listenPort →（经 SSH 连接）→ remoteHost:remotePort。
type ForwardRule struct {
	ID         string `json:"id"`
	SessionID  string `json:"sessionId"`
	ListenPort int    `json:"listenPort"`
	RemoteHost string `json:"remoteHost"`
	RemotePort int    `json:"remotePort"`
	// State 取 running / stopped / error / removed（removed 只出现在删除事件里）。
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// ForwardStatus 是一条规则的状态快照，经 forward:status 事件推送。
type ForwardStatus struct {
	ForwardRule
	// Conns 是当前活动连接数，TotalConns 是本条监听累计接受的连接数。
	Conns      int   `json:"conns"`
	TotalConns int64 `json:"totalConns"`
	// BytesIn 是远端→本地流量，BytesOut 是本地→远端流量。
	BytesIn  int64 `json:"bytesIn"`
	BytesOut int64 `json:"bytesOut"`
}

// forwardEntry 是一条规则的运行时状态；fwd 为 nil 表示当前停止。
type forwardEntry struct {
	rule ForwardRule
	fwd  *sshcore.Forward
}

// ForwardAdd 新增一条转发规则并立即启动监听。
//
// 监听只绑 127.0.0.1：把服务暴露到局域网必须是显式决定，不能是顺手的默认值。
func (a *App) ForwardAdd(sessionID string, listenPort int, remoteHost string, remotePort int) (ForwardStatus, error) {
	rule := ForwardRule{
		ID:         store.NewID(),
		SessionID:  sessionID,
		ListenPort: listenPort,
		RemoteHost: strings.TrimSpace(remoteHost),
		RemotePort: remotePort,
		State:      "stopped",
	}
	if err := validateForward(rule); err != nil {
		return ForwardStatus{}, err
	}
	if _, err := a.session(sessionID); err != nil {
		return ForwardStatus{}, err
	}

	e := &forwardEntry{rule: rule}
	a.mu.Lock()
	// 本地端口是整台机器的资源：同一会话内不许两条规则抢同一个端口。
	for _, other := range a.forwards {
		if other.rule.SessionID == sessionID && other.rule.ListenPort == listenPort {
			a.mu.Unlock()
			return ForwardStatus{}, fmt.Errorf("forward: 本地端口 %d 已有转发规则", listenPort)
		}
	}
	a.forwards[rule.ID] = e
	a.mu.Unlock()

	f, err := a.forwardStartEntry(e)
	if err != nil {
		// 起不来（比如端口被别的程序占了）就不留半成品规则。
		a.mu.Lock()
		delete(a.forwards, rule.ID)
		a.mu.Unlock()
		return ForwardStatus{}, err
	}
	return a.forwardStatusOf(e, f), nil
}

// ForwardList 返回某个会话的全部转发规则。
func (a *App) ForwardList(sessionID string) ([]ForwardStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ForwardStatus, 0, len(a.forwards))
	for _, e := range a.forwards {
		if e.rule.SessionID != sessionID {
			continue
		}
		var st sshcore.ForwardStats
		if e.fwd != nil {
			st = e.fwd.Stats()
		}
		out = append(out, e.statusLocked(st))
	}
	return out, nil
}

// ForwardStop 停止一条规则的监听（规则保留，可再启动）。已停止时是幂等的。
func (a *App) ForwardStop(id string) (ForwardStatus, error) {
	e, err := a.forwardEntryOf(id)
	if err != nil {
		return ForwardStatus{}, err
	}
	f := e.detachLocked()
	if f == nil {
		a.mu.Unlock() // 本来就是停止态，幂等返回
		return a.forwardStatusOf(e, nil), nil
	}
	e.setStateLocked("stopped", "")
	a.mu.Unlock()

	f.Close()
	st := a.forwardStatusOf(e, nil)
	a.emit(a.ctx, "forward:status", st)
	return st, nil
}

// ForwardStart 重新启动一条已停止的规则。
func (a *App) ForwardStart(id string) (ForwardStatus, error) {
	e, err := a.forwardEntryOf(id)
	if err != nil {
		return ForwardStatus{}, err
	}
	if e.fwd != nil {
		f := e.fwd
		a.mu.Unlock()
		return a.forwardStatusOf(e, f), nil
	}
	a.mu.Unlock()

	f, err := a.forwardStartEntry(e)
	if err != nil {
		a.mu.Lock()
		e.setStateLocked("error", err.Error())
		a.mu.Unlock()
		st := a.forwardStatusOf(e, nil)
		a.emit(a.ctx, "forward:status", st)
		return st, err
	}
	st := a.forwardStatusOf(e, f)
	a.emit(a.ctx, "forward:status", st)
	return st, nil
}

// ForwardRemove 停止并删除一条规则。
func (a *App) ForwardRemove(id string) error {
	a.mu.Lock()
	e, ok := a.forwards[id]
	if !ok {
		a.mu.Unlock()
		return fmt.Errorf("forward: 未找到规则 %s", id)
	}
	delete(a.forwards, id)
	f := e.detachLocked()
	e.setStateLocked("removed", "")
	a.mu.Unlock()

	if f != nil {
		f.Close()
	}
	a.emit(a.ctx, "forward:status", a.forwardStatusOf(e, nil))
	return nil
}

// forwardStartEntry 启动监听并挂上统计推送协程，返回新启动的监听句柄。
func (a *App) forwardStartEntry(e *forwardEntry) (*sshcore.Forward, error) {
	sess, err := a.session(e.rule.SessionID)
	if err != nil {
		return nil, err
	}
	f, err := sess.client.Forward(
		net.JoinHostPort("127.0.0.1", strconv.Itoa(e.rule.ListenPort)),
		net.JoinHostPort(e.rule.RemoteHost, strconv.Itoa(e.rule.RemotePort)),
	)
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	e.fwd = f
	e.setStateLocked("running", "")
	a.mu.Unlock()

	go a.runForward(e, f)
	return f, nil
}

// runForward 推送统计快照，并在连接断开时把监听收干净。
//
// 统计只在数值变化时推送：空闲的转发不该每 120ms 打一次无意义的事件。
func (a *App) runForward(e *forwardEntry, f *sshcore.Forward) {
	sess, err := a.session(e.rule.SessionID)
	if err != nil {
		f.Close()
		return
	}
	clientGone := sess.client.Closed()

	ticker := time.NewTicker(transferEmitGap)
	defer ticker.Stop()
	var last sshcore.ForwardStats
	for {
		select {
		case <-f.Done():
			// 规则可能已被删除；删了就不再发事件，免得前端把行复活。
			a.mu.Lock()
			alive := a.forwards[e.rule.ID] == e
			a.mu.Unlock()
			if alive {
				a.emit(a.ctx, "forward:status", a.forwardStatusOf(e, f))
			}
			return
		case <-clientGone:
			// 连接没了转发自然失效，但监听端口还占着——必须主动收掉。
			a.mu.Lock()
			if a.forwards[e.rule.ID] == e {
				e.detachLocked()
				e.setStateLocked("error", "连接已断开")
			}
			a.mu.Unlock()
			f.Close()
			return
		case <-a.ctx.Done():
			f.Close()
			return
		case <-ticker.C:
			st := f.Stats()
			if st == last {
				continue
			}
			last = st
			a.emit(a.ctx, "forward:status", a.forwardStatusOf(e, f))
		}
	}
}

// dropForwards 清掉某个会话下的全部转发规则（会话关闭时调用）。
func (a *App) dropForwards(sessionID string) {
	a.mu.Lock()
	var gone []*forwardEntry
	var stale []*sshcore.Forward
	for id, e := range a.forwards {
		if e.rule.SessionID != sessionID {
			continue
		}
		delete(a.forwards, id)
		gone = append(gone, e)
		if f := e.detachLocked(); f != nil {
			stale = append(stale, f)
		}
		e.setStateLocked("removed", "")
	}
	a.mu.Unlock()

	for _, f := range stale {
		f.Close()
	}
	for _, e := range gone {
		a.emit(a.ctx, "forward:status", a.forwardStatusOf(e, nil))
	}
}

// forwardEntryOf 取一条规则并保持加锁状态返回，调用方负责解锁。
func (a *App) forwardEntryOf(id string) (*forwardEntry, error) {
	a.mu.Lock()
	e, ok := a.forwards[id]
	if !ok {
		a.mu.Unlock()
		return nil, fmt.Errorf("forward: 未找到规则 %s", id)
	}
	return e, nil
}

func (e *forwardEntry) setStateLocked(state, errMsg string) {
	e.rule.State = state
	e.rule.Error = errMsg
}

// detachLocked 摘掉正在跑的监听并返回它（没有则返回 nil），调用方负责解锁。
func (e *forwardEntry) detachLocked() *sshcore.Forward {
	f := e.fwd
	e.fwd = nil
	return f
}

// forwardStatusOf 拍一张状态快照；f 为 nil 时统计全零。
func (a *App) forwardStatusOf(e *forwardEntry, f *sshcore.Forward) ForwardStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	var st sshcore.ForwardStats
	if f != nil {
		st = f.Stats()
	}
	return e.statusLocked(st)
}

func (e *forwardEntry) statusLocked(st sshcore.ForwardStats) ForwardStatus {
	return ForwardStatus{
		ForwardRule: e.rule,
		Conns:       int(st.Active),
		TotalConns:  st.Total,
		BytesIn:     st.In,
		BytesOut:    st.Out,
	}
}

// validateForward 校验前端给的规则输入；端口占用冲突由监听直接报错。
func validateForward(r ForwardRule) error {
	switch {
	case r.ListenPort < 1 || r.ListenPort > 65535:
		return errors.New("forward: 本地端口必须在 1~65535 之间")
	case r.RemotePort < 1 || r.RemotePort > 65535:
		return errors.New("forward: 目标端口必须在 1~65535 之间")
	case r.RemoteHost == "":
		return errors.New("forward: 目标主机不能为空")
	case strings.ContainsAny(r.RemoteHost, " \t/\\"):
		return errors.New("forward: 目标主机不合法")
	}
	return nil
}
