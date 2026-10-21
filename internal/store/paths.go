// Package store 负责 NanoSSH 的全部本地落盘：服务器清单、known_hosts、私钥目录。
// 设计取舍：用 JSON 而非 SQLite——数据量是"几十条服务器"级别，
// 而 modernc.org/sqlite 会给二进制 +4~6MB，直接打爆体积预算；JSON 也更便于 diff 与手工修复。
package store

import (
	"os"
	"path/filepath"
)

// Paths 汇总应用的全部落盘位置。
type Paths struct {
	// Dir 是数据根目录：%APPDATA%\NanoSSH。
	Dir string
	// Servers 是 servers.json——唯一需要备份的文件，纯 JSON、不含秘密。
	Servers string
	// KnownHosts 是标准 OpenSSH 格式，可直接与系统 ~/.ssh/known_hosts 互拷。
	KnownHosts string
	// Keys 是存放标准 OpenSSH 私钥文件的目录。
	Keys string
	// Logs 是日志目录。
	Logs string
}

// Resolve 计算默认路径。环境变量 NANOSSH_DATA_DIR 可覆盖根目录（供测试与便携运行使用）。
func Resolve() (Paths, error) {
	root, err := DataDir()
	if err != nil {
		return Paths{}, err
	}
	return Paths{
		Dir:        root,
		Servers:    filepath.Join(root, "servers.json"),
		KnownHosts: filepath.Join(root, "known_hosts"),
		Keys:       filepath.Join(root, "keys"),
		Logs:       filepath.Join(root, "logs"),
	}, nil
}

// DataDir 返回数据根目录。
func DataDir() (string, error) {
	if v := os.Getenv("NANOSSH_DATA_DIR"); v != "" {
		return v, nil
	}
	base := os.Getenv("APPDATA")
	if base == "" {
		// 非 Windows 或环境异常时回退到系统配置目录。
		var err error
		base, err = os.UserConfigDir()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(base, "NanoSSH"), nil
}

// Ensure 创建全部目录。
func (p Paths) Ensure() error {
	for _, dir := range []string{p.Dir, p.Keys, p.Logs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}
