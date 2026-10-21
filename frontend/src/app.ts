// 应用装配与流程控制。
//
// 一切交互都收束到两条路径：点服务器行 → 连上 → 出 shell；以及服务器 CRUD。
// 没有落地页、没有 dashboard——第一屏就是列表。
//
// P2 增加第二个分区：轨道顶部可切到「密钥」。切换只换轨道与舞台的内容，
// 终端 pane 保持挂载——切回来时连接还在，不用重连。

import { effect } from "@preact/signals-core";
import {
  backend,
  windowCtl,
  type ForwardStatus,
  type HostKeyPrompt,
  type KeyInfo,
  type PassphrasePrompt,
  type ServerInfo,
  type TransferStatus,
} from "./ipc/api";
import { createTitleBar } from "./components/TitleBar";
import { createRail } from "./components/ServerList";
import { createStage } from "./views/TerminalView";
import { createKeyStage } from "./components/KeyView";
import { openKeyDialog } from "./components/KeyDialog";
import { openHostKeyDialog } from "./components/HostKeyDialog";
import { openPassphraseDialog } from "./components/PassphraseDialog";
import { openPasswordDialog, openServerDialog } from "./components/ServerDialog";
import { openDialog } from "./components/Dialog";
import { h, ICONS } from "./ui/h";
import {
  activeKey,
  activeTab,
  addKeyLocal,
  closeTabLocal,
  connectError,
  connecting,
  dataDir,
  loadKeys,
  loadMeta,
  loadServers,
  persistServer,
  railView,
  removeKeyLocal,
  removeServer,
  tabs,
  upsertTab,
  type SessionTab,
} from "./state/store";

const FONT = {
  fontFamily: '"Cascadia Mono", ui-monospace, "JetBrains Mono", Consolas, monospace',
  fontSize: 13,
  scrollback: 5000,
};

/** 去掉后端错误里的 "key: " / "connect: " 前缀。 */
function humanError(err: unknown): string {
  const raw = err instanceof Error ? err.message : String(err);
  return raw.replace(/^[a-z]+:\s*/i, "");
}

