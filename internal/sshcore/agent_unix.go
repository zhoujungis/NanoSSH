//go:build !windows

package sshcore

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// dialAgent 连接类 unix 系统上的 ssh-agent（SSH_AUTH_SOCK 指向的域套接字）。
//
// NanoSSH 只发布 Windows 版，这个分支存在的意义是让 sshcore 保持可移植与可测——
// 它是五个功能的内核，不该被外壳或平台绑死。
func dialAgent() (io.ReadWriteCloser, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("sshcore: 未设置 SSH_AUTH_SOCK，无法连接 ssh-agent")
	}
	conn, err := net.DialTimeout("unix", sock, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("sshcore: 连接 ssh-agent（%s）失败: %w", sock, err)
	}
	return conn, nil
}
