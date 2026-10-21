//go:build windows

package sshcore

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

// TestDialAgentPipePath 是本机唯一能对 Windows 命名管道路径做的实证：
// 没有跑 ssh-agent 时，CreateFile 必须返回"文件不存在"（ERROR_FILE_NOT_FOUND）
// 或"实例全忙"（ERROR_PIPE_BUSY），而不是 ERROR_INVALID_NAME 之类的路径错误。
//
// 这条断言的价值在于：它证明 `\\.\pipe\openssh-ssh-agent` 这个地址真的被送进了
// 管道命名空间。若哪天 Go 或 x/sys 对设备路径的处理变了，这里会立刻炸，
// 而不是等到用户报"agent 认证永远失败"。
func TestDialAgentPipePath(t *testing.T) {
	conn, err := dialAgent()
	if err == nil {
		// 本机确实有 agent：那就顺手确认它真的可用，随后关闭。
		_ = conn.Close()
		t.Skip("本机有可用的 ssh-agent，跳过")
	}

	if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PIPE_BUSY) {
		t.Fatalf("管道路径似乎不合法，实际错误: %v", err)
	}
}
