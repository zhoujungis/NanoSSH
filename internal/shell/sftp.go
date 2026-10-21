package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nanossh/internal/sshcore"
	"nanossh/internal/store"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// SFTP 绑定层。
//
// 与终端同一套思路：sshcore 负责协议，本文件只做三件事——
// 把会话换成 SFTP 通道、把文件字节搬到对面、把进度变成事件。
// 进度事件沿用 emitFunc，因此整个传输链路能在无 WebView 的测试里跑通。

// SftpInfo 是文件面板首次打开时需要的信息。
type SftpInfo struct {
	// Home 是远端家目录的绝对路径，作为文件面板的起始位置。
	Home string `json:"home"`
}

// SftpListing 是一次列目录的结果。
type SftpListing struct {
	Path    string              `json:"path"`
	Parent  string              `json:"parent"`
	Entries []sshcore.FileEntry `json:"entries"`
}

// TransferStatus 是一次上传/下载的进度快照。
type TransferStatus struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	// Direction 取 upload（本地→远端）或 download（远端→本地）。
	Direction string `json:"direction"`
	// Name 是显示名，即文件名。
	Name string `json:"name"`
	// Remote 是远端路径。
	Remote string `json:"remote"`
	// Local 是本地路径。
	Local string `json:"local"`
	Total int64  `json:"total"`
	Done  int64  `json:"done"`
	// State 取 running / done / error / cancelled。
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// 传输的两个常量：块大小决定单次 syscall 与内存占用，事件节流决定 UI 刷新频率。
const (
	transferChunk   = 32 << 10               // 32 KiB
	transferEmitGap = 120 * time.Millisecond // 进度事件下限间隔
)

// SftpOpen 在会话上打开（或复用）SFTP 通道，并返回远端家目录。
//
// ★ SFTP 挂在同一条已认证的 SSH 连接上，只多开一个子系统通道：
//
//	不重新拨号、不重新握手，终端与文件面板因此可以共存于同一个会话标签。
func (a *App) SftpOpen(sessionID string) (SftpInfo, error) {
	s, err := a.sftpOf(sessionID)
	if err != nil {
		return SftpInfo{}, err
	}
	home, err := s.RealPath(".")
	if err != nil {
		return SftpInfo{}, err
	}
	return SftpInfo{Home: home}, nil
}

// SftpList 列出远端目录。dir 为空时等价于家目录。
func (a *App) SftpList(sessionID, dir string) (SftpListing, error) {
	s, err := a.sftpOf(sessionID)
	if err != nil {
		return SftpListing{}, err
	}
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	resolved, err := s.RealPath(dir)
	if err != nil {
		return SftpListing{}, err
	}
	entries, err := s.List(resolved)
	if err != nil {
		return SftpListing{}, err
	}
	return SftpListing{
		Path:    resolved,
		Parent:  sshcore.Parent(resolved),
		Entries: entries,
	}, nil
}

// SftpMkdir 在 parent 下建一个目录。
func (a *App) SftpMkdir(sessionID, parent, name string) error {
	name, err := cleanEntryName(name)
	if err != nil {
		return err
	}
	s, err := a.sftpOf(sessionID)
	if err != nil {
		return err
	}
	return s.Mkdir(sshcore.Join(parent, name))
}

// SftpRename 把 path 改名为同目录下的 newName。
func (a *App) SftpRename(sessionID, path, newName string) error {
	newName, err := cleanEntryName(newName)
	if err != nil {
		return err
	}
	s, err := a.sftpOf(sessionID)
	if err != nil {
		return err
	}
	return s.Rename(path, sshcore.Join(sshcore.Parent(path), newName))
}

// SftpDelete 删除一个远端条目。isDir 决定走 rmdir 还是 unlink。
//
// 只删空目录，不做递归删除：递归一旦路径算错就是不可逆的灾难。
func (a *App) SftpDelete(sessionID, path string, isDir bool) error {
	s, err := a.sftpOf(sessionID)
	if err != nil {
		return err
	}
	if isDir {
		return s.RemoveDir(path)
	}
	return s.Remove(path)
}

