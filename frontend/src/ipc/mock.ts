// 开发态 mock 后端：仅用于在普通浏览器里校验界面与终端渲染。
//
// 生产构建时，api.ts 里 `import.meta.env.DEV` 的分支会被静态求值为 false，
// 连带本模块被 tree-shake 掉，不会进入最终 bundle。

import type {
  Backend,
  FileEntry,
  ForwardStatus,
  HostKeyChoice,
  KeyInfo,
  ServerInfo,
  SftpListing,
  TransferStatus,
} from "./api";

function b64(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin);
}

export function createMockBackend(): Backend {
  const listeners = new Map<string, Set<(...data: unknown[]) => void>>();

  const emit = (name: string, ...data: unknown[]): void => {
    listeners.get(name)?.forEach((cb) => cb(...data));
  };

  let servers: ServerInfo[] = [
    { id: "01JD0MOCKUS", name: "VPS-01", country: "US", host: "203.0.113.24", port: 22, user: "root", auth: "password" },
    { id: "01JD0MOCKDE", name: "法兰克福中转", country: "DE", host: "198.51.100.7", port: 2222, user: "deploy", auth: "password" },
    { id: "01JD0MOCKJP", name: "东京-小机", country: "JP", host: "192.0.2.88", port: 22, user: "nano", auth: "password" },
    { id: "01JD0MOCKHK", name: "香港加速", country: "HK", host: "203.0.113.201", port: 22, user: "ubuntu", auth: "password" },
    { id: "01JD0MOCKKEY", name: "密钥样例机", country: "SG", host: "192.0.2.41", port: 22, user: "root", auth: "key", keyId: "01JD0MOCKK1" },
  ];

  // 开发态展示用密钥；encrypted 的那把会触发 passphrase:prompt，方便校验口令链路。
  let keys: KeyInfo[] = [
    {
      id: "01JD0MOCKK1",
      name: "工作机 ed25519",
      path: String.raw`C:\Users\you\AppData\Roaming\NanoSSH\keys\01JD0MOCKK1`,
      type: "ed25519",
      fingerprint: "SHA256:qB7wK3n1sV0pR7xL2cT9dM4hY6zA8uE5fG0iJ1kN3oQ",
      publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGqB7wK3n1sV0pR7xL2cT9dM4hY6zA8uE5fG0iJ1kN3oQ 工作机 ed25519",
      encrypted: true,
    } as KeyInfo & { encrypted: boolean },
    {
      id: "01JD0MOCKK2",
      name: "备份 RSA-4096",
      path: String.raw`C:\Users\you\AppData\Roaming\NanoSSH\keys\01JD0MOCKK2`,
      type: "rsa",
      fingerprint: "SHA256:z9Y8x7W6v5U4t3S2r1Q0p9O8n7M6l5K4j3I2h1G0f9E",
      publicKey:
        "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAACAQDz9Y8x7W6v5U4t3S2r1Q0p9O8n7M6l5K4j3I2h1G0f9E 备份 RSA-4096",
    },
  ];

  const secretPassphrase = "nanossh";
  const savedPasswords = new Set<string>();
  const DATA_DIR = String.raw`C:\Users\you\AppData\Roaming\NanoSSH`;

  let seq = 0;
  const nextId = (): string => `01MOCK${String(++seq).padStart(14, "0")}`;

  // ---- SFTP 传输模拟 ----
  const mockTimers = new Map<string, number>();
  const mockTransfers = new Map<string, TransferStatus>();

  /** 按 120ms 一帧推送进度，与后端的节流间隔保持一致。 */
  function runTransfer(st: TransferStatus): void {
    mockTransfers.set(st.id, st);
    const step = Math.max(24_000, Math.round(st.total / 12));
    const timer = window.setInterval(() => {
      st.done = Math.min(st.total, st.done + step);
      if (st.done >= st.total) {
        st.state = "done";
        window.clearInterval(timer);
        mockTimers.delete(st.id);
        mockTransfers.delete(st.id);
      }
      emit("sftp:transfer", { ...st });
    }, 120);
    mockTimers.set(st.id, timer);
    emit("sftp:transfer", { ...st });
  }

  // ---- 转发统计模拟 ----
  // 偶尔给运行中的规则涨一点流量，让统计列在开发态也能动起来；
  // 数值不变时不推送，与后端"只在变化时发事件"的行为一致。
  window.setInterval(() => {
    for (const st of mockForwards.values()) {
      if (st.state !== "running" || Math.random() > 0.45) continue;
      st.conns = Math.random() < 0.55 ? 1 : 2;
      if (Math.random() < 0.18) st.totalConns += 1;
      const chunk = 4_000 + Math.floor(Math.random() * 60_000);
      st.bytesOut += chunk;
      st.bytesIn += Math.round(chunk * (0.6 + Math.random()));
      emit("forward:status", { ...st });
    }
  }, 250);

  // 每个会话一个假的 shell 状态，键为 sessionId。
  const sessions = new Map<string, { user: string; host: string; opened: boolean }>();
  const pendingPrompts = new Map<string, (c: HostKeyChoice) => void>();
  const pendingPassphrases = new Map<string, (p: { passphrase: string; cancelled: boolean }) => void>();

  return {
    async version() {
      return "0.1.0-dev(mock)";
    },

    async dataDir() {
      return DATA_DIR;
    },

    async listServers() {
      return servers.map((s) => ({ ...s }));
    },

    async saveServer(s) {
      if (s.id) {
        const i = servers.findIndex((x) => x.id === s.id);
        if (i >= 0) {
          servers[i] = { ...servers[i], ...s, id: s.id } as ServerInfo;
          return { ...servers[i] };
        }
      }
      const created = { port: 22, auth: "password", ...s, id: nextId() } as ServerInfo;
      servers = [...servers, created];
      return { ...created };
    },

    async deleteServer(id) {
      servers = servers.filter((s) => s.id !== id);
      savedPasswords.delete(id);
    },

    async hasPassword(serverId) {
      return savedPasswords.has(serverId);
    },

    async forgetPassword(serverId) {
      savedPasswords.delete(serverId);
    },

    async listKeys() {
      return keys.map((k) => ({ ...k }));
    },

    async generateKey(req) {
      const algo = req.algo ?? "ed25519";
      const id = nextId();
      const created: KeyInfo = {
        id,
        name: req.name,
        path: `${DATA_DIR}/keys/${id}`,
        type: algo,
        fingerprint: "SHA256:" + id.toLowerCase(),
        publicKey: `ssh-${algo} AAAAB3NzaC1yc2E...${id} ${req.comment || req.name}`,
      };
      keys = [...keys, created];
      return { ...created };
    },

    async importKey(req) {
      if (!req.sourcePath) throw new Error("key: 未选择私钥文件");
      const id = nextId();
      const base = req.sourcePath.split(/[\\/]/).pop() || "imported";
      const created: KeyInfo = {
        id,
        name: req.name || base,
        path: `${DATA_DIR}/keys/${id}`,
        type: "ed25519",
        fingerprint: "SHA256:" + id.toLowerCase(),
        publicKey: `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI...${id} ${req.name || base}`,
      };
      keys = [...keys, created];
      return { ...created };
    },

    async deleteKey(id) {
      const used = servers.find((s) => s.keyId === id);
      if (used) throw new Error(`key: 该密钥正被 ${used.name} 使用，请先修改这些服务器的认证方式`);
      keys = keys.filter((k) => k.id !== id);
    },

    async pickKeyFile() {
      // 浏览器里没有系统对话框，返回一个假路径即可。
      await new Promise((r) => setTimeout(r, 120));
      return String.raw`C:\Users\you\.ssh\id_ed25519`;
    },

    async connect(req) {
      const server = servers.find((s) => s.id === req.serverId);
      if (!server) throw new Error(`未找到服务器 ${req.serverId}`);
      await new Promise((r) => setTimeout(r, 450));

      if (server.auth === "password" && req.password) savedPasswords.add(server.id);

      // 密钥认证：若引用的密钥受口令保护且未缓存，走一次 passphrase:prompt。
      if (server.auth === "key") {
        const key = keys.find((k) => k.id === server.keyId) as (KeyInfo & { encrypted?: boolean }) | undefined;
        if (!key) throw new Error("connect: 未选择密钥");
        if (key.encrypted && !savedPasswords.has("pp:" + key.id)) {
          let retry = false;
          for (;;) {
            const requestId = nextId();
            const ans = await new Promise<{ passphrase: string; cancelled: boolean }>((resolve) => {
              pendingPassphrases.set(requestId, resolve);
              emit("passphrase:prompt", {
                requestId,
                keyId: key.id,
                keyName: key.name,
                retry,
              });
            });
            if (ans.cancelled || !ans.passphrase) throw new Error("connect: 已取消输入私钥口令");
            if (ans.passphrase === secretPassphrase) break;
            retry = true;
          }
          savedPasswords.add("pp:" + key.id);
        }
      }

      // 首次连接弹一次主机密钥卡片，让 UI 的 TOFU 流程在开发态也能被看到。
      const requestId = nextId();
      const choice = await new Promise<HostKeyChoice>((resolve) => {
        pendingPrompts.set(requestId, resolve);
        emit("hostkey:prompt", {
          requestId,
          host: server.host,
          port: server.port,
          keyType: "ssh-ed25519",
          fingerprint: "SHA256:qB7wK3n1sV0pR7xL2cT9dM4hY6zA8uE5fG0iJ1kN3oQ",
          changed: false,
        });
      });
      if (choice === "reject") throw new Error("主机密钥未获信任，连接已中断");

      const sessionId = nextId();
      sessions.set(sessionId, { user: server.user, host: server.host, opened: false });
      return { sessionId, server };
    },

    async hostKeyDecision(requestId, decision) {
      pendingPrompts.get(requestId)?.(decision);
      pendingPrompts.delete(requestId);
    },

    async passphraseDecision(requestId, passphrase, cancelled) {
      pendingPassphrases.get(requestId)?.({ passphrase, cancelled });
      pendingPassphrases.delete(requestId);
    },

    async startShell(req) {
      const s = sessions.get(req.sessionId);
      if (!s) throw new Error(`未找到会话 ${req.sessionId}`);
      s.opened = true;

      // 模拟远端 banner 与提示符，验证 ANSI 色彩、CJK 宽度与滚动。
      const banner =
        "\x1b[38;5;110m╭──────────────────────────────────────────────╮\r\n" +
        "│  NanoSSH · mock shell                        │\r\n" +
        "╰──────────────────────────────────────────────╯\x1b[0m\r\n" +
        `\x1b[2m已连接到 \x1b[0m\x1b[1m${s.host}\x1b[0m\x1b[2m（开发态 mock，并非真实 SSH）\x1b[0m\r\n` +
        "\x1b[2m试试输入任意内容，回车后会被回显；输入 exit 结束会话。\x1b[0m\r\n\r\n";
      setTimeout(() => emit(`term:data:${req.sessionId}`, b64(banner)), 60);

      return { sessionId: req.sessionId, rows: req.rows, cols: req.cols };
    },

    async writeStdin(sessionId, data) {
      const s = sessions.get(sessionId);
      if (!s?.opened) return;

      let out = "";
      for (const ch of data) {
        const code = ch.charCodeAt(0);
        if (ch === "\r") {
          out += "\r\n";
          if (lineBuffer.get(sessionId)?.trim() === "exit") {
            setTimeout(() => emit(`term:exit:${sessionId}`, "远端已关闭会话"), 80);
            continue;
          }
          out += `\x1b[38;5;110m${s.user}@${s.host}\x1b[0m \x1b[38;5;245m~\x1b[0m $ `;
        } else if (code === 127 || code === 8) {
          out += "\b \b";
        } else if (code === 3) {
          out += "^C\r\n";
        } else if (code >= 32) {
          out += ch;
        }
      }
      if (out) {
        const acc = lineBuffer.get(sessionId) ?? "";
        if (data.includes("\r")) lineBuffer.set(sessionId, "");
        else lineBuffer.set(sessionId, acc + data.replace(/[\x00-\x1f]/g, ""));
        setTimeout(() => emit(`term:data:${sessionId}`, b64(out)), 12);
      }
    },

    async resizeShell() {
      /* mock 无远端 PTY，无需处理 */
    },

    async ackTermData() {
      /* mock 不实现反压 */
    },

    async closeSession(sessionId) {
      sessions.delete(sessionId);
      lineBuffer.delete(sessionId);
    },

    // ---------- SFTP（P3）----------
    // 内存里的假远端文件系统：只为让文件面板的交互在纯浏览器里可走通。

    async sftpOpen() {
      await new Promise((r) => setTimeout(r, 220));
      return { home: MOCK_HOME };
    },

    async sftpList(_sessionId, dir) {
      await new Promise((r) => setTimeout(r, 160));
      const target = dir || MOCK_HOME;
      if (!mockFS.has(target)) throw new Error(`sftp: 目录不存在 ${target}`);
      const entries: FileEntry[] = [...mockFS.entries()]
        .filter(([p]) => parentOf(p) === target)
        .map(([p, n]) => ({
          name: p.slice(p.lastIndexOf("/") + 1),
          path: p,
          size: n.size,
          mode: n.mode,
          modTime: n.modTime,
          isDir: n.isDir,
          isSymlink: false,
        }))
        .sort((a, b) =>
          a.isDir === b.isDir ? a.name.localeCompare(b.name) : a.isDir ? -1 : 1,
        );
      const listing: SftpListing = { path: target, parent: parentOf(target), entries };
      return listing;
    },

    async sftpMkdir(_sessionId, parent, name) {
      assertName(name);
      const path = `${parent}/${name}`;
      if (mockFS.has(path)) throw new Error(`sftp: ${name} 已存在`);
      mockFS.set(path, { isDir: true, size: 0, mode: "drwxr-xr-x", modTime: Date.now() });
    },

    async sftpRename(_sessionId, path, newName) {
      assertName(newName);
      if (!mockFS.has(path)) throw new Error("sftp: 对象已不存在");
      const moved = `${parentOf(path)}/${newName}`;
      if (mockFS.has(moved)) throw new Error(`sftp: ${newName} 已存在`);
      for (const key of [...mockFS.keys()]) {
        if (key === path || key.startsWith(path + "/")) {
          mockFS.set(moved + key.slice(path.length), mockFS.get(key)!);
          mockFS.delete(key);
        }
      }
    },

    async sftpDelete(_sessionId, path, isDir) {
      const node = mockFS.get(path);
      if (!node) return;
      if (isDir && [...mockFS.keys()].some((k) => k.startsWith(path + "/"))) {
        throw new Error("sftp: 目录非空");
      }
      mockFS.delete(path);
    },

    async sftpUpload(sessionId, remoteDir, localPaths) {
      const out: TransferStatus[] = localPaths.map((local) => {
        const name = local.split(/[\\/]/).pop() || "file";
        const total = 120_000 + Math.floor(Math.random() * 400_000);
        const st: TransferStatus = {
          id: nextId(),
          sessionId,
          direction: "upload",
          name,
          remote: `${remoteDir}/${name}`,
          local,
          total,
          done: 0,
          state: "running",
        };
        mockFS.set(st.remote, {
          isDir: false,
          size: total,
          mode: "-rw-r--r--",
          modTime: Date.now(),
        });
        runTransfer(st);
        return st;
      });
      return out;
    },

    async sftpDownload(sessionId, remotePath, localPath) {
      const node = mockFS.get(remotePath);
      if (!node || node.isDir) throw new Error("sftp: 只能下载文件");
      const st: TransferStatus = {
        id: nextId(),
        sessionId,
        direction: "download",
        name: remotePath.slice(remotePath.lastIndexOf("/") + 1),
        remote: remotePath,
        local: localPath,
        total: node.size,
        done: 0,
        state: "running",
      };
      runTransfer(st);
      return st;
    },

    async sftpCancelTransfer(id) {
      const timer = mockTimers.get(id);
      if (timer === undefined) return;
      window.clearInterval(timer);
      mockTimers.delete(id);
      const st = mockTransfers.get(id);
      if (st) {
        st.state = "cancelled";
        emit("sftp:transfer", { ...st });
      }
    },

    async pickUploadFiles() {
      await new Promise((r) => setTimeout(r, 140));
      return [String.raw`C:\Users\you\Desktop\report.pdf`, String.raw`C:\Users\you\Downloads\shot-01.png`];
    },

    async pickSavePath(defaultName) {
      await new Promise((r) => setTimeout(r, 140));
      // 不能用 String.raw：结尾的反斜杠会把模板串的反引号转义掉。
      return "C:\\Users\\you\\Downloads\\" + defaultName;
    },

    // ---------- 端口转发（P4）----------
    // 只造出规则与统计的形，不真的开监听：开发态要看的是面板的交互与观感。

    async forwardList(sessionId) {
      await new Promise((r) => setTimeout(r, 120));
      return [...mockForwards.values()]
        .filter((f) => f.sessionId === sessionId)
        .map((f) => ({ ...f }));
    },

    async forwardAdd(sessionId, listenPort, remoteHost, remotePort) {
      await new Promise((r) => setTimeout(r, 220));
      const host = remoteHost.trim();
      if (listenPort < 1 || listenPort > 65535) throw new Error("forward: 本地端口必须在 1~65535 之间");
      if (remotePort < 1 || remotePort > 65535) throw new Error("forward: 目标端口必须在 1~65535 之间");
      if (!host) throw new Error("forward: 目标主机不能为空");
      if (/[/\\\s]/.test(host)) throw new Error("forward: 目标主机不合法");
      if ([...mockForwards.values()].some((f) => f.sessionId === sessionId && f.listenPort === listenPort)) {
        throw new Error(`forward: 本地端口 ${listenPort} 已有转发规则`);
      }

      const st: ForwardStatus = {
        id: nextId(),
        sessionId,
        listenPort,
        remoteHost: host,
        remotePort,
        state: "running",
        conns: 0,
        totalConns: 0,
        bytesIn: 0,
        bytesOut: 0,
      };
      mockForwards.set(st.id, st);
      emit("forward:status", { ...st });
      return { ...st };
    },

    async forwardRemove(id) {
      const st = mockForwards.get(id);
      if (!st) return;
      mockForwards.delete(id);
      emit("forward:status", { ...st, state: "removed" });
    },

    async forwardStart(id) {
      const st = mockForwards.get(id);
      if (!st) throw new Error("forward: 未找到规则");
      st.state = "running";
      st.error = undefined;
      emit("forward:status", { ...st });
      return { ...st };
    },

    async forwardStop(id) {
      const st = mockForwards.get(id);
      if (!st) throw new Error("forward: 未找到规则");
      st.state = "stopped";
      st.conns = 0;
      emit("forward:status", { ...st });
      return { ...st };
    },

    // 开发态没有窗口/托盘，关闭请求静默处理；浏览器里也没有 Mica 背景，
    // 保持不透明表面（要预览玻璃表面可在控制台给 html 加 mica 类）。
    async requestClose() {
      console.warn("[nanossh] mock: requestClose（开发态无窗口生命周期）");
    },
    async micaEnabled() {
      return false;
    },

    on(name, cb) {
      let set = listeners.get(name);
      if (!set) {
        set = new Set();
        listeners.set(name, set);
      }
      set.add(cb);
      return () => set.delete(cb);
    },
  };
}

