// Package sshcore 是 NanoSSH 的内核：零 UI 依赖，可被任意外壳（Wails / 其他）复用。
// 五个功能（Servers / Terminal / SFTP / Port Forward / SSH Key）的逻辑都收敛在这里。
package sshcore

import (
	"net"
	"strconv"
)

// AuthMethod 是一台服务器使用的认证方式。
type AuthMethod string

const (
	AuthPassword AuthMethod = "password"
	AuthKey      AuthMethod = "key"
	AuthAgent    AuthMethod = "agent"
)

// ForwardKind 是端口转发类型，语义对齐 ssh 的 -L / -R / -D。
type ForwardKind string

const (
	// ForwardLocal 对应 ssh -L：本地监听 → 经远端连目标。
	ForwardLocal ForwardKind = "local"
	// ForwardRemote 对应 ssh -R：远端监听 → 回到本地连目标。
	ForwardRemote ForwardKind = "remote"
	// ForwardDynamic 对应 ssh -D：本地起 SOCKS5，经远端出网。
	ForwardDynamic ForwardKind = "dynamic"
)

// ForwardRule 是一条端口转发规则。
type ForwardRule struct {
	ID string `json:"id"`
	// Kind 决定 LocalAddr / RemoteAddr 的解释方式。
	Kind ForwardKind `json:"kind"`
	// LocalAddr 是本地监听地址（-L / -D），形如 127.0.0.1:8080。
	LocalAddr string `json:"localAddr,omitempty"`
	// RemoteAddr 是远端目标地址（-L），或远端监听地址（-R），形如 127.0.0.1:8080。
	RemoteAddr string `json:"remoteAddr,omitempty"`
	// AutoStart 为 true 时，连接建立后自动开启该转发。
	AutoStart bool `json:"autoStart"`
}

// KeyRef 是一个已纳入管理的 SSH 私钥引用。
// 私钥本身以标准 OpenSSH 文件形式存放于 keys/ 目录，可直接与系统 ~/.ssh 互拷。
type KeyRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Path 是私钥文件的绝对路径。
	Path string `json:"path"`
	// Type 是算法名：ed25519 / rsa / ecdsa。
	Type string `json:"type"`
	// Fingerprint 是 SHA256:... 指纹，与 ssh-keygen -lf 输出一致，供用户交叉验证。
	Fingerprint string `json:"fingerprint"`
	// PublicKey 是 authorized_keys 格式的单行公钥，便于一键复制粘贴到服务端。
	PublicKey string `json:"publicKey,omitempty"`
}

// Server 是一台被保存的服务器。这是 servers.json 的核心记录。
type Server struct {
	// ID 是 ULID，同时用作秘密（密码 / passphrase）在凭据管理器中的键。
	ID string `json:"id"`
	// Name 是用户自定义的显示名，如 "VPS-01"。
	Name string `json:"name"`
	// Country 是 ISO-3166 alpha-2 国家码（如 "US"），驱动前端 SVG 国旗图标。
	// 注意：不使用 emoji 国旗——Segoe UI Emoji 缺少 regional-indicator 字形，会退化成两个字母。
	Country string     `json:"country,omitempty"`
	Host    string     `json:"host"`
	Port    int        `json:"port"`
	User    string     `json:"user"`
	Auth    AuthMethod `json:"auth"`
	// KeyID 指向 KeyRef.ID，仅在 Auth 为 key 时使用。
	KeyID string `json:"keyId,omitempty"`
	// Terminal 是请求的 TERM 值，空则默认 xterm-256color。
	Terminal string `json:"terminal,omitempty"`
	// Forwards 是该服务器上配置的转发规则。
	Forwards []ForwardRule `json:"forwards,omitempty"`
}

// Addr 返回 host:port 形式，port 缺省为 22。
func (s Server) Addr() string {
	p := s.Port
	if p == 0 {
		p = 22
	}
	return net.JoinHostPort(s.Host, strconv.Itoa(p))
}
