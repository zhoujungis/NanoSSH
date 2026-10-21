//go:build windows

package store

import (
	"errors"
	"fmt"

	"github.com/danieljoos/wincred"
)

// wincredStore 把秘密存进 Windows 凭据管理器。
//
// 为什么是指纹管理器而不是自己加密一个文件：那是"自研密码学"的经典陷阱。
// 凭据管理器用 DPAPI 绑定当前用户，只有本人登录后能读回，且不会随数据目录被拷走。
type wincredStore struct{}

// NewSecretStore 返回平台默认的秘密存储。
func NewSecretStore() SecretStore { return wincredStore{} }

// Get 实现 SecretStore。
func (wincredStore) Get(target string) (string, error) {
	cred, err := wincred.GetGenericCredential(target)
	if err != nil {
		if errors.Is(err, wincred.ErrElementNotFound) {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("store: 读取凭据 %q 失败: %w", target, err)
	}
	return string(cred.CredentialBlob), nil
}

// Set 实现 SecretStore。
func (wincredStore) Set(target, secret string) error {
	cred := wincred.NewGenericCredential(target)
	// UserName 在这条凭据里只作展示用，与登录用户名无关。
	cred.UserName = "NanoSSH"
	cred.CredentialBlob = []byte(secret)
	// PersistLocalMachine：重启、注销后依然有效（用户要的正是"只问一次"）。
	// 与凭据管理器里的普通网站密码一致。
	cred.Persist = wincred.PersistLocalMachine
	if err := cred.Write(); err != nil {
		return fmt.Errorf("store: 写入凭据 %q 失败: %w", target, err)
	}
	return nil
}

// Delete 实现 SecretStore，幂等。
func (wincredStore) Delete(target string) error {
	cred, err := wincred.GetGenericCredential(target)
	if err != nil {
		if errors.Is(err, wincred.ErrElementNotFound) {
			return nil
		}
		return fmt.Errorf("store: 读取凭据 %q 失败: %w", target, err)
	}
	if err := cred.Delete(); err != nil {
		return fmt.Errorf("store: 删除凭据 %q 失败: %w", target, err)
	}
	return nil
}
