package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"nanossh/internal/sshcore"
	"nanossh/internal/store"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App 是暴露给前端的门面（facade）。
//
// 设计约束：App 只做 binding 转发与"字节搬运"，真正的协议逻辑全部在 internal/sshcore，
// 落盘逻辑全部在 internal/store。这样外壳（Wails / 裸 WebView2）可整体替换，
// 而 internal/sshcore 一行不改。终端数据的合并与反压逻辑放在 sessions.go，
// 因为它依赖 Wails 的事件通道，属于外壳层职责。
type App struct {
	ctx context.Context

	paths store.Paths
	store *store.Store

	mu       sync.Mutex
	sessions map[string]*shellSession
	prompts  map[string]chan sshcore.HostKeyDecision

	// transfers 跟踪进行中的文件传输（键为传输 ID），用于取消。
	// 每次传输结束会自行摘除，这里不会积累。
	transfers map[string]context.CancelFunc

	// forwards 是全部端口转发规则（键为规则 ID）。
	// 规则的生命周期随所属会话：会话关闭时由 dropForwards 统一清掉。
	forwards map[string]*forwardEntry

	// passphrasePrompts 与 prompts 同构，用于私钥口令询问。
	// 分开维护是为了让两条询问链路的失效与超时互不牵连。
	passphrasePrompts map[string]chan passphraseAnswer

	// secrets 是密码与私钥口令的持久化后端（Windows 凭据管理器）。
	// 抽成接口字段，测试里可整体替换成内存实现，不碰真实凭据库。
	secrets store.SecretStore

	// emit 是发往前端的事件通道，默认是 Wails 的 runtime.EventsEmit。
	// 抽成字段是为了让字节搬运与反压逻辑能在无 WebView 的测试中替换掉它。
	emit emitFunc

	// tray 是系统托盘图标；为 nil 表示托盘不可用，此时关闭窗口直接退出。
	tray *tray

	// quitting 标记用户显式退出（托盘菜单"退出"），BeforeClose 据此放行关闭。
	quitting bool

	// trayTipShown 保证"收进托盘"的气泡提示只弹一次，不反复打扰。
	trayTipShown bool
}

// passphraseAnswer 是前端对 passphrase:prompt 的回复。
type passphraseAnswer struct {
	Passphrase string
	Cancelled  bool
}

// ConnectRequest 是"连接一台已保存服务器"的输入。
type ConnectRequest struct {
	ServerID string `json:"serverId"`
	// Password 仅在服务器配置为密码认证时使用；留空表示复用本次进程内已缓存的口令。
	Password string `json:"password,omitempty"`
}

// ConnectResult 是一次成功连接的句柄。
type ConnectResult struct {
	SessionID string         `json:"sessionId"`
	Server    sshcore.Server `json:"server"`
}

// HostKeyPromptPayload 是发往前端的 TOFU / 指纹变化询问。
// Changed 为 true 时前端必须按"硬阻断"样式呈现（显示旧 vs 新，且没有"仅本次"选项）。
type HostKeyPromptPayload struct {
	RequestID       string   `json:"requestId"`
	Host            string   `json:"host"`
	Port            int      `json:"port"`
	KeyType         string   `json:"keyType"`
	Fingerprint     string   `json:"fingerprint"`
	Changed         bool     `json:"changed"`
	OldFingerprints []string `json:"oldFingerprints,omitempty"`
}

func NewApp() *App {
	return &App{
		sessions:          make(map[string]*shellSession),
		prompts:           make(map[string]chan sshcore.HostKeyDecision),
		passphrasePrompts: make(map[string]chan passphraseAnswer),
		transfers:         make(map[string]context.CancelFunc),
		forwards:          make(map[string]*forwardEntry),
		secrets:           store.NewSecretStore(),
		emit:              wruntime.EventsEmit,
	}
}

func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx

	paths, err := store.Resolve()
	if err != nil {
		wruntime.LogError(ctx, "定位数据目录失败: "+err.Error())
		return
	}
	if err := paths.Ensure(); err != nil {
		wruntime.LogError(ctx, "创建数据目录失败: "+err.Error())
		return
	}
	a.paths = paths
	a.store = store.New(paths.Servers)
}

