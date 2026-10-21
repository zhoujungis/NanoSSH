package shell

import (
	"errors"
	"fmt"
	"os"

	"nanossh/internal/sshcore"
	"nanossh/internal/store"

	"golang.org/x/crypto/ssh"
)

// HasPassword 告知前端该服务器是否已有保存的密码。
// 前端据此决定"直接连"还是"先弹密码框"——没有它，每次连接都要白问一遍。
func (a *App) HasPassword(serverID string) bool {
	_, err := a.secrets.Get(store.PasswordTarget(serverID))
	return err == nil
}

// ForgetPassword 清除已保存的密码，供 UI 的"清除已保存密码"使用。
func (a *App) ForgetPassword(serverID string) error {
	return a.secrets.Delete(store.PasswordTarget(serverID))
}

// resolveAuth 按服务器配置与前端传入的密码，装配出本次连接所需的凭据。
//
// 分工：秘密的"取"在这里完成（凭据管理器 / 询问前端），sshcore 只负责"用"。
// 注意这里刻意不写库——只有认证成功了才值得保存，见 Connect。
func (a *App) resolveAuth(srv sshcore.Server, password string) (sshcore.AuthInput, error) {
	switch srv.Auth {
	case sshcore.AuthPassword:
		if password == "" {
			saved, err := a.secrets.Get(store.PasswordTarget(srv.ID))
			if err != nil && !errors.Is(err, store.ErrSecretNotFound) {
				return sshcore.AuthInput{}, err
			}
			password = saved
		}
		if password == "" {
			return sshcore.AuthInput{}, fmt.Errorf("connect: 服务器 %s 尚未提供密码", srv.Name)
		}
		return sshcore.AuthInput{Password: password}, nil

	case sshcore.AuthKey:
		signer, err := a.loadSigner(srv)
		if err != nil {
			return sshcore.AuthInput{}, err
		}
		return sshcore.AuthInput{Signer: signer}, nil

	case sshcore.AuthAgent:
		// agent 无需凭据，交给内核去连系统 ssh-agent。
		return sshcore.AuthInput{}, nil

	default:
		return sshcore.AuthInput{}, fmt.Errorf("connect: 服务器 %s 的认证方式 %q 未知", srv.Name, srv.Auth)
	}
}

// loadSigner 取回服务器引用的私钥并解析成签名器，必要时向前端索要口令。
func (a *App) loadSigner(srv sshcore.Server) (ssh.Signer, error) {
	if srv.KeyID == "" {
		return nil, fmt.Errorf("connect: 服务器 %s 配置了密钥认证但未选择密钥", srv.Name)
	}
	ref, err := a.findKey(srv.KeyID)
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(a.keyFilePath(ref.ID))
	if err != nil {
		return nil, fmt.Errorf("connect: 读取私钥 %s 失败: %w", ref.Name, err)
	}

	// 口令优先取已保存的；没有就问前端。
	passphrase, err := a.secrets.Get(store.PassphraseTarget(ref.ID))
	if err != nil {
		if !errors.Is(err, store.ErrSecretNotFound) {
			return nil, err
		}
		passphrase = ""
	}

	signer, err := sshcore.ParsePrivateKey(raw, passphrase)
	switch {
	case err == nil:
		return signer, nil

	case errors.Is(err, sshcore.ErrKeyEncrypted):
		// 还没保存过口令：按首次询问处理。
		return a.askSignerPassphrase(ref, raw, false)

	case errors.Is(err, sshcore.ErrKeyFIDOUnsupported):
		return nil, err

	default:
		// 走到这里说明文件能读、但口令没解开——保存的口令多半已经失效（比如用户改过）。
		// 清掉它再问一次，否则用户会永远卡在"保存的旧口令"上。
		if passphrase != "" {
			_ = a.secrets.Delete(store.PassphraseTarget(ref.ID))
			return a.askSignerPassphrase(ref, raw, true)
		}
		return nil, err
	}
}

// askSignerPassphrase 向前端索要口令，解开后立刻落库，实现"重启应用也只问一次"。
// 允许多次重试：口令打错不该重新走一遍 TCP 握手。
func (a *App) askSignerPassphrase(ref sshcore.KeyRef, raw []byte, retry bool) (ssh.Signer, error) {
	for attempt := 0; attempt < 3; attempt++ {
		ans, err := a.askPassphrase(ref, retry)
		if err != nil {
			return nil, err
		}
		if ans.Cancelled || ans.Passphrase == "" {
			return nil, errors.New("connect: 已取消输入私钥口令")
		}

		signer, perr := sshcore.ParsePrivateKey(raw, ans.Passphrase)
		if perr == nil {
			if err := a.secrets.Set(store.PassphraseTarget(ref.ID), ans.Passphrase); err != nil {
				return nil, err
			}
			return signer, nil
		}
		retry = true
	}
	return nil, errors.New("connect: 私钥口令连续 3 次不正确，已放弃")
}
