package store

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"nanossh/internal/sshcore"
)

// CurrentVersion 是 servers.json 的 schema 版本。
const CurrentVersion = 1

// File 是 servers.json 的内存表示。
type File struct {
	Version  int              `json:"version"`
	Servers  []sshcore.Server `json:"servers"`
	Keys     []sshcore.KeyRef `json:"keys,omitempty"`
	Settings Settings         `json:"settings,omitempty"`
}

// Settings 是全局偏好。
type Settings struct {
	// FontFamily 为空时前端使用内置子集化等宽字体。
	FontFamily string `json:"fontFamily,omitempty"`
	FontSize   int    `json:"fontSize,omitempty"`
	// Scrollback 是终端回滚行数。
	Scrollback int `json:"scrollback,omitempty"`
}

// Store 负责 servers.json 的原子读写与版本迁移。
type Store struct {
	path string
}

// New 基于 servers.json 的绝对路径构造 Store。
func New(path string) *Store { return &Store{path: path} }

// Path 返回底层文件路径。
func (s *Store) Path() string { return s.path }

// Load 读取配置。文件不存在时返回一个空的、版本为当前版本的 File。
func (s *Store) Load() (*File, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return &File{Version: CurrentVersion}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: 读取 %s 失败: %w", s.path, err)
	}

	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("store: 解析 %s 失败（文件可能已损坏，可尝试 .bak）: %w", s.path, err)
	}
	if err := migrate(&f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Save 原子写入配置，并把上一版保留为 servers.json.bak。
// 写入过程为"临时文件 → fsync → rename"，避免掉电或崩溃留下半截文件。
func (s *Store) Save(f *File) error {
	if f == nil {
		return errors.New("store: File 为 nil")
	}
	if err := migrate(f); err != nil {
		return err
	}

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("store: 序列化失败: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	// 保留上一版，供手工回滚。
	if _, err := os.Stat(s.path); err == nil {
		if err := copyFile(s.path, s.path+".bak"); err != nil {
			return fmt.Errorf("store: 备份 servers.json.bak 失败: %w", err)
		}
	}

	tmp, err := os.CreateTemp(dir, "servers.json.tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	return nil
}

// migrate 是纯函数迁移链：把任意历史版本升到 CurrentVersion，不引入 ORM。
func migrate(f *File) error {
	if f.Version == 0 {
		// 早期文件没有 version 字段，视为 v1。
		f.Version = 1
	}
	if f.Version > CurrentVersion {
		return fmt.Errorf("store: 配置版本 %d 高于本程序支持的 %d，请升级 NanoSSH", f.Version, CurrentVersion)
	}
	// 后续新增版本时在此逐级 upgrade，例如：
	//   if f.Version == 1 { f.Version = 2; /* 字段改写 */ }
	f.Version = CurrentVersion
	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

// crockford 是 ULID 使用的 Crockford Base32 字母表。
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID 生成 ULID：48 位毫秒时间戳 + 80 位随机，26 字符 Crockford Base32。
// 字典序即时间序，且作为秘密在凭据管理器中的键时足够唯一。
func NewID() string {
	var raw [16]byte
	ts := uint64(time.Now().UnixMilli())
	raw[0] = byte(ts >> 40)
	raw[1] = byte(ts >> 32)
	raw[2] = byte(ts >> 24)
	raw[3] = byte(ts >> 16)
	raw[4] = byte(ts >> 8)
	raw[5] = byte(ts)
	if _, err := rand.Read(raw[6:]); err != nil {
		// crypto/rand 失败意味着系统熵源不可用，此时不应用继续运行。
		panic(fmt.Sprintf("store: 生成 ID 失败: %v", err))
	}

	var n big.Int
	n.SetBytes(raw[:])

	out := make([]byte, 26)
	radix := big.NewInt(32)
	rem := new(big.Int)
	for i := len(out) - 1; i >= 0; i-- {
		q := new(big.Int)
		q.QuoRem(&n, radix, rem)
		out[i] = crockford[rem.Int64()]
		n = *q
	}
	return string(out)
}