// MicaEnabled 告知前端是否启用了 Win11 Mica 系统背景。
// 启用时前端把标题栏/侧栏做成半透明表面透出材质；否则保持不透明。
func (a *App) MicaEnabled() bool { return micaActive }

// ShowWindow 把主窗口唤到前台（托盘点击、二次实例唤起共用）。
func (a *App) ShowWindow() {
	if a.ctx == nil {
		return
	}
	wruntime.WindowUnminimise(a.ctx)
	wruntime.WindowShow(a.ctx)
}

// quitFromTray 显式退出应用。
func (a *App) quitFromTray() {
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
	if a.ctx != nil {
		wruntime.Quit(a.ctx)
	}
}

// RequestClose 供前端标题栏关闭按钮调用，语义与系统关闭按钮一致：
// 托盘可用时收进托盘继续运行，否则退出。
func (a *App) RequestClose() {
	if a.ctx == nil {
		return
	}
	if !a.BeforeClose(a.ctx) {
		a.quitFromTray()
	}
}

// BeforeClose 拦截窗口关闭（含 Alt+F4）：托盘可用且非显式退出时把窗口收进
// 托盘继续运行，SSH 会话不中断。返回 true 表示阻止本次关闭。
func (a *App) BeforeClose(ctx context.Context) bool {
	a.mu.Lock()
	quitting := a.quitting
	a.mu.Unlock()
	if quitting {
		return false
	}

	// 托盘懒创建：只在真的要收进托盘时才挂图标（窗口开着时不占通知区），
	// 创建失败则放行正常关闭，绝不留下"藏起来了却没图标"的失联窗口。
	trayOK := a.ensureTray()
	if !trayOK {
		return false
	}

	wruntime.WindowHide(ctx)
	a.mu.Lock()
	tipShown := a.trayTipShown
	a.trayTipShown = true
	a.mu.Unlock()
	if !tipShown {
		a.tray.balloon("NanoSSH", "仍在托盘运行，双击图标可回到窗口")
	}
	return true
}

// ensureTray 保证托盘图标存在；返回是否可用。
func (a *App) ensureTray() bool {
	a.mu.Lock()
	if a.tray != nil {
		a.mu.Unlock()
		return true
	}
	a.mu.Unlock()

	t, err := newTray(a.ShowWindow, a.quitFromTray)
	if err != nil {
		wruntime.LogWarning(a.ctx, "托盘图标不可用: "+err.Error())
		return false
	}
	a.mu.Lock()
	a.tray = t
	a.mu.Unlock()
	return true
}

// Version 供前端标题栏显示。
func (a *App) Version() string { return "0.1.0-dev" }

// DataDir 返回数据根目录，供前端"打开配置目录"使用。
func (a *App) DataDir() string { return a.paths.Dir }

// ListServers 返回全部已保存的服务器。
func (a *App) ListServers() ([]sshcore.Server, error) {
	f, err := a.load()
	if err != nil {
		return nil, err
	}
	if f.Servers == nil {
		return []sshcore.Server{}, nil
	}
	return f.Servers, nil
}

// SaveServer 新增或更新一台服务器；ID 为空时分配新 ULID。
func (a *App) SaveServer(s sshcore.Server) (sshcore.Server, error) {
	f, err := a.load()
	if err != nil {
		return s, err
	}
	if s.Port == 0 {
		s.Port = 22
	}
	if s.Auth == "" {
		s.Auth = sshcore.AuthPassword
	}

	if s.ID == "" {
		s.ID = store.NewID()
		f.Servers = append(f.Servers, s)
	} else {
		idx := -1
		for i := range f.Servers {
			if f.Servers[i].ID == s.ID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return s, fmt.Errorf("save: 未找到 id=%s 的服务器", s.ID)
		}
		f.Servers[idx] = s
	}

	if err := a.store.Save(f); err != nil {
		return s, err
	}
	return s, nil
}

