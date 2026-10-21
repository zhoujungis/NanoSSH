package shell

// 系统托盘图标（Windows）。
//
// 手写 Shell_NotifyIconW，不引第三方 systray 依赖：getlantern/energye 系都会
// 带来数百 KB 增量与各自的平台代码，与"极致轻量"定位冲突；x/sys/windows
// 已在依赖树里，自写约两百行即可覆盖"图标 + 显示/退出菜单 + 气泡提示"。
//
// 线程模型：窗口消息按线程投递，消息窗口必须建在 LockOSThread 的 goroutine
// 上——goroutine 若在建窗后漂移到别的 OS 线程，GetMessage 将永远收不到托盘
// 回调，表现为"点了图标没反应"。

import (
	"errors"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmDestroy       = 0x0002
	wmApp           = 0x8000
	wmTrayCallback  = wmApp + 1 // 托盘鼠标事件回调
	wmTrayDestroy   = 0x0401    // 自定义：销毁托盘图标并退出消息循环
	wmTrayBalloon   = 0x0402    // 自定义：弹一次气泡提示
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifInfo    = 0x00000010

	niifInfo = 0x00000001

	cmdTrayShow = 101
	cmdTrayQuit = 102

	mfString    = 0x00000000
	mfSeparator = 0x00000800

	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
	tpmNonotify    = 0x0080

	idcArrow = 32512
)

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	user32  = windows.NewLazySystemDLL("user32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	procGetModuleHandleW       = kernel32.NewProc("GetModuleHandleW")
	procRegisterClassExW       = user32.NewProc("RegisterClassExW")
	procCreateWindowExW        = user32.NewProc("CreateWindowExW")
	procDestroyWindow          = user32.NewProc("DestroyWindow")
	procDefWindowProcW         = user32.NewProc("DefWindowProcW")
	procGetMessageW            = user32.NewProc("GetMessageW")
	procTranslateMessage       = user32.NewProc("TranslateMessage")
	procDispatchMessageW       = user32.NewProc("DispatchMessageW")
	procPostQuitMessage        = user32.NewProc("PostQuitMessage")
	procPostMessageW           = user32.NewProc("PostMessageW")
	procLoadCursorW            = user32.NewProc("LoadCursorW")
	procSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	procGetCursorPos           = user32.NewProc("GetCursorPos")
	procCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	procAppendMenuW            = user32.NewProc("AppendMenuW")
	procTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	procDestroyMenu            = user32.NewProc("DestroyMenu")
	procDestroyIcon            = user32.NewProc("DestroyIcon")
	procExtractAssociatedIconW = shell32.NewProc("ExtractAssociatedIconW")
	procShellNotifyIconW       = shell32.NewProc("Shell_NotifyIconW")
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

type winPoint struct{ x, y int32 }

type winMsg struct {
	hwnd    windows.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      winPoint
}

// notifyIconDataW 对应 NOTIFYICONDATAW（V3 布局，x64 下 976 字节）。
type notifyIconDataW struct {
	cbSize            uint32
	hWnd              windows.Handle
	uID               uint32
	uFlags            uint32
	uCallbackMessage  uint32
	hIcon             windows.Handle
	szTip             [128]uint16
	dwState           uint32
	dwStateMask       uint32
	szInfo            [256]uint16
	uTimeoutOrVersion uint32
	szInfoTitle       [64]uint16
	dwInfoFlags       uint32
	guidItem          windows.GUID
	hBalloonIcon      windows.Handle
}

// tray 是一个系统托盘图标。newTray 同步等待图标就绪后返回；
// 未就绪前的气泡请求会排队到窗口建好后再弹。
type tray struct {
	onShow func()
	onQuit func()

	mu        sync.Mutex
	hwnd      windows.Handle
	nid       notifyIconDataW
	pending   *trayBalloon
	ready     bool
	destroyed bool
}

type trayBalloon struct{ title, text string }

// wndProcThunk 与 tray 实例的绑定：单实例应用只有一枚托盘图标，
// 用包级变量比 SetWindowLongPtr 简洁得多。
var activeTray *tray

var wndProcCallback = windows.NewCallback(func(hwnd, msg, wparam, lparam uintptr) uintptr {
	t := activeTray
	if t == nil {
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
		return r
	}
	switch msg {
	case wmTrayCallback:
		switch uint32(lparam) {
		case wmLButtonUp, wmLButtonDblClk:
			t.onShow()
		case wmRButtonUp:
			t.showMenu()
		}
		return 0
	case wmTrayBalloon:
		t.flushBalloon()
		return 0
	case wmTrayDestroy:
		t.removeIcon()
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
})

// newTray 创建托盘图标并阻塞到就绪（或失败）。
//
// onShow/onQuit 在 Win32 消息线程上被调用，实现里不得阻塞。
// 懒创建 + 同步握手是刻意的：托盘只在"窗口要收进托盘"时才挂出来（窗口开着
// 时不占通知区），并且创建失败时调用方能立刻知道、放行正常关闭流程——
// 否则会出现"窗口藏起来了、图标却没挂上"的失联窗口。
func newTray(onShow, onQuit func()) (*tray, error) {
	t := &tray{onShow: onShow, onQuit: onQuit}
	activeTray = t

	done := make(chan error, 1)
	go t.run(done)

	select {
	case err := <-done:
		if err != nil {
			activeTray = nil
			return nil, err
		}
	case <-time.After(3 * time.Second):
		activeTray = nil
		return nil, errors.New("托盘图标创建超时")
	}
	return t, nil
}

func (t *tray) run(done chan<- error) {
	// 窗口消息按线程投递：建窗与消息循环必须绑定同一个 OS 线程。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	fail := func(err error) {
		select {
		case done <- err:
		default:
		}
	}

	hInst, _, _ := procGetModuleHandleW.Call(0)
	if hInst == 0 {
		fail(errors.New("GetModuleHandle 失败"))
		return
	}
	hInstance := windows.Handle(hInst)
	className := windows.StringToUTF16Ptr("NanoSSHTrayMsgWindow")
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)

	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   wndProcCallback,
		hInstance:     hInstance,
		hCursor:       windows.Handle(cursor),
		lpszClassName: className,
	}
	if r, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		fail(errors.New("注册托盘窗口类失败"))
		return
	}

	// HWND_MESSAGE(-3)：消息窗口，不占屏幕。
	hwnd, _, _ := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(className)), 0,
		0, 0, 0, 0, 0,
		^uintptr(2), 0, uintptr(hInstance), 0,
	)
	if hwnd == 0 {
		fail(errors.New("创建托盘消息窗口失败"))
		return
	}

	t.mu.Lock()
	t.hwnd = windows.Handle(hwnd)
	t.nid = notifyIconDataW{
		cbSize:           uint32(unsafe.Sizeof(notifyIconDataW{})),
		hWnd:             windows.Handle(hwnd),
		uID:              1,
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: wmTrayCallback,
		hIcon:            loadTrayIcon(hInstance),
	}
	copyUTF16(t.nid.szTip[:], "NanoSSH")
	t.ready = true
	pending := t.pending
	t.pending = nil
	t.mu.Unlock()

	if r, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&t.nid))); r == 0 {
		t.mu.Lock()
		t.ready = false
		t.mu.Unlock()
		procDestroyWindow.Call(hwnd)
		fail(errors.New("Shell_NotifyIcon 添加失败"))
		return
	}
	done <- nil

	if pending != nil {
		t.balloon(pending.title, pending.text)
	}

	// GetMessage 返回 0（WM_QUIT）即退出。
	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// loadTrayIcon 从当前可执行文件提取图标（与任务栏图标同源）。
