package main

// NanoSSH 外壳装配：只做窗口与生命周期配置，
// 业务绑定、窗口行为与托盘全部在 internal/shell（App 门面）。

import (
	"embed"
	"log"

	"nanossh/internal/shell"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := shell.NewApp()

	winOpts := &windows.Options{
		// 深浅色跟随系统（含运行时热切换），前端以 prefers-color-scheme 跟随。
		Theme: windows.SystemDefault,
		// 高 DPI / 多显示器下拖拽缩放时 WebView2 重绘去抖，减轻缩放闪烁。
		ResizeDebounceMS: 120,
	}
	// 与前端 --bg-0 对齐，避免启动瞬间白屏闪烁。
	bg := &options.RGBA{R: 11, G: 13, B: 16, A: 1}

	if shell.MicaActive() {
		// Mica：窗口半透明 + WebView 透明，由前端把标题栏/侧栏做成半透明
		// 表面透出系统材质；终端与内容区保持不透明以保证可读性。
		winOpts.BackdropType = windows.Mica
		winOpts.WindowIsTranslucent = true
		winOpts.WebviewIsTransparent = true
		bg = &options.RGBA{R: 0, G: 0, B: 0, A: 0}
	}

	err := wails.Run(&options.App{
		Title:     "NanoSSH",
		Width:     1180,
		Height:    760,
		MinWidth:  900,
		MinHeight: 560,
		// 无边框窗口：标题栏由前端自绘（含拖拽区与窗口控制按钮），
		// 以获得统一的视觉语言；Wails 在 Windows 上为其保留缩放热区与投影。
		Frameless:        true,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: bg,
		OnStartup:        app.Startup,
		OnShutdown:       app.Shutdown,
		OnBeforeClose:    app.BeforeClose,
		Bind: []any{
			app,
		},
		// 单实例：二次启动不再开新窗口，把已有窗口唤到前台即可。
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "nanossh-single-instance",
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				app.ShowWindow()
			},
		},
		Windows: winOpts,
	})
	if err != nil {
		log.Fatal(err)
	}
}