// DeleteServer 删除一台服务器。
func (a *App) DeleteServer(id string) error {
	f, err := a.load()
	if err != nil {
		return err
	}
	out := f.Servers[:0]
	found := false
	for _, s := range f.Servers {
		if s.ID == id {
			found = true
			continue
		}
		out = append(out, s)
	}
	if !found {
		return fmt.Errorf("delete: 未找到 id=%s 的服务器", id)
	}
	f.Servers = out

	// 秘密与服务器记录同生命周期：删服务器就该把它的密码一起抹掉，
	// 否则凭据管理器里会留下一堆指向不存在主机的孤儿密码。
	if err := a.secrets.Delete(store.PasswordTarget(id)); err != nil {
		return err
	}

	return a.store.Save(f)
}

// Connect 建立到指定服务器的连接，返回一个会话句柄。
//
// 未知主机 / 指纹变化会通过 hostkey:prompt 事件询问前端，本调用在此期间挂起，
// 直到前端回传 HostKeyDecision 才返回。Wails 的每个 binding 调用各跑在独立
// goroutine 上，因此挂起不会阻塞 UI。
func (a *App) Connect(req ConnectRequest) (*ConnectResult, error) {
	if a.store == nil {
		return nil, errors.New("connect: 数据目录尚未就绪")
	}
	f, err := a.load()
	if err != nil {
		return nil, err
	}

	var srv *sshcore.Server
	for i := range f.Servers {
		if f.Servers[i].ID == req.ServerID {
			srv = &f.Servers[i]
			break
		}
	}
	if srv == nil {
		return nil, fmt.Errorf("connect: 未找到 id=%s 的服务器", req.ServerID)
	}

	auth, err := a.resolveAuth(*srv, req.Password)
	if err != nil {
		return nil, err
	}

	client, err := sshcore.Connect(a.ctx, sshcore.ConnectOptions{
		Server:         *srv,
		Auth:           auth,
		KnownHostsPath: a.paths.KnownHosts,
		HostKeyPrompt:  a.askHostKey,
	})
	if err != nil {
		// 认证失败时清掉已保存的密码，让下次连接重新询问。
		// 没有这一步，用户改了远端密码后会陷入"点了就失败、却再也没机会输入"的死角。
		if srv.Auth == sshcore.AuthPassword && req.Password == "" &&
			strings.Contains(err.Error(), "unable to authenticate") {
			_ = a.secrets.Delete(store.PasswordTarget(srv.ID))
		}
		return nil, err
	}

	// 认证成功后才把用户输入的密码落库：手滑打错的密码不值得写进凭据管理器。
	// 密码来自已保存记录（req.Password 为空）时无需重复写入。
	if srv.Auth == sshcore.AuthPassword && req.Password != "" {
		if serr := a.secrets.Set(store.PasswordTarget(srv.ID), req.Password); serr != nil {
			// 存不下只是少了个便利，不该让已经建立好的连接失败。
			wruntime.LogWarning(a.ctx, "保存服务器密码失败: "+serr.Error())
		}
	}

	sess := newShellSession(a.ctx, store.NewID(), *srv, client, a.emit)
	a.mu.Lock()
	a.sessions[sess.id] = sess
	a.mu.Unlock()

	// 连接被远端或网络断开时，主动通知前端，避免"卡住但不报错"。
	go func() {
		<-client.Closed()
		sess.terminate("连接已断开")
	}()

	return &ConnectResult{SessionID: sess.id, Server: *srv}, nil
}

// HostKeyDecision 是前端对 hostkey:prompt 的回复。
// decision 取值：once（仅本次）/ save（信任并保存）/ reject（取消）。
// 指纹变化场景下 only "save" 会被接受，其余一律阻断。
func (a *App) HostKeyDecision(requestID, decision string) error {
	var d sshcore.HostKeyDecision
	switch decision {
	case "once":
		d = sshcore.HostKeyAcceptOnce
	case "save":
		d = sshcore.HostKeyAcceptAndSave
	case "reject":
		d = sshcore.HostKeyReject
	default:
		return fmt.Errorf("hostkey: 未知的决策 %q", decision)
	}

	a.mu.Lock()
	ch, ok := a.prompts[requestID]
	delete(a.prompts, requestID)
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("hostkey: 询问 %s 已失效", requestID)
	}
	ch <- d
	return nil
}

