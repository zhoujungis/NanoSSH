//go:build !windows

package store

// NewSecretStore 返回平台默认的秘密存储。
// 非 Windows 平台没有凭据管理器可用，退化为进程内存储——
// 只为保证内核可编译可测，NanoSSH 的正式产物永远是 Windows 版。
func NewSecretStore() SecretStore { return NewMemSecretStore() }
