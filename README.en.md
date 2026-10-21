# NanoSSH

> A deliberately narrow SSH client for Windows. Only SSH — done extremely light.
>
> 中文版：[README.md](README.md)

Most SSH clients (Termius, SecureCRT, Xshell, WindTerm) keep piling on features — cloud sync, K8s, AI, plugin marketplaces. NanoSSH goes the other way: **the feature set is exactly five things, not one more**. Open it, see your servers, click one, you're in. No landing page, no account system, no telemetry.

- **Light**: a single portable executable, ~13.3 MB (x64), zero runtime dependencies
- **Fast**: binary WebSocket IPC with ack backpressure — `cat` a huge file without freezing the UI
- **Clean**: everything lives in one local folder plus Windows Credential Manager; zero cloud involvement

---

## Features

| Feature | Description |
|---|---|
| **Servers** | Server list with SVG country flags (not emoji — Windows doesn't render flag emoji), one-click connect |
| **Terminal** | xterm.js 6 + WebGL renderer, 256 colors / CJK wide chars / bracketed paste; `htop` and `vim` render correctly |
| **SFTP** | A "Terminal / Files" sub-tab inside the terminal pane: browse, upload/download (progress / cancel), create / rename / delete |
| **Port Forward** | A "Forwards" sub-tab in the same pane: local forwarding (`-L`) with running / error / stopped states; rules cleaned up when the session closes |
| **SSH Key** | Key list / generate (ed25519) / import / copy public key / delete; fingerprints match `ssh-keygen -lf` |

### System integration

- **Windows 11 Mica** background (enabled automatically on build ≥ 22621, graceful fallback to opaque elsewhere)
- **Light/dark theme follows the system** (live switching)
- **Single instance**: launching again brings the existing window to the front instead of opening a new one
- **System tray**: closing the window keeps the app (and your SSH sessions) alive in the tray; double-click to return
- Frameless custom title bar, PerMonitorV2 DPI awareness, resize debouncing for multi-monitor setups

### Authentication & security

- Password / private key / ssh-agent (via the Windows named pipe `\\.\pipe\openssh-ssh-agent`)
- Passwords and key passphrases are stored in **Windows Credential Manager** (DPAPI, bound to your account, revocable in the OS) — never on disk in plaintext
- **known_hosts TOFU**: first connection shows a one-time fingerprint card (this time only / trust & save / cancel); **fingerprint changes hard-block the connection**, showing old vs new — there is no "always ignore"
- Private keys stay in standard OpenSSH format and interoperate with `~/.ssh`; `known_hosts` is the standard OpenSSH format too

### Explicitly out of scope

Cloud accounts, sign-up, sync, AI, plugin marketplaces, workspaces, telemetry, auto-update, split panes, X11 forwarding. The narrow scope is the product thesis, not a backlog.

---

## System requirements

- Windows 10 / 11 (x64)
- Mica background requires Windows 11 22H2 (build 22621+) — older systems fall back to opaque surfaces
- No runtime installation needed

---

## Quick start

1. Take `build\bin\NanoSSH.exe` and run it (portable, no install)
2. "New server": fill in name / host / port / user, pick an auth method
3. Click the server → (first time shows the host-key fingerprint card) → connected
4. Use the "Terminal / Files / Forwards" sub-tabs at the top of the terminal pane for in-session capabilities

---

## Building from source

```powershell
.\build.ps1
```

What the script enforces (turning "small binary, right architecture" from words into assertions):

| Step | Description |
|---|---|
| Pin the architecture | Forces `GOARCH=amd64` (process-local, doesn't touch global `go env`) and asserts the PE header is `0x8664` |
| Vulnerability scan | Runs `govulncheck` if it's on PATH (`-SkipVuln` to skip) |
| Build | `wails build -ldflags "-s -w"`; the frontend build is driven by `wails.json` |
| Size guard | Fails the build if the binary exceeds the threshold (default 14 MB, tunable via `-MaxSizeMB`) — "extremely light" is the product; check dependency growth before raising it |
| Signing hook | Signs only when `-SignThumbprint <SHA1>` or `-SignPfx <pfx>` is provided; otherwise skipped |

Common invocations:

```powershell
.\build.ps1 -MaxSizeMB 14                    # custom size ceiling
.\build.ps1 -SkipVuln                        # skip vulnerability scan
.\build.ps1 -SignThumbprint <cert SHA1>      # Authenticode sign + timestamp
```

### Prerequisites

- A Go toolchain (any 32/64-bit distribution works — the script pins amd64 itself)
- [Wails v2 CLI](https://wails.io): `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- Node.js (frontend build)

### CI & releases

The repository ships with GitHub Actions (`.github/workflows/ci.yml`):

- **push / PR**: four gates — `go vet`, `go test`, `tsc`, `govulncheck`
- **`v*` tag or manual dispatch**: additionally packages `NanoSSH-<version>-windows-x64.zip` (portable) and `NanoSSH-<version>-x64.msi` (installer: Program Files, Start Menu / desktop shortcuts)
- **`v*` tag**: artifacts are attached to the GitHub Release automatically

Build the MSI locally (requires WiX Toolset 3.x; the script falls back to `choco install wixtoolset`):

```powershell
.\build\windows\make-msi.ps1 -Version 0.1.0
```

---

## Project layout

```
NanoSSH/
├─ main.go                 # Shell wiring: window options, lifecycle, single instance (no business logic)
├─ wails.json              # Wails build configuration
├─ build.ps1               # Build script: amd64 assertion / size guard / vuln scan / signing hook
│
├─ internal/
│  ├─ shell/               # Shell business layer (Wails binding facade)
│  │   ├─ app.go               # App facade: server CRUD, connect, host-key/passphrase prompts
│  │   ├─ sessions.go          # Terminal sessions: PTY, stdout/stderr merge, byte backpressure
│  │   ├─ sftp.go              # SFTP (reuses the same SSH connection, lazily created)
│  │   ├─ forward.go           # Port-forward rules and lifecycle
│  │   ├─ keys.go              # Key management (generate / import / public key / fingerprints)
│  │   ├─ secrets.go           # Windows Credential Manager wrapper
│  │   ├─ tray.go              # System tray (hand-rolled Shell_NotifyIconW, zero third-party deps)
│  │   └─ windowopts.go        # OS capability detection (Mica support)
│  │
│  ├─ sshcore/             # ★ SSH core: zero UI dependencies, reusable by any shell
│  │   ├─ client.go            # Dial, auth assembly, keepalive
│  │   ├─ session.go           # PTY / WindowChange / byte streams
│  │   ├─ sftp.go              # Browsing & transfer (concurrent request pipeline)
│  │   ├─ forward.go           # direct-tcpip local forwarding
│  │   ├─ hostkey.go           # knownhosts + TOFU callback protocol
│  │   ├─ auth.go / keys.go    # Authentication and key parse/generate
│  │   └─ agent_windows.go     # ssh-agent (named pipe)
│  │
│  ├─ store/               # Local persistence: atomic servers.json read/write, path resolution
│  └─ testsshd/            # In-process sshd for integration tests
│
├─ cmd/sshprobe/           # UI-less end-to-end probe (exec / shell / serve)
├─ frontend/               # Frontend (framework-free: Vanilla TS + xterm.js 6)
│  ├─ src/
│  │   ├─ components/          # TitleBar / ServerList / panels & dialogs
│  │   ├─ views/               # Top-level view assembly
│  │   ├─ ipc/                 # api (binding wrapper) / transport (backpressure) / decode.worker / mock
│  │   ├─ term/                # xterm.js wrapper
│  │   ├─ styles/              # tokens.css (design tokens) + app.css
│  │   ├─ state/ ui/           # Signal-based state & DOM constructor
│  │   └─ main.ts              # Entry point
│  └─ wailsjs/               # Bindings generated by wails build (generated)
└─ build/                  # Wails resources (icon / manifest) and output build/bin/
```

---

## Architecture

**Core principle: keep the risk in the replaceable layer.** `internal/sshcore` has no UI dependency whatsoever; all five features live there. The shell only renders and shuttles bytes. If the shell ever changes (e.g. to bare WebView2), only `internal/shell` and the frontend's `ipc/transport.ts` move — the core stays untouched.

### Terminal data path (where performance comes from)

```
remote sshd → x/crypto/ssh []byte → coalesce ~16KB / 8ms flush → IPC → worker base64-decodes
→ zero-copy to main thread → xterm.write(bytes, cb) → cb sends ack → backend reads the next chunk
```

**Ack backpressure is the real source of low latency**: xterm's write callback means "rendered", and the backend only reads the next chunk after the ack — at which point the SSH channel window naturally pushes pressure back to the remote shell. Everything stays `[]byte` end to end; the backend never splits lines and never UTF-8-decodes (a CJK character can straddle chunks).

### Session reuse

SFTP and port forwarding never re-handshake — they open extra channels (the `sftp` subsystem / `direct-tcpip`) over the same authenticated SSH connection. Panels are lazily created (the channel opens on first switch), and rules are cleaned up with ports released when the session ends.

---

## Data & configuration

Everything lives in `%APPDATA%\NanoSSH\` (override with the `NANOSSH_DATA_DIR` environment variable for portable runs and tests):

| Path | Contents | Back up? |
|---|---|---|
| `servers.json` | Server list (plain JSON, **no secrets**) | ✅ copying it backs up your config |
| `known_hosts` | Host fingerprints, standard OpenSSH format | interops with `~/.ssh/known_hosts` |
| `keys\` | Standard OpenSSH private key files | passphrase-protected keys stay encrypted |
| `logs\` | Runtime logs (secrets redacted) | safe to delete |

Passwords and key passphrases live in **Windows Credential Manager** (not in the folder above), bound to your Windows account — they cannot be decrypted on another machine, which is the correct behavior: copy `servers.json` + `keys\` to a new machine to restore servers and keys, then re-enter passwords once.

---

## Development

The frontend ships with a mock backend so you can preview every screen in a plain browser (with fake data):

```powershell
cd frontend
npm install
npm run dev        # http://localhost:5173
```

### Verification commands

```powershell
go vet ./...                    # static analysis
go test ./...                   # backend integration tests (terminal loop / backpressure / forwarding / SFTP)
cd frontend
npx tsc --noEmit                # frontend type check
npm run build                   # frontend build
.\build.ps1                     # full build + size guard
```

### UI-less probe

```powershell
go run ./cmd/sshprobe exec  -host H -user U -password P -cmd "uname -a"
go run ./cmd/sshprobe shell -host H -user U -password P
go run ./cmd/sshprobe serve                            # spin up an in-process test sshd
```

Every core capability must be verifiable without opening a window — "the UI clicks through" hides low-level protocol bugs.

---

## License

No license specified yet.
