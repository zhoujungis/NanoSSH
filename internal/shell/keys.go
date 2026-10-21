package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"nanossh/internal/sshcore"
	"nanossh/internal/store"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// GenerateKeyRequest 是"新建密钥"的输入。
type GenerateKeyRequest struct {
	Name string `json:"name"`
	// Algo 取 ed25519 / rsa / ecdsa，留空默认 ed25519。
	Algo string `json:"algo,omitempty"`
	// Comment 会写进 authorized_keys 行尾，留空则用 Name。
	Comment string `json:"comment,omitempty"`
	// Passphrase 非空时私钥受口令保护，并被存进凭据管理器，做到"只问一次"。
	Passphrase string `json:"passphrase,omitempty"`
}

// ImportKeyRequest 是"导入已有私钥"的输入。
type ImportKeyRequest struct {
	Name string `json:"name,omitempty"`
	// SourcePath 是源私钥文件路径，由前端的系统文件选择器给出。
	SourcePath string `json:"sourcePath"`
	// Passphrase 用于解开受口令保护的源密钥；没有口令的密钥留空即可。
	Passphrase string `json:"passphrase,omitempty"`
}

// ListKeys 返回全部已管理的密钥，并把 Path 修正为当前数据目录下的真实位置。
func (a *App) ListKeys() ([]sshcore.KeyRef, error) {
	f, err := a.load()
	if err != nil {
		return nil, err
	}
	out := make([]sshcore.KeyRef, 0, len(f.Keys))
	for _, k := range f.Keys {
		// 数据目录可能被整体搬走（或换了 NANOSSH_DATA_DIR），
		// 所以以 ID 推导的路径为准，而不是沿用记录里的旧绝对路径。
		k.Path = a.keyFilePath(k.ID)
		out = append(out, k)
	}
	return out, nil
}

// GenerateKey 生成一把新密钥并纳入管理。
func (a *App) GenerateKey(req GenerateKeyRequest) (sshcore.KeyRef, error) {
	if a.store == nil {
		return sshcore.KeyRef{}, errors.New("key: 数据目录尚未就绪")
	}

	alg := sshcore.Algo(req.Algo)
	if alg == "" {
		alg = sshcore.AlgoEd25519
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return sshcore.KeyRef{}, errors.New("key: 密钥名称不能为空")
	}
	comment := strings.TrimSpace(req.Comment)
	if comment == "" {
		comment = name
	}

	km, err := sshcore.GenerateKey(alg, comment, req.Passphrase)
	if err != nil {
		return sshcore.KeyRef{}, err
	}

	id := store.NewID()
	if err := a.writeKeyFile(id, km.PrivatePEM); err != nil {
		return sshcore.KeyRef{}, err
	}

	ref := sshcore.KeyRef{
		ID:          id,
		Name:        name,
		Path:        a.keyFilePath(id),
		Type:        string(alg),
		Fingerprint: km.Fingerprint,
		PublicKey:   km.PublicLine,
	}
	if err := a.appendKey(ref); err != nil {
		// 记录没写成功就不要在磁盘上留下私钥：孤儿私钥比丢失更难排查。
		_ = os.Remove(a.keyFilePath(id))
		return sshcore.KeyRef{}, err
	}

	if req.Passphrase != "" {
		if serr := a.secrets.Set(store.PassphraseTarget(id), req.Passphrase); serr != nil {
			// 口令没存下不算致命：首连时会再问一次，答对后自然补上。
			wruntime.LogWarning(a.ctx, "保存私钥口令失败: "+serr.Error())
		}
	}
	return ref, nil
}