func loadTrayIcon(hInstance windows.Handle) windows.Handle {
	buf := make([]uint16, windows.MAX_PATH)
	n, err := windows.GetModuleFileName(0, &buf[0], windows.MAX_PATH)
	if err != nil || n == 0 {
		return 0
	}
	var idx uint16
	h, _, _ := procExtractAssociatedIconW.Call(
		uintptr(hInstance),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&idx)),
	)
	return windows.Handle(h)
}

// balloon 弹一次气泡通知（如"仍在托盘运行"）。窗口未就绪时先排队，
// 就绪后由 flushBalloon 统一在消息线程上执行 NIM_MODIFY。
func (t *tray) balloon(title, text string) {
	t.mu.Lock()
	if t.destroyed {
		t.mu.Unlock()
		return
	}
	t.pending = &trayBalloon{title: title, text: text}
	hwnd, ready := t.hwnd, t.ready
	t.mu.Unlock()
	if ready && hwnd != 0 {
		procPostMessageW.Call(uintptr(hwnd), wmTrayBalloon, 0, 0)
	}
}

func (t *tray) flushBalloon() {
	t.mu.Lock()
	if !t.ready || t.destroyed || t.pending == nil {
		t.mu.Unlock()
		return
	}
	b := t.pending
	t.pending = nil
	copyUTF16(t.nid.szInfoTitle[:], b.title)
	copyUTF16(t.nid.szInfo[:], b.text)
	t.nid.dwInfoFlags = niifInfo
	t.nid.uFlags = nifMessage | nifIcon | nifTip | nifInfo
	nid := t.nid
	t.mu.Unlock()

	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))

	// 气泡一次性：发完把 info 区清掉，避免下次 NIM_MODIFY 重复弹出。
	t.mu.Lock()
	t.nid.dwInfoFlags = 0
	t.nid.uFlags = nifMessage | nifIcon | nifTip
	t.nid.szInfo = [256]uint16{}
	t.nid.szInfoTitle = [64]uint16{}
	t.mu.Unlock()
}