export function mountApp(root: HTMLElement): void {
  // ---------- 舞台 ----------

  const stage = createStage({
    font: () => FONT,
    onClose: (sessionId) => void closeSession(sessionId),
    onRetry: () => {
      const server = connectError.value?.server;
      connectError.value = null;
      if (server) void openServer(server);
    },
    onDismissError: () => {
      connectError.value = null;
    },
    // 文件面板不自己弹框：错误统一走 showError，和密钥/服务器那边一个视觉。
    onError: (message) => showError("文件操作失败", message),
  });

  const keyStage = createKeyStage({ onDelete: deleteKey });

  // ---------- 连接流程 ----------

  async function openServer(server: ServerInfo, password?: string): Promise<void> {
    const existing = tabs.value.find((t) => t.server.id === server.id);
    if (existing) {
      activeTab.value = existing.sessionId;
      stage.terminalOf(existing.sessionId)?.focus();
      return;
    }

    // 密码认证且本次未提供密码：先问后端是否已经存过，没有才弹框。
    if (server.auth === "password" && password === undefined) {
      let saved = false;
      try {
        saved = await backend.hasPassword(server.id);
      } catch {
        saved = false;
      }
      if (!saved) {
        openPasswordDialog(server, (pw) => void openServer(server, pw));
        return;
      }
    }

    connecting.value = server;
    connectError.value = null;

    try {
      const res = await backend.connect({
        serverId: server.id,
        password: password || undefined,
      });
      const tab: SessionTab = { sessionId: res.sessionId, server: res.server, state: "open" };
      upsertTab(tab);
      // 先切到该标签，pane 变为可见后 fit() 才能算出真实行列数。
      activeTab.value = res.sessionId;

      const handle = stage.openTerminal(tab);
      const { rows, cols } = handle.fit();

      // ★ 参数顺序：Go 侧 (rows, cols)——行在前、列在后。
      await backend.startShell({ sessionId: res.sessionId, rows, cols });
      handle.syncSize();
      handle.focus();
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      if (message.includes("主机密钥未获信任") || message.includes("已取消")) {
        // 用户主动取消，不算失败。
      } else {
        connectError.value = { server, message: humanError(err) };
      }
    } finally {
      connecting.value = null;
    }
  }

  async function closeSession(sessionId: string): Promise<void> {
    stage.drop(sessionId);
    closeTabLocal(sessionId);
    try {
      await backend.closeSession(sessionId);
    } catch {
      /* 连接可能已断开，忽略 */
    }
  }

  // 后端在握手过程中会通过事件询问主机密钥；回应后 Connect 才继续。
  backend.on("hostkey:prompt", (payload) => {
    const p = payload as HostKeyPrompt;
    openHostKeyDialog(p, (choice) => {
      void backend.hostKeyDecision(p.requestId, choice);
    });
  });

  // 私钥口令也是同一套"后端暂停、前端应答"的机制。
  backend.on("passphrase:prompt", (payload) => {
    const p = payload as PassphrasePrompt;
    openPassphraseDialog(p, (passphrase, cancelled) => {
      void backend.passphraseDecision(p.requestId, passphrase, cancelled);
    });
  });

  // 文件传输进度：后端按会话推送，这里路由到对应标签的文件面板。
  // 面板可能还没创建（传输由它自己发起，理论上必然存在）——找不到就丢弃，
  // 不为一次进度事件去把面板建出来。
  backend.on("sftp:transfer", (payload) => {
    const st = payload as TransferStatus;
    stage.filePanelOf(st.sessionId)?.onTransfer(st);
  });

  // 转发统计：同一套路由到对应标签的转发面板，面板没建就丢弃。
  backend.on("forward:status", (payload) => {
    const st = payload as ForwardStatus;
    stage.forwardPanelOf(st.sessionId)?.onStatus(st);
  });

  // ---------- 服务器 CRUD ----------

  function newServer(): void {
    openServerDialog(null, (value) => {
      void persistServer(value).then((saved) => {
        void openServer(saved);
      });
    });
  }

  function editServer(server: ServerInfo): void {
    openServerDialog(server, (value) => {
      void persistServer(value);
    });
  }

  function deleteServer(server: ServerInfo): void {
    const dlg = openDialog({
      title: "删除服务器",
      subtitle: server.name || server.host,
      tone: "danger",
      iconPath: ICONS.warn,
      body: [
        h(
          "div",
          { class: "note", "data-tone": "danger" },
          "配置将从 servers.json 中移除，同时清除该服务器保存的密码。私钥文件与 known_hosts 记录不受影响。",
        ),
      ],
      footer: [
        h("button", { class: "btn", onclick: () => dlg.close() }, "取消"),
        h(
          "button",
          {
            class: "btn",
            "data-variant": "danger",
            onclick: () => {
              dlg.close();
              const tab = tabs.value.find((t) => t.server.id === server.id);
              if (tab) void closeSession(tab.sessionId);
              void removeServer(server.id);
            },
          },
          "删除",
        ),
      ],
    });
  }

  // ---------- 密钥 CRUD ----------

  function showError(title: string, message: string): void {
    const d = openDialog({
      title,
      tone: "danger",
      iconPath: ICONS.warn,
      body: [h("div", { class: "note", "data-tone": "danger" }, message)],
      footer: [
        h("button", { class: "btn", "data-variant": "primary", onclick: () => d.close() }, "知道了"),
      ],
    });
  }

  function newKey(): void {
    openKeyDialog((key) => {
      addKeyLocal(key);
      railView.value = "keys";
    });
  }

  function deleteKey(key: KeyInfo): void {
    const dlg = openDialog({
      title: "删除密钥",
      subtitle: key.name,
      tone: "danger",
      iconPath: ICONS.warn,
      body: [
        h(
          "div",
          { class: "note", "data-tone": "danger" },
          "私钥文件与已保存的口令会一并删除，且不可恢复。若仍有服务器引用该密钥，删除会被拒绝。",
        ),
      ],
      footer: [
        h("button", { class: "btn", onclick: () => dlg.close() }, "取消"),
        h(
          "button",
          {
            class: "btn",
            "data-variant": "danger",
            onclick: () => {
              dlg.close();
              void backend
                .deleteKey(key.id)
                .then(() => removeKeyLocal(key.id))
                .catch((err) => showError("无法删除密钥", humanError(err)));
            },
          },
          "删除",
        ),
      ],
    });
  }

  // ---------- 侧栏 ----------

  const rail = createRail({
    onOpen: (server) => void openServer(server),
    onEdit: editServer,
    onDelete: deleteServer,
    onNewServer: newServer,
    onNewKey: newKey,
    onOpenKey: (key) => {
      activeKey.value = key.id;
    },
    onDeleteKey: deleteKey,
    onCopyDataDir: () => {
      const dir = dataDir.value;
      if (dir) void windowCtl.copyText(dir);
    },
  });

  // ---------- 装配 ----------

  const titlebar = createTitleBar();
  const work = h("div", { class: "workarea" }, stage.root, keyStage);
  root.append(h("div", { class: "app" }, titlebar, h("div", { class: "body" }, rail, work)));

  // 分区切换：只换舞台内容，终端 pane 保持挂载；切回服务器时重新量一次尺寸。
  effect(() => {
    const onKeys = railView.value === "keys";
    stage.root.hidden = onKeys;
    keyStage.hidden = !onKeys;
    if (!onKeys) requestAnimationFrame(() => stage.refitAll());
  });

  document.addEventListener("keydown", (e: KeyboardEvent) => {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "n") {
      e.preventDefault();
      newServer();
    }
  });

  void loadMeta();
  void loadServers().catch((err) => {
    console.error("[nanossh] 读取服务器列表失败：", err);
  });
  void loadKeys().catch((err) => {
    console.error("[nanossh] 读取密钥列表失败：", err);
  });
}