// askHostKey 由 sshcore 在握手过程中回调（见 HostKeyPrompt）。
func (a *App) askHostKey(info sshcore.HostKeyInfo, change *sshcore.HostKeyChange) (sshcore.HostKeyDecision, error) {
	reqID := store.NewID()
	ch := make(chan sshcore.HostKeyDecision, 1)

	a.mu.Lock()
	a.prompts[reqID] = ch
	a.mu.Unlock()

	payload := HostKeyPromptPayload{
		RequestID:   reqID,
		Host:        info.Host,
		Port:        info.Port,
		KeyType:     info.KeyType,
		Fingerprint: info.Fingerprint,
	}
	if change != nil {
		payload.Changed = true
		payload.OldFingerprints = change.OldFingerprints
	}
	a.emit(a.ctx, "hostkey:prompt", payload)

	select {
	case d := <-ch:
		return d, nil
	case <-a.ctx.Done():
		return sshcore.HostKeyReject, nil
	}
}

// PassphrasePromptPayload 是发往前端的私钥口令询问。
type PassphrasePromptPayload struct {
	RequestID string `json:"requestId"`
	KeyID     string `json:"keyId"`
	KeyName   string `json:"keyName"`
	// Retry 为 true 表示上一次的口令不对，前端应提示用户重试而不是当作首次询问。
	Retry bool `json:"retry"`
}

// PassphraseDecision 是前端对 passphrase:prompt 的回复。
// cancelled 为 true 表示用户放弃（或前端关闭了对话框），此时连接中止。
func (a *App) PassphraseDecision(requestID, passphrase string, cancelled bool) error {
	a.mu.Lock()
	ch, ok := a.passphrasePrompts[requestID]
	delete(a.passphrasePrompts, requestID)
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("passphrase: 询问 %s 已失效", requestID)
	}
	ch <- passphraseAnswer{Passphrase: passphrase, Cancelled: cancelled}
	return nil
}

// askPassphrase 在连接过程中向前端索要私钥口令，语义与 askHostKey 一致：
// 调用挂起，直到前端回复或应用退出。
func (a *App) askPassphrase(ref sshcore.KeyRef, retry bool) (passphraseAnswer, error) {
	reqID := store.NewID()
	ch := make(chan passphraseAnswer, 1)

	a.mu.Lock()
	a.passphrasePrompts[reqID] = ch
	a.mu.Unlock()

	a.emit(a.ctx, "passphrase:prompt", PassphrasePromptPayload{
		RequestID: reqID,
		KeyID:     ref.ID,
		KeyName:   ref.Name,
		Retry:     retry,
	})

	select {
	case ans := <-ch:
		return ans, nil
	case <-a.ctx.Done():
		return passphraseAnswer{Cancelled: true}, nil
	}
}

func (a *App) load() (*store.File, error) {
	if a.store == nil {
		return nil, errors.New("store: 数据目录尚未就绪")
	}
	return a.store.Load()
}

func (a *App) Shutdown(context.Context) {
	if a.tray != nil {
		a.tray.destroy()
	}
	a.mu.Lock()
	sessions := make([]*shellSession, 0, len(a.sessions))
	for _, s := range a.sessions {
		sessions = append(sessions, s)
	}
	a.sessions = make(map[string]*shellSession)

	// 传输随会话一起收尾：留着它们只会对着已关闭的通道继续徒劳写入。
	cancels := make([]context.CancelFunc, 0, len(a.transfers))
	for _, cancel := range a.transfers {
		cancels = append(cancels, cancel)
	}
	a.transfers = make(map[string]context.CancelFunc)
	a.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	for _, s := range sessions {
		// 转发监听随会话一起收尾：留着只会占住本地端口，
		// 指向一条已经不存在的连接。
		a.dropForwards(s.id)
		s.close()
	}
}