// SftpUpload 把若干本地文件上传到远端目录，立即返回进度描述符，传输在后台进行。
// 进度通过 sftp:transfer 事件推送。
func (a *App) SftpUpload(sessionID, remoteDir string, localPaths []string) ([]TransferStatus, error) {
	s, err := a.sftpOf(sessionID)
	if err != nil {
		return nil, err
	}
	if len(localPaths) == 0 {
		return nil, errors.New("sftp: 未选择要上传的文件")
	}

	out := make([]TransferStatus, 0, len(localPaths))
	for _, local := range localPaths {
		info, err := os.Stat(local)
		if err != nil {
			return nil, fmt.Errorf("sftp: 读取 %s 失败: %w", filepath.Base(local), err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("sftp: %s 是文件夹，暂不支持整目录上传", filepath.Base(local))
		}

		name := filepath.Base(local)
		remote := sshcore.Join(remoteDir, name)
		st := TransferStatus{
			ID:        store.NewID(),
			SessionID: sessionID,
			Direction: "upload",
			Name:      name,
			Remote:    remote,
			Local:     local,
			Total:     info.Size(),
			State:     "running",
		}
		out = append(out, st)

		local := local
		a.spawnTransfer(st, func(ctx context.Context, tick func(int64)) error {
			src, err := os.Open(local)
			if err != nil {
				return err
			}
			defer src.Close()

			dst, err := s.Create(remote)
			if err != nil {
				return err
			}
			defer dst.Close()

			if err := shuttle(ctx, src, dst, tick); err != nil {
				return err
			}
			return dst.Close()
		})
	}
	return out, nil
}

// SftpDownload 把一个远端文件下载到 localPath，立即返回进度描述符。
func (a *App) SftpDownload(sessionID, remotePath, localPath string) (TransferStatus, error) {
	s, err := a.sftpOf(sessionID)
	if err != nil {
		return TransferStatus{}, err
	}
	if strings.TrimSpace(localPath) == "" {
		return TransferStatus{}, errors.New("sftp: 未选择保存位置")
	}

	src, size, err := s.Open(remotePath)
	if err != nil {
		return TransferStatus{}, err
	}
	src.Close() // 立刻关掉：真正传输时再开一次，避免两处各持一个句柄

	st := TransferStatus{
		ID:        store.NewID(),
		SessionID: sessionID,
		Direction: "download",
		Name:      filepath.Base(strings.ReplaceAll(remotePath, "\\", "/")),
		Remote:    remotePath,
		Local:     localPath,
		Total:     size,
		State:     "running",
	}

	a.spawnTransfer(st, func(ctx context.Context, tick func(int64)) error {
		src, _, err := s.Open(remotePath)
		if err != nil {
			return err
		}
		defer src.Close()

		dst, err := os.Create(localPath)
		if err != nil {
			return err
		}
		defer dst.Close()

		if err := shuttle(ctx, src, dst, tick); err != nil {
			return err
		}
		return dst.Close()
	})
	return st, nil
}

// SftpCancelTransfer 取消一次进行中的传输。
func (a *App) SftpCancelTransfer(id string) error {
	a.mu.Lock()
	cancel, ok := a.transfers[id]
	a.mu.Unlock()
	if !ok {
		return nil // 已经结束，取消是幂等的
	}
	cancel()
	return nil
}

// PickUploadFiles 弹出系统文件选择器（多选），返回本地路径；空列表表示取消。
func (a *App) PickUploadFiles() ([]string, error) {
	paths, err := wruntime.OpenMultipleFilesDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "选择要上传的文件",
	})
	if err != nil {
		return nil, err
	}
	if paths == nil {
		return []string{}, nil
	}
	return paths, nil
}

// PickSavePath 弹出"另存为"对话框；空串表示取消。
func (a *App) PickSavePath(defaultName string) (string, error) {
	return wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:           "保存到",
		DefaultFilename: defaultName,
	})
}

// sftpOf 取会话上的 SFTP 通道，没有就地创建一次并缓存。
func (a *App) sftpOf(sessionID string) (*sshcore.SFTP, error) {
	sess, err := a.session(sessionID)
	if err != nil {
		return nil, err
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.sftp != nil {
		return sess.sftp, nil
	}
	s, err := sess.client.NewSFTP()
	if err != nil {
		return nil, err
	}
	sess.sftp = s
	return s, nil
}

// spawnTransfer 启动一次后台传输：注册可取消句柄 → 执行 → 终态事件。
//
// 与 pumpSession 同理，进度全部经 a.emit 出去，测试里可整体换掉。
func (a *App) spawnTransfer(st TransferStatus, work func(ctx context.Context, tick func(int64)) error) {
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	a.transfers[st.ID] = cancel
	a.mu.Unlock()

	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.transfers, st.ID)
			a.mu.Unlock()
		}()

		cur := st
		last := time.Now()
		tick := func(n int64) {
			cur.Done += n
			if time.Since(last) >= transferEmitGap {
				last = time.Now()
				a.emit(a.ctx, "sftp:transfer", cur)
			}
		}

		err := work(ctx, tick)
		switch {
		case err == nil:
			cur.State = "done"
		case errors.Is(err, context.Canceled):
			cur.State = "cancelled"
		default:
			cur.State = "error"
			cur.Error = strings.TrimPrefix(err.Error(), "sftp: ")
		}
		a.emit(a.ctx, "sftp:transfer", cur)
	}()
}

// shuttle 把 r 搬到 w，按块推进并报告进度；ctx 一取消立刻停手。
func shuttle(ctx context.Context, r io.Reader, w io.Writer, tick func(int64)) error {
	buf := make([]byte, transferChunk)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			tick(int64(n))
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// cleanEntryName 校验前端给的新名字：不许带路径分隔符或占位符。
func cleanEntryName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", errors.New("sftp: 名称不能为空")
	case name == "." || name == "..":
		return "", errors.New("sftp: 名称不合法")
	case strings.ContainsAny(name, `/\`):
		return "", errors.New("sftp: 名称不能包含路径分隔符")
	}
	return name, nil
}