// mock shell 的行缓冲：用于识别 exit。
const lineBuffer = new Map<string, string>();

// mock 转发规则：键为规则 ID。
const mockForwards = new Map<string, ForwardStatus>();

// ---- mock 远端文件系统（SFTP 面板在开发态的假目录树）----

const MOCK_HOME = "/home/nano";

interface MockNode {
  isDir: boolean;
  size: number;
  mode: string;
  modTime: number;
}

function parentOf(p: string): string {
  const i = p.lastIndexOf("/");
  return i <= 0 ? "/" : p.slice(0, i);
}

/** 与后端 cleanEntryName 同规则：空名、"."、".."、含分隔符的一律拒。 */
function assertName(name: string): void {
  if (!name || name === "." || name === ".." || /[/\\]/.test(name)) {
    throw new Error("sftp: 名称不能为空，也不能包含路径分隔符");
  }
}

const mockFS = new Map<string, MockNode>(function seed(): [string, MockNode][] {
  const now = Date.now();
  const day = 86_400_000;
  const node = (isDir: boolean, size: number, age: number, mode?: string): MockNode => ({
    isDir,
    size,
    mode: mode ?? (isDir ? "drwxr-xr-x" : "-rw-r--r--"),
    modTime: now - age,
  });
  return [
    [MOCK_HOME, node(true, 0, day * 30)],
    [`${MOCK_HOME}/.ssh`, node(true, 0, day * 12)],
    [`${MOCK_HOME}/.ssh/authorized_keys`, node(false, 812, day * 12)],
    [`${MOCK_HOME}/backups`, node(true, 0, day * 2)],
    [`${MOCK_HOME}/backups/servers.json`, node(false, 4_196, day * 2)],
    [`${MOCK_HOME}/logs`, node(true, 0, 3_600_000)],
    [`${MOCK_HOME}/logs/nginx-access.log`, node(false, 4_812_336, 3_600_000)],
    [`${MOCK_HOME}/deploy.sh`, node(false, 1_337, day * 6, "-rwxr-xr-x")],
    [`${MOCK_HOME}/notes.md`, node(false, 2_048, day)],
  ];
}());