// showMenu 在光标处弹右键菜单（显示主窗口 / 退出）。
func (t *tray) showMenu() {
	t.mu.Lock()
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd == 0 {
		return
	}

	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	show := windows.StringToUTF16Ptr("显示主窗口")
	quit := windows.StringToUTF16Ptr("退出")
	procAppendMenuW.Call(hMenu, mfString, cmdTrayShow, uintptr(unsafe.Pointer(show)))
	procAppendMenuW.Call(hMenu, mfSeparator, 0, 0)
	procAppendMenuW.Call(hMenu, mfString, cmdTrayQuit, uintptr(unsafe.Pointer(quit)))

	// TPM_RETURNCMD：菜单项直接以返回值给出，不必绕 WM_COMMAND。
	var pt winPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(uintptr(hwnd))
	cmd, _, _ := procTrackPopupMenu.Call(
		hMenu, tpmRightButton|tpmReturnCmd|tpmNonotify,
		uintptr(uint32(pt.x)), uintptr(uint32(pt.y)),
		0, uintptr(hwnd), 0,
	)

	switch cmd {
	case cmdTrayShow:
		t.onShow()
	case cmdTrayQuit:
		t.onQuit()
	}
}

// destroy 删除托盘图标并结束消息循环（进程退出路径上的收尾）。
func (t *tray) destroy() {
	t.mu.Lock()
	if t.destroyed {
		t.mu.Unlock()
		return
	}
	t.destroyed = true
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(uintptr(hwnd), wmTrayDestroy, 0, 0)
	}
}

func (t *tray) removeIcon() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.ready {
		return
	}
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&t.nid)))
	if t.nid.hIcon != 0 {
		procDestroyIcon.Call(uintptr(t.nid.hIcon))
		t.nid.hIcon = 0
	}
	t.ready = false
}

func copyUTF16(dst []uint16, s string) {
	src := windows.StringToUTF16(s)
	copy(dst, src)
}
