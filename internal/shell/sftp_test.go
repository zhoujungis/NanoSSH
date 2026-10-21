package shell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nanossh/internal/sshcore"
	"nanossh/internal/testsshd"
)

// startConnOnly 只建立连接、不起 shell：文件面板本就不该依赖终端已经跑起来。
func startConnOnly(t *testing.T) (*App, *testsshd.Server, string, *eventCollector) {
	t.Helper()
	srv := newRelay(t, "tester", "s3cret")
	a, c := newTestApp(t)
	host, port := srv.HostPort()

	rec, err := a.SaveServer(passwordServer(host, port))
	if err != nil {
		t.Fatalf("保存服务器失败: %v", err)
	}
	conn, err := a.Connect(ConnectRequest{ServerID: rec.ID, Password: "s3cret"})
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	t.Cleanup(func() { _ = a.CloseSession(conn.SessionID) })
	return a, srv, conn.SessionID, c
}

// TestSftpReusesSameConnection 钉死 P3 的核心架构约束：
// 文件通道必须挂在同一条已认证连接上，而不是再拨一次号、再握一次手。
func TestSftpReusesSameConnection(t *testing.T) {
	a, srv, sid, c := startPair(t)

	if _, err := a.SftpOpen(sid); err != nil {
		t.Fatalf("打开 SFTP 失败: %v", err)
	}
	// 再开一次必须命中缓存，不能多起一个子系统。
	if _, err := a.SftpOpen(sid); err != nil {
		t.Fatalf("再次打开 SFTP 失败: %v", err)
	}

	if got := srv.ConnCount(); got != 1 {
		t.Fatalf("SFTP 应复用同一条连接，实际握手了 %d 条", got)
	}
	if got := srv.SFTPCount(); got != 1 {
		t.Fatalf("SFTP 通道应只建立一次，实际 %d 次", got)
	}

	// 终端不受影响：两条通道并存是这套设计的前提。
	if err := a.WriteStdin(sid, "echo sftp-parallel\n"); err != nil {
		t.Fatalf("终端写入失败: %v", err)
	}
	waitFor(t, "终端与 SFTP 并存", func() bool {
		return strings.Contains(c.text(sid), "sftp-parallel")
	})
}

// TestSftpBrowseMkdirRenameDelete 覆盖目录浏览与条目管理的完整闭环。
func TestSftpBrowseMkdirRenameDelete(t *testing.T) {
	a, _, sid, _ := startConnOnly(t)

	root := t.TempDir()
	remote := filepath.Join(root, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatalf("准备远端目录失败: %v", err)
	}

	info, err := a.SftpOpen(sid)
	if err != nil {
		t.Fatalf("打开 SFTP 失败: %v", err)
	}
	if info.Home == "" {
		t.Fatal("SftpOpen 应返回远端家目录")
	}

	// 1) 新建目录 → 列表里能看到，且被标记为目录。
	if err := a.SftpMkdir(sid, remote, "docs"); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	list, err := a.SftpList(sid, remote)
	if err != nil {
		t.Fatalf("列目录失败: %v", err)
	}
	entry := findEntry(list, "docs")
	if entry == nil {
		t.Fatalf("列表里应有 docs，实际 %v", names(list))
	}
	if !entry.IsDir {
		t.Fatal("docs 应被识别为目录")
	}

	// 2) 重命名 → 旧名消失、新名出现。
	if err := a.SftpRename(sid, entry.Path, "notes"); err != nil {
		t.Fatalf("重命名失败: %v", err)
	}
	list, err = a.SftpList(sid, remote)
	if err != nil {
		t.Fatalf("列目录失败: %v", err)
	}
	if findEntry(list, "docs") != nil {
		t.Fatal("重命名后 docs 不应还在")
	}
	renamed := findEntry(list, "notes")
	if renamed == nil {
		t.Fatalf("重命名后应有 notes，实际 %v", names(list))
	}

	// 3) 删除目录 → 列表清空。
	if err := a.SftpDelete(sid, renamed.Path, true); err != nil {
		t.Fatalf("删除目录失败: %v", err)
	}
	list, err = a.SftpList(sid, remote)
	if err != nil {
		t.Fatalf("列目录失败: %v", err)
	}
	if len(list.Entries) != 0 {
		t.Fatalf("删除后列表应为空，实际 %v", names(list))
	}

	// 4) 非法名字必须被挡在门外：带分隔符的名字一旦拼进路径就是越权读写。
	if err := a.SftpMkdir(sid, remote, "../escape"); err == nil {
		t.Fatal("带路径分隔符的名称应被拒绝")
	}
	if err := a.SftpRename(sid, remote, ".."); err == nil {
		t.Fatal("重命名为 .. 应被拒绝")
	}
}

