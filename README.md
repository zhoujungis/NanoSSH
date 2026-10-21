# NanoSSH

> 一个刻意做窄的 Windows SSH 客户端。只做 SSH，但做到极致轻。
>
> English version: [README.en.md](README.en.md)

市面上的 SSH 客户端（Termius、SecureCRT、Xshell、WindTerm）都在堆功能——云同步、K8s、AI、插件市场。NanoSSH 反过来：**功能面就是五件事，不多一件**，打开即见服务器列表，点一下立刻连上。没有落地页、没有账号系统、没有遥测。

- **轻**：单文件可执行，x64 约 13.3 MB，无运行时依赖
- **快**：二进制 WebSocket IPC + ack 反压，`cat` 大文件不卡界面
- **净**：全部数据落在本地一个目录 + Windows 凭据管理器，零云依赖

---

## 功能

| 功能 | 说明 |
|---|---|
| **服务器** | 带 SVG 国旗（不用 emoji，Windows 上 emoji 国旗不渲染）的服务器列表，点一下直连 |
| **终端** | xterm.js 6 + WebGL 渲染，256 色 / CJK 宽字符 / 粘贴转义、`htop`/`vim` 全屏不错乱 |
| **SFTP** | 终端 pane 内「终端 / 文件」子切换：目录浏览、上传下载（进度 / 取消）、新建 / 重命名 / 删除 |
| **端口转发** | pane 内「转发」子切换：本地转发（`-L`），运行中 / 出错 / 停止三态，会话关闭自动清理 |
| **SSH Key** | 密钥列表 / 生成（ed25519）/ 导入 / 复制公钥 / 删除，指纹与 `ssh-keygen -lf` 一致 |

### 系统集成

- **Win11 Mica** 系统背景（build ≥ 22621 自动启用，老系统自动降级为不透明）
- **深浅色跟随系统**（运行时热切换）
- **单实例**：二次启动不再开新窗，直接唤起已有窗口
- **系统托盘**：关窗驻留托盘、会话不断，双击图标回到窗口
- 无边框自绘标题栏、PerMonitorV2 DPI、多显示器缩放去抖

### 认证与安全

- 密码 / 私钥 / ssh-agent（Windows 命名管道 `\\.\pipe\openssh-ssh-agent`）
- 密码与私钥口令存 **Windows 凭据管理器**（DPAPI，绑定账户，可在系统里撤销），不落明文
- **known_hosts TOFU**：首次连接弹一次指纹卡（仅本次 / 信任并保存 / 取消）；**指纹变化硬阻断**，显示新旧差异，没有"总是忽略"
- 私钥保持标准 OpenSSH 格式，可与 `~/.ssh` 互拷；known_hosts 与系统 `known_hosts` 互通

### 明确不做

云账号、注册、同步、AI、插件市场、工作区、遥测、自动更新、分屏、X11 转发。功能窄是产品主张，不是排期。

---

## 系统要求

- Windows 10 / 11（x64）
- Mica 背景需要 Windows 11 22H2（build 22621）及以上，其余系统自动降级
- 无需安装任何运行时

---

## 快速开始

1. 取 `build\bin\NanoSSH.exe`，双击运行（绿色免安装）
2. 「新建服务器」填名称 / 主机 / 端口 / 用户，选认证方式
3. 点击服务器 →（首次会弹主机指纹卡）→ 连接 → 开始工作
4. 终端 pane 顶部的「终端 / 文件 / 转发」切换会话内能力

---

## 从源码构建

```powershell
.\build.ps1
```

脚本职责（把体积与架构从口头约定变成可执行断言）：

| 步骤 | 说明 |
|---|---|
| 钉死架构 | 强制 `GOARCH=amd64`（只影响本进程，不动全局 go env），并断言产物 PE 头是 `0x8664` |
| 漏洞扫描 | PATH 上有 `govulncheck` 则先跑（`-SkipVuln` 跳过） |
| 构建 | `wails build -ldflags "-s -w"`，前端构建由 `wails.json` 驱动 |
| 体积守卫 | 超过阈值（默认 14 MB，`-MaxSizeMB` 可调）直接失败——定位是"极致轻"，先查依赖增量再提阈值 |
| 签名钩子 | 提供 `-SignThumbprint <SHA1>` 或 `-SignPfx <pfx>` 才签名，否则跳过 |

常用参数：

```powershell
.\build.ps1 -MaxSizeMB 14                      # 自定义体积上限
.\build.ps1 -SkipVuln                          # 跳过漏洞扫描
.\build.ps1 -SignThumbprint <证书SHA1指纹>      # Authenticode 签名 + 时间戳
```

### 前置条件