// ImportKey 把一把已有私钥复制进 NanoSSH 的密钥目录。
//
// 先解析再落盘：解析成功同时证明了两件事——这确实是一把可用的私钥，
// 且我们拿到了准确的公钥与指纹（口令错时这里就会报错，不会留下坏记录）。
func (a *App) ImportKey(req ImportKeyRequest) (sshcore.KeyRef, error) {
	if a.store == nil {
		return sshcore.KeyRef{}, errors.New("key: 数据目录尚未就绪")
	}
	src := strings.TrimSpace(req.SourcePath)
	if src == "" {
		return sshcore.KeyRef{}, errors.New("key: 未选择私钥文件")
	}

	raw, err := os.ReadFile(src)
	if err != nil {
		return sshcore.KeyRef{}, fmt.Errorf("key: 读取 %s 失败: %w", src, err)
	}

	line, fingerprint, keyType, err := sshcore.PublicKeyFromPrivatePEM(raw, req.Passphrase)
	if err != nil {
		return sshcore.KeyRef{}, err
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	}

	id := store.NewID()
	if err := a.writeKeyFile(id, raw); err != nil {
		return sshcore.KeyRef{}, err
	}

	ref := sshcore.KeyRef{
		ID:          id,
		Name:        name,
		Path:        a.keyFilePath(id),
		Type:        string(sshcore.AlgoOf(keyType)),
		Fingerprint: fingerprint,
		PublicKey:   line,
	}
	if err := a.appendKey(ref); err != nil {
		_ = os.Remove(a.keyFilePath(id))
		return sshcore.KeyRef{}, err
	}

	if req.Passphrase != "" {
		if serr := a.secrets.Set(store.PassphraseTarget(id), req.Passphrase); serr != nil {
			wruntime.LogWarning(a.ctx, "保存私钥口令失败: "+serr.Error())
		}
	}
	return ref, nil
}

// DeleteKey 删除一把密钥，连同私钥文件与已保存的口令。
//
// 若仍有服务器引用它，直接拒绝——绝不"顺手"改掉那些服务器的认证方式：
// 暗改配置比报错难排查得多。
func (a *App) DeleteKey(id string) error {
	f, err := a.load()
	if err != nil {
		return err
	}

	var users []string
	for _, s := range f.Servers {
		if s.KeyID == id {
			users = append(users, s.Name)
		}
	}
	if len(users) > 0 {
		return fmt.Errorf("key: 该密钥正被 %s 使用，请先修改这些服务器的认证方式", strings.Join(users, "、"))
	}

	out := f.Keys[:0]
	found := false
	for _, k := range f.Keys {
		if k.ID == id {
			found = true
			continue
		}
		out = append(out, k)
	}
	if !found {
		return fmt.Errorf("key: 未找到 id=%s 的密钥", id)
	}
	f.Keys = out

	if err := a.store.Save(f); err != nil {
		return err
	}
	// 记录先落盘、文件后删除：顺序反了就可能出现"记录还在、文件没了"的死引用。
	if err := os.Remove(a.keyFilePath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("key: 删除私钥文件失败: %w", err)
	}
	return a.secrets.Delete(store.PassphraseTarget(id))
}

// PickKeyFile 弹出系统文件选择器，返回用户选中的私钥文件路径；空串表示取消。
func (a *App) PickKeyFile() (string, error) {
	return wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "选择 SSH 私钥",
		Filters: []wruntime.FileFilter{
			{DisplayName: "SSH 私钥 (*.pem;*.key;id_*)", Pattern: "*.pem;*.key;id_*"},
			{DisplayName: "全部文件", Pattern: "*.*"},
		},
	})
}

// keyFilePath 返回某密钥的私钥文件路径。
// 用 ULID 作文件名而不是用户取的名字：既杜绝重名与路径穿越，也让密钥可以随便改名。
func (a *App) keyFilePath(id string) string { return filepath.Join(a.paths.Keys, id) }

func (a *App) writeKeyFile(id string, pem []byte) error {
	if err := os.MkdirAll(a.paths.Keys, 0o700); err != nil {
		return fmt.Errorf("key: 创建密钥目录失败: %w", err)
	}
	if err := os.WriteFile(a.keyFilePath(id), pem, 0o600); err != nil {
		return fmt.Errorf("key: 写入私钥失败: %w", err)
	}
	return nil
}

func (a *App) findKey(id string) (sshcore.KeyRef, error) {
	f, err := a.load()
	if err != nil {
		return sshcore.KeyRef{}, err
	}
	for _, k := range f.Keys {
		if k.ID == id {
			return k, nil
		}
	}
	return sshcore.KeyRef{}, fmt.Errorf("key: 未找到 id=%s 的密钥", id)
}

func (a *App) appendKey(ref sshcore.KeyRef) error {
	f, err := a.load()
	if err != nil {
		return err
	}
	f.Keys = append(f.Keys, ref)
	return a.store.Save(f)
}
