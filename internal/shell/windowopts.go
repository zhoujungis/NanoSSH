package shell

// 窗口装配相关的系统能力探测：与业务解耦，供 main 包装配窗口选项时使用。

import (
	"strconv"

	"golang.org/x/sys/windows/registry"
)

// micaActive 表示本次运行是否启用了 Win11 Mica 系统背景。
// 前端通过 App.MicaEnabled() 读取，决定表面是否做成半透明以透出材质。
var micaActive = detectMica()

// MicaActive 供 main 包装配窗口选项：返回是否启用 Mica 系统背景。
func MicaActive() bool { return micaActive }

// detectMica 判断系统是否支持 Mica 背景（Windows 11 22H2 / build 22621 起）。
//
// 老系统上若强行开 WindowIsTranslucent，会得到一个没有底色的透明窗口——
// 内容直接"浮"在桌面上，观感比没有 Mica 差得多。因此半透明必须按系统能力
// 开关，并让前端感知到该决策。
func detectMica() bool {
	k, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return false
	}
	defer k.Close()

	s, _, err := k.GetStringValue("CurrentBuildNumber")
	if err != nil {
		return false
	}
	build, err := strconv.Atoi(s)
	if err != nil {
		return false
	}
	return build >= 22621
}