- Go 工具链（任意 32/64 位发行版均可，脚本自己钉死 amd64）
- [Wails v2 CLI](https://wails.io)：`go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- Node.js（前端构建）

### CI 与发布

仓库自带 GitHub Actions（`.github/workflows/ci.yml`）：

- **push / PR**：`go vet`、`go test`、`tsc`、`govulncheck` 四道检查
- **推送 `v*` tag 或手动触发**：额外打包，产出 `NanoSSH-<版本>-windows-x64.zip`（绿色版）与 `NanoSSH-<版本>-x64.msi`（安装包：装到 Program Files，带开始菜单 / 桌面快捷方式）
- **推送 `v*` tag**：产物自动挂到 GitHub Release

本地打 MSI（需要 WiX Toolset 3.x，脚本找不到时会尝试 `choco install wixtoolset`）：

```powershell
.\build\windows\make-msi.ps1 -Version 0.1.0
```

---

## 项目结构

```
NanoSSH/
├─ main.go                 # 外壳装配：窗口选项、生命周期、单实例（不含业务）
├─ wails.json              # Wails 构建配置
├─ build.ps1               # 构建脚本：amd64 断言 / 体积守卫 / 漏洞扫描 / 签名钩子
│
├─ internal/
│  ├─ shell/               # 外壳业务层（Wails 绑定门面）
│  │   ├─ app.go               # App 门面：服务器 CRUD、连接、主机密钥/口令询问
│  │   ├─ sessions.go          # 终端会话：PTY、合并 stdout/stderr、字节反压
│  │   ├─ sftp.go              # SFTP 会话（复用同一 SSH 连接，懒创建）
│  │   ├─ forward.go           # 端口转发规则与生命周期
│  │   ├─ keys.go              # 密钥管理（生成/导入/公钥/指纹）
│  │   ├─ secrets.go           # Windows 凭据管理器封装
│  │   ├─ tray.go              # 系统托盘（手写 Shell_NotifyIconW，零第三方依赖）
│  │   └─ windowopts.go        # 系统能力探测（Mica 支持判定）
│  │
│  ├─ sshcore/             # ★ SSH 内核：零 UI 依赖，可被任意外壳复用
│  │   ├─ client.go            # 拨号、认证装配、keepalive
│  │   ├─ session.go           # PTY / WindowChange / 字节流
│  │   ├─ sftp.go              # 浏览与传输（并发请求流水线）
│  │   ├─ forward.go           # direct-tcpip 本地转发
│  │   ├─ hostkey.go           # knownhosts + TOFU 回调协议
│  │   ├─ auth.go / keys.go    # 认证与密钥解析、生成
│  │   └─ agent_windows.go     # ssh-agent（命名管道）
│  │
│  ├─ store/               # 本地落盘：servers.json 原子读写、路径解析
│  └─ testsshd/            # 集成测试用的进程内 sshd
│
├─ cmd/sshprobe/           # 无 UI 端到端探针（exec / shell / serve）
├─ frontend/               # 前端（无框架：Vanilla TS + xterm.js 6）
│  ├─ src/
│  │   ├─ components/          # TitleBar / ServerList / 各面板与对话框
│  │   ├─ views/               # 顶层视图装配
│  │   ├─ ipc/                 # api（绑定封装）/ transport（反压）/ decode.worker / mock
│  │   ├─ term/                # xterm.js 封装
│  │   ├─ styles/              # tokens.css（设计令牌）+ app.css
│  │   ├─ state/ ui/           # 信号状态与 DOM 构造器
│  │   └─ main.ts              # 入口
│  └─ wailsjs/               # wails build 生成的绑定（生成物）
└─ build/                  # Wails 资源（图标 / manifest）与产物 build/bin/
```

---

## 架构

**核心原则：把风险放在可替换的那一层。** `internal/sshcore` 不含任何 UI 依赖，五个功能全在里面；外壳只负责渲染与字节搬运。若将来外壳要换（如裸 WebView2），只动 `internal/shell` 与前端 `ipc/transport.ts`，内核一行不改。

### 终端数据流（性能的关键）

```
远端 sshd → x/crypto/ssh []byte → 合并 ~16KB / 8ms flush → IPC → Worker 解 base64
→ 零拷贝交给主线程 → xterm.write(bytes, cb) → cb 回 ack → 后端才读下一段
```

**ack 反压是低延迟的真正来源**：xterm 的 write 回调是"渲染完成"，后端收到 ack 才继续读，此时 SSH channel 窗口自然把压力传回远端 shell。全程 `[]byte` 直通，后端不做行切分、不做 UTF-8 解码（汉字可能跨 chunk）。

### 会话复用

SFTP 与端口转发都不重新握手——在同一条已认证 SSH 连接上多开子通道（`sftp` 子系统 / `direct-tcpip`），面板懒创建（首次切换才开通道），会话关闭时统一清理规则并释放端口。

---

## 数据与配置

全部数据在 `%APPDATA%\NanoSSH\`（可用环境变量 `NANOSSH_DATA_DIR` 覆盖，供便携运行与测试）：

| 路径 | 内容 | 备份 |
|---|---|---|
| `servers.json` | 服务器清单（纯 JSON，**不含秘密**） | ✅ 复制它即备份配置 |
| `known_hosts` | 标准 OpenSSH 格式主机指纹 | 可与 `~/.ssh/known_hosts` 互拷 |
| `keys\` | 标准 OpenSSH 私钥文件 | 有 passphrase 的保持加密 |
| `logs\` | 运行日志（秘密脱敏） | 可删 |

密码与私钥口令存在 **Windows 凭据管理器**（不在上述目录），绑定 Windows 账户——换机无法解密，这是正确行为：复制 `servers.json` + `keys\` 到新机器即恢复服务器与密钥，密码重新输一次。

---

## 开发

前端开发态自带 mock 后端，普通浏览器即可预览全部界面（含假数据）：

```powershell
cd frontend
npm install
npm run dev        # http://localhost:5173
```

### 验证命令

```powershell
go vet ./...                    # 静态检查
go test ./...                   # 后端集成测试（终端闭环 / 反压 / 转发 / SFTP）
cd frontend
npx tsc --noEmit                # 前端类型检查
npm run build                   # 前端构建
.\build.ps1                     # 全量构建 + 体积守卫
```

### 无 UI 探针

```powershell
go run ./cmd/sshprobe exec  -host H -user U -password P -cmd "uname -a"
go run ./cmd/sshprobe shell -host H -user U -password P
go run ./cmd/sshprobe serve                            # 起进程内测试 sshd
```

所有内核能力都必须能不打开窗口就验证——"UI 能点通"会掩盖底层协议错误。

---

## 许可

暂未指定许可证。
