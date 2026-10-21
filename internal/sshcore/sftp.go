package sshcore

import (
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/pkg/sftp"
)

// FileEntry 是远端文件系统里的一个条目，字段与前端文件面板一一对应。
type FileEntry struct {
	Name string `json:"name"`
	// Path 是绝对路径（远端一律用 / 分隔），可直接回传给后续操作。
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime int64  `json:"modTime"`
	IsDir   bool   `json:"isDir"`
	// IsSymlink 为 true 时前端给出"链接"标记；链接指向的目标路径不解析——
	// 每条目一次 stat 会让大目录的列出变成 N 次往返，不值得。
	IsSymlink bool `json:"isSymlink"`
}

// SFTP 是挂在一条已有 SSH 连接上的 SFTP 会话。
//
// ★ 它复用 Client 的同一条 TCP/SSH 连接：不再拨号、不再握手、不再认证，
// 只是多开一个 "sftp" 子系统通道。这正是终端与文件面板共用一个会话标签的前提。
type SFTP struct {
	client *sftp.Client
}

// NewSFTP 在已有连接上打开 sftp 子系统。
func (c *Client) NewSFTP() (*SFTP, error) {
	cl, err := sftp.NewClient(c.ssh)
	if err != nil {
		return nil, fmt.Errorf("sshcore: 打开 SFTP 失败: %w", err)
	}
	return &SFTP{client: cl}, nil
}

// Close 关闭 SFTP 通道（不影响底层 SSH 连接与终端）。
func (s *SFTP) Close() error { return s.client.Close() }

// RealPath 解析路径为远端的绝对路径（服务端视角），"." 表示远端家目录。
func (s *SFTP) RealPath(p string) (string, error) {
	if p == "" {
		p = "."
	}
	return s.client.RealPath(p)
}

// Parent 返回路径的上一级；根目录的上一级仍是根目录。
//
// 远端路径按 POSIX 语义处理（真实 SSH 服务端几乎都是 Unix），但进程内测试靶机
// 跑在 Windows 上、给出的是反斜杠路径。为了同一套逻辑在两种靶机上都成立，
// 这里同时认两种分隔符——Linux 远端的文件名里出现反斜杠的概率可以忽略。
func Parent(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

// Join 在远端路径上拼接名字。远端一律 POSIX 语义，故用 path 而非 filepath。
func Join(dir, name string) string { return path.Join(dir, name) }

// List 列出目录内容，目录优先、再按名字排序，让前端不必再排一次。
func (s *SFTP) List(dir string) ([]FileEntry, error) {
	infos, err := s.client.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("sshcore: 列出 %s 失败: %w", dir, err)
	}

	entries := make([]FileEntry, 0, len(infos))
	for _, fi := range infos {
		e := FileEntry{
			Name:      fi.Name(),
			Path:      path.Join(dir, fi.Name()),
			Size:      fi.Size(),
			Mode:      fi.Mode().String(),
			ModTime:   fi.ModTime().UnixMilli(),
			IsDir:     fi.IsDir(),
			IsSymlink: fi.Mode()&os.ModeSymlink != 0,
		}
		// 符号链接本身不是目录，但可能指向目录；不 follow 的话用户进不去。
		// 只对链接条目补一次 stat——普通目录里链接通常是少数。
		if e.IsSymlink && !e.IsDir {
			if target, err := s.client.Stat(e.Path); err == nil && target.IsDir() {
				e.IsDir = true
			}
		}
		entries = append(entries, e)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

// Stat 取单个条目的信息。
func (s *SFTP) Stat(p string) (FileEntry, error) {
	fi, err := s.client.Stat(p)
	if err != nil {
		return FileEntry{}, fmt.Errorf("sshcore: 读取 %s 失败: %w", p, err)
	}
	return FileEntry{
		Name:      fi.Name(),
		Path:      p,
		Size:      fi.Size(),
		Mode:      fi.Mode().String(),
		ModTime:   fi.ModTime().UnixMilli(),
		IsDir:     fi.IsDir(),
		IsSymlink: fi.Mode()&os.ModeSymlink != 0,
	}, nil
}

// Open 打开远端文件供读取，返回句柄与文件大小。
func (s *SFTP) Open(p string) (io.ReadCloser, int64, error) {
	f, err := s.client.Open(p)
	if err != nil {
		return nil, 0, fmt.Errorf("sshcore: 打开 %s 失败: %w", p, err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("sshcore: 读取 %s 属性失败: %w", p, err)
	}
	return f, fi.Size(), nil
}

// Create 创建（或截断）远端文件供写入。
func (s *SFTP) Create(p string) (io.WriteCloser, error) {
	f, err := s.client.Create(p)
	if err != nil {
		return nil, fmt.Errorf("sshcore: 创建 %s 失败: %w", p, err)
	}
	return f, nil
}

// Mkdir 创建目录。
func (s *SFTP) Mkdir(p string) error {
	if err := s.client.Mkdir(p); err != nil {
		return fmt.Errorf("sshcore: 创建目录 %s 失败: %w", p, err)
	}
	return nil
}

// Remove 删除文件（或符号链接）。
func (s *SFTP) Remove(p string) error {
	if err := s.client.Remove(p); err != nil {
		return fmt.Errorf("sshcore: 删除 %s 失败: %w", p, err)
	}
	return nil
}

// RemoveDir 删除空目录。非空目录会由服务端拒绝并报错，不做递归删除——
// 递归删除一旦路径算错就是不可逆的灾难，宁可多点几下。
func (s *SFTP) RemoveDir(p string) error {
	if err := s.client.RemoveDirectory(p); err != nil {
		return fmt.Errorf("sshcore: 删除目录 %s 失败: %w", p, err)
	}
	return nil
}

// Rename 重命名或移动。
func (s *SFTP) Rename(oldpath, newpath string) error {
	if err := s.client.PosixRename(oldpath, newpath); err != nil {
		// 服务端未必实现了 posix-rename@openssh.com，退回标准 rename。
		if err2 := s.client.Rename(oldpath, newpath); err2 != nil {
			return fmt.Errorf("sshcore: 重命名 %s 失败: %w", oldpath, err2)
		}
	}
	return nil
}
