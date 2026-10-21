package sshcore

import (
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// AuthInput 携带一次连接所需的凭据。
// 凭据的"取"由上层负责（密码走凭据管理器、passphrase 由前端询问），内核只负责"用"。
type AuthInput struct {
	// Password 供 AuthPassword 使用。
	Password string
	// Signer 供 AuthKey 使用。私钥由 keys.go 解析。
	Signer ssh.Signer
	// Agent 供 AuthAgent 使用。留空时内核自行连接系统 ssh-agent；
	// 显式注入的用途是测试（用进程内 agent 替身），生产路径不会走到。
	Agent agent.ExtendedAgent
}

// ErrNoAuthMethod 表示没有任何可用的认证方式。
var ErrNoAuthMethod = errors.New("sshcore: 没有可用的认证方式")

// authMethods 按服务器的 Auth 配置装配认证方法，并返回需要随连接一起关闭的资源。
//
// 顺序遵循 OpenSSH 习惯：公开密钥优先于密码——避免服务端先记一次密码失败。
func authMethods(s Server, in AuthInput) ([]ssh.AuthMethod, io.Closer, error) {
	switch s.Auth {
	case AuthKey:
		if in.Signer == nil {
			return nil, nil, fmt.Errorf("sshcore: 服务器 %s 配置为密钥认证，但未提供可用私钥", s.Name)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(in.Signer)}, nil, nil

	case AuthPassword:
		if in.Password == "" {
			return nil, nil, fmt.Errorf("sshcore: 服务器 %s 配置为密码认证，但未提供密码", s.Name)
		}
		return []ssh.AuthMethod{ssh.Password(in.Password)}, nil, nil

	case AuthAgent:
		ag := in.Agent
		var closer io.Closer
		if ag == nil {
			conn, err := dialAgent()
			if err != nil {
				return nil, nil, err
			}
			ag = agent.NewClient(conn)
			closer = conn
		}
		// 先探一次钥匙串：agent 里没有密钥时给出明确文案，
		// 否则用户只会看到 ssh 库那句含糊的 "no supported methods remain"。
		identities, err := ag.List()
		if err != nil {
			if closer != nil {
				closer.Close()
			}
			return nil, nil, fmt.Errorf("sshcore: 读取 ssh-agent 密钥列表失败: %w", err)
		}
		if len(identities) == 0 {
			if closer != nil {
				closer.Close()
			}
			return nil, nil, errors.New("sshcore: ssh-agent 中没有可用密钥，请先 ssh-add")
		}
		return []ssh.AuthMethod{ssh.PublicKeysCallback(ag.Signers)}, closer, nil

	default:
		return nil, nil, fmt.Errorf("sshcore: 服务器 %s 的认证方式 %q 未知", s.Name, s.Auth)
	}
}