// TestSftpUploadDownloadRoundTrip 覆盖传输闭环：上传 → 落盘正确 → 下载回来逐字节一致，
// 同时校验进度事件的终态与字节数。
func TestSftpUploadDownloadRoundTrip(t *testing.T) {
	a, _, sid, c := startConnOnly(t)

	root := t.TempDir()
	local := filepath.Join(root, "local")
	remote := filepath.Join(root, "remote")
	for _, d := range []string{local, remote} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("准备目录失败: %v", err)
		}
	}

	// 大小跨过 32 KiB 的搬运块，确保走了多轮 chunk 而不是一次性 memcpy。
	const size = 200_000
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i*7 + i/251)
	}
	src := filepath.Join(local, "payload.bin")
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatalf("写入源文件失败: %v", err)
	}

	// 上传
	ups, err := a.SftpUpload(sid, remote, []string{src})
	if err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	if len(ups) != 1 {
		t.Fatalf("应产生 1 个传输，实际 %d 个", len(ups))
	}
	up := ups[0]
	if up.Total != size {
		t.Fatalf("上传总量应为 %d，实际 %d", size, up.Total)
	}
	waitFor(t, "上传完成", func() bool {
		st, ok := c.transferFinal(up.ID)
		return ok && st.State == "done"
	})
	final, _ := c.transferFinal(up.ID)
	if final.Done != size {
		t.Fatalf("上传字节数应为 %d，实际 %d", size, final.Done)
	}

	// 远端落盘内容必须逐字节正确。
	got, err := os.ReadFile(filepath.Join(remote, "payload.bin"))
	if err != nil {
		t.Fatalf("远端文件未生成: %v", err)
	}
	if len(got) != len(payload) {
		t.Fatalf("远端文件大小应为 %d，实际 %d", len(payload), len(got))
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Fatalf("远端文件第 %d 字节不一致", i)
		}
	}

	// 下载
	dst := filepath.Join(local, "roundtrip.bin")
	dl, err := a.SftpDownload(sid, filepath.Join(remote, "payload.bin"), dst)
	if err != nil {
		t.Fatalf("下载失败: %v", err)
	}
	waitFor(t, "下载完成", func() bool {
		st, ok := c.transferFinal(dl.ID)
		return ok && st.State == "done"
	})

	back, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("下载文件未生成: %v", err)
	}
	if len(back) != len(payload) {
		t.Fatalf("下载文件大小应为 %d，实际 %d", len(payload), len(back))
	}
	for i := range payload {
		if back[i] != payload[i] {
			t.Fatalf("下载文件第 %d 字节不一致", i)
		}
	}
}

// TestTransferCancel 验证取消链路：前端点取消 → 中途停下 → 终态是 cancelled 而不是 done。
func TestTransferCancel(t *testing.T) {
	a, c := newTestApp(t)

	started := make(chan struct{})
	st := TransferStatus{ID: "tx-cancel", Direction: "download", State: "running"}
	a.spawnTransfer(st, func(ctx context.Context, tick func(int64)) error {
		close(started)
		tick(1)
		<-ctx.Done()
		return ctx.Err()
	})

	<-started
	if err := a.SftpCancelTransfer("tx-cancel"); err != nil {
		t.Fatalf("取消传输失败: %v", err)
	}

	waitFor(t, "传输进入 cancelled 终态", func() bool {
		got, ok := c.transferFinal("tx-cancel")
		return ok && got.State == "cancelled"
	})

	// 取消是幂等的：已经结束的传输再取消一次不该报错。
	if err := a.SftpCancelTransfer("tx-cancel"); err != nil {
		t.Fatalf("重复取消不应报错: %v", err)
	}
}

// TestSftpListingParent 校验列目录返回的父路径，它是文件面板"上一级"按钮的唯一依据。
//
// 这里做行为断言而不是比字符串：真实远端给的是 POSIX 路径，进程内靶机跑在 Windows 上，
// 两者路径写法不同，但"上一级能列出当前目录"这件事必须一样成立。
func TestSftpListingParent(t *testing.T) {
	a, _, sid, _ := startConnOnly(t)

	root := t.TempDir()
	inner := filepath.Join(root, "remote", "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("准备远端目录失败: %v", err)
	}

	list, err := a.SftpList(sid, inner)
	if err != nil {
		t.Fatalf("列目录失败: %v", err)
	}
	if !strings.HasSuffix(strings.ReplaceAll(list.Path, `\`, "/"), "/inner") {
		t.Fatalf("当前路径应以 /inner 结尾，实际 %q", list.Path)
	}

	up, err := a.SftpList(sid, list.Parent)
	if err != nil {
		t.Fatalf("列父目录失败: %v", err)
	}
	if findEntry(up, "inner") == nil {
		t.Fatalf("父目录里应有 inner，实际 %v", names(up))
	}
}

// findEntry 在列目录结果里按名字找条目。
func findEntry(list SftpListing, name string) *sshcore.FileEntry {
	for i := range list.Entries {
		if list.Entries[i].Name == name {
			return &list.Entries[i]
		}
	}
	return nil
}

func names(list SftpListing) []string {
	out := make([]string, 0, len(list.Entries))
	for _, e := range list.Entries {
		out = append(out, e.Name)
	}
	return out
}
