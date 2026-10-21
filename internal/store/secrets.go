package store

import (
	"errors"
	"sync"
)

// SecretStore 是秘密（服务器密码、私钥口令）的持久化后端。
//
// 抽成接口有两个理由：
//  1. 生产实现是 Windows 凭据管理器（DPAPI 加密、只有当前用户能读回）；
//     只有经过接口，上层才能在没有凭据管理器的环境里被测试；
//  2. 替换实现（内存 → 系统凭据管理器）只发生在一个地方。
type SecretStore interface {
	// Get 取回秘密；不存在时返回 ErrSecretNotFound。
	Get(target string) (string, error)
	// Set 写入或覆盖秘密。
	Set(target, secret string) error
	// Delete 删除秘密；不存在时视为成功（幂等）。
	Delete(target string) error
}

// ErrSecretNotFound 表示该 target 下没有已保存的秘密。
var ErrSecretNotFound = errors.New("store: 未找到已保存的秘密")

// PasswordTarget 是服务器密码在凭据管理器中的键。
// 用 NanoSSH/ 前缀圈定命名空间，避免与其他程序的凭据混淆。
func PasswordTarget(serverID string) string { return "NanoSSH/server/" + serverID }

// PassphraseTarget 是私钥口令在凭据管理器中的键。
func PassphraseTarget(keyID string) string { return "NanoSSH/key/" + keyID }

// MemSecretStore 是进程内实现。
//
// 它有两个正当用途：测试，以及非 Windows 构建的占位——
// 进程退出即丢失，所以它**不是**持久化方案，只保证接口在别处可用。
// NanoSSH 只发布 Windows 版，真实用户永远走 wincred。
type MemSecretStore struct {
	mu sync.Mutex
	m  map[string]string
}

// NewMemSecretStore 构造一个空的进程内秘密存储。
func NewMemSecretStore() *MemSecretStore {
	return &MemSecretStore{m: make(map[string]string)}
}

// Get 实现 SecretStore。
func (s *MemSecretStore) Get(target string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[target]
	if !ok {
		return "", ErrSecretNotFound
	}
	return v, nil
}

// Set 实现 SecretStore。
func (s *MemSecretStore) Set(target, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[target] = secret
	return nil
}

// Delete 实现 SecretStore，幂等。
func (s *MemSecretStore) Delete(target string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, target)
	return nil
}
