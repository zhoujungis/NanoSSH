//go:build windows

package sshcore

import (
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const (
	// Windows 版 OpenSSH 的 ssh-agent 监听这个固定的命名管道。
	agentPipeName = `\\.\pipe\openssh-ssh-agent`
	// 管道存在但实例全忙时最多等这么久。宁可给出明确报错，
	// 也不要把连接界面一直卡在"连接中"。
	agentDialTimeout = 3 * time.Second
)

// dialAgent 连接 Windows OpenSSH 的 ssh-agent。
//
// 手写而不用 winio：命名管道只需要 CreateFile + WaitNamedPipe 两步，
// 为这点代码引第三方库是白白增加体积；x/sys 本就是 x/crypto 的传递依赖，不新增模块。
//
// 注意：Windows 上不支持 unix 域套接字，因此这里**不**尝试 SSH_AUTH_SOCK——
// 该变量在 Windows 上即便存在（如 Git Bash 设置）也是 Cygwin 私有格式，Go 拨不通。
func dialAgent() (io.ReadWriteCloser, error) {
	name, err := windows.UTF16PtrFromString(agentPipeName)
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(agentDialTimeout)
	for {
		h, err := windows.CreateFile(
			name,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0, nil, windows.OPEN_EXISTING, 0, 0,
		)
		if err == nil {
			return os.NewFile(uintptr(h), agentPipeName), nil
		}

		if err != windows.ERROR_PIPE_BUSY || time.Now().After(deadline) {
			return nil, fmt.Errorf(
				"sshcore: 无法连接 ssh-agent（%s）: %w；请确认 Windows 的「OpenSSH 身份验证代理」服务已启动",
				agentPipeName, err,
			)
		}
		// 管道存在但实例全忙：小睡后重试 CreateFile。
		// 等价于 WaitNamedPipe，但不必为此再挂一个 lazy proc。
		time.Sleep(50 * time.Millisecond)
	}
}
