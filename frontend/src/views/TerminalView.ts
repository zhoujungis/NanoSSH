// 舞台：标签条 + 终端面板 + 空/连接中/失败三种状态卡。
// 每个会话一个独立 pane 与一个 xterm 实例；切换标签只是显示/隐藏，
// 实例与远端连接都保持存活。
//
// pane 内部再分「终端 / 文件 / 转发」三个子视图（P3 / P4）。这是刻意的：
// 文件面板与转发面板都挂在同一条 SSH 连接上，做成会话内的子标签而不是一级导航，
// 用户不必为传文件或开转发另开一个会话，连接数与标签数都不涨。

import { effect } from "@preact/signals-core";
import { h, ICONS, icon, type Child } from "../ui/h";
import {
  activeTab,
  connectError,
  connecting,
  paneViewOf,
  paneViews,
  setPaneView,
  tabs,
  type PaneView,
  type SessionTab,
} from "../state/store";
import { createTerminal, type TermHandle } from "../term/terminal";
import { flagIcon } from "../components/FlagIcon";
import { createFilePanel, type FilePanel } from "../components/FilePanel";
import { createForwardPanel, type ForwardPanel } from "../components/ForwardPanel";

export interface StageOptions {
  font(): { fontFamily: string; fontSize: number; scrollback: number };
  onClose(sessionId: string): void;
  onRetry(): void;
  onDismissError(): void;
  /** 文件面板自身不弹框，错误统一走这里，保证全应用视觉一致。 */
  onError(message: string): void;
}

export interface Stage {
  root: HTMLElement;
  hostFor(sessionId: string): HTMLElement;
  setOverlay(sessionId: string, content: Child | null): void;
  openTerminal(tab: SessionTab): TermHandle;
  terminalOf(sessionId: string): TermHandle | undefined;
  /** 返回该会话已创建的文件面板；未切到「文件」时为 undefined。 */
  filePanelOf(sessionId: string): FilePanel | undefined;
  /** 返回该会话已创建的转发面板；未切到「转发」时为 undefined。 */
  forwardPanelOf(sessionId: string): ForwardPanel | undefined;
  drop(sessionId: string): void;
  refitAll(): void;
}

function card(title: string, msg: Child, tone?: string, actions?: Node[]): HTMLElement {
  return h(
    "div",
    { class: "status__card" },
    h("div", { class: "status__mark" }, icon(tone === "error" ? ICONS.warn : ICONS.terminal, 26, 1.3)),
    h("div", { class: "overlay__title" }, title),
    msg ? h("div", { class: "overlay__msg", dataset: tone ? { tone } : {} }, msg) : null,
    actions?.length ? h("div", { class: "overlay__actions" }, ...actions) : null,
  );
}

export function createStage(opts: StageOptions): Stage {
  const terminals = new Map<string, TermHandle>();
  const panes = new Map<string, HTMLElement>();
  const hosts = new Map<string, HTMLElement>();
  const bodies = new Map<string, HTMLElement>();
  // 文件面板懒创建：首次切到「文件」才去后端开 sftp 通道，
  // 只用终端的会话不会为此多付一次子系统握手。
  const panels = new Map<string, FilePanel>();
  // 转发面板同理懒创建：它的规则列表只在需要看时才拉。
  const fwdPanels = new Map<string, ForwardPanel>();

  const tabsEl = h("div", { class: "tabs" });
  const panesEl = h("div", { class: "panes-body" });
  const statusEl = h("div", { class: "status" });

  const root = h("main", { class: "stage" }, tabsEl, h("div", { class: "panes" }, panesEl, statusEl));

  // ---- 状态区 ----

  const renderStatus = (): void => {
    if (tabs.value.length > 0) {
      statusEl.hidden = true;
      return;
    }
    statusEl.hidden = false;

    const err = connectError.value;
    if (err) {
      statusEl.replaceChildren(
        card(
          `连接 ${err.server.name || err.server.host} 失败`,
          err.message,
          "error",
          [
            h("button", { class: "btn", "data-variant": "primary", onclick: () => opts.onRetry() }, "重试"),
            h("button", { class: "btn", onclick: () => opts.onDismissError() }, "关闭"),
          ],
        ),
      );
      return;
    }

    const pending = connecting.value;
    if (pending) {
      statusEl.replaceChildren(
        h(
          "div",
          { class: "status__card" },
          h("div", { class: "spinner" }),
          h("div", { class: "overlay__title" }, `正在连接 ${pending.name || pending.host}`),
          h("div", { class: "overlay__msg" }, `${pending.user}@${pending.host}:${pending.port}`),
        ),
      );
      return;
    }

    statusEl.replaceChildren(
      h(
        "div",
        { class: "empty" },
        h("div", { class: "empty__mark" }, icon(ICONS.terminal, 34, 1.2)),
        h("div", { class: "empty__title" }, "选一台服务器，立刻开始"),
        h(
          "div",
          { class: "empty__hint" },
          "左侧是服务器列表，单击即连接。",
          h("br"),
          "也可以按 ",
          h("kbd", {}, "Ctrl"),
          " + ",
          h("kbd", {}, "N"),
          " 新建一台。",
        ),
      ),
    );
  };

  // ---- 标签条 ----

  const tabEl = (t: SessionTab): HTMLElement =>
    h(
      "div",
      {
        class: "tab",
        dataset: { id: t.sessionId, active: String(activeTab.value === t.sessionId), state: t.state },
        onclick: () => {
          activeTab.value = t.sessionId;
          terminals.get(t.sessionId)?.focus();
        },
        onauxclick: (e: MouseEvent) => {
          if (e.button === 1) opts.onClose(t.sessionId);
        },
        title: `${t.server.user}@${t.server.host}:${t.server.port}`,
      },
      h("span", { class: "tab__dot" }),
      h("span", { class: "tab__flag" }, flagIcon(t.server.country)),
      h("span", { class: "tab__label" }, t.server.name || t.server.host),
      h(
        "span",
        {
          class: "tab__close",
          onclick: (e: MouseEvent) => {
            e.stopPropagation();
            opts.onClose(t.sessionId);
          },
        },
        icon(ICONS.close, 12, 1.7),
      ),
    );

  const renderTabs = (): void => tabsEl.replaceChildren(...tabs.value.map(tabEl));

  const renderActive = (): void => {
    for (const [id, pane] of panes) pane.hidden = id !== activeTab.value;
    for (const el of tabsEl.querySelectorAll<HTMLElement>(".tab")) {
      el.dataset.active = String(el.dataset.id === activeTab.value);
    }
  };

  effect(renderTabs);
  effect(renderActive);
  effect(renderStatus);

  // ---- pane 管理 ----

  function ensurePane(sessionId: string): HTMLElement {
    const found = panes.get(sessionId);
    if (found) return found;

    const host = h("div", { class: "term-host" });
    const body = h("div", { class: "pane__body" }, host);

    const segTerm = h(
      "button",
      {
        class: "seg__item",
        type: "button",
        dataset: { view: "term" },
        onclick: () => switchView(sessionId, "term"),
      },
      icon(ICONS.terminal, 13, 1.7),
      h("span", {}, "终端"),
    );
    const segFiles = h(
      "button",
      {
        class: "seg__item",
        type: "button",
        dataset: { view: "files" },
        onclick: () => switchView(sessionId, "files"),
      },
      icon(ICONS.folder, 13, 1.7),
      h("span", {}, "文件"),
    );
    const segFwd = h(
      "button",
      {
        class: "seg__item",
        type: "button",
        dataset: { view: "fwd" },
        onclick: () => switchView(sessionId, "fwd"),
      },
      icon(ICONS.forward, 13, 1.7),
      h("span", {}, "转发"),
    );

    const pane = h(
      "div",
      {
        class: "pane",
        dataset: { id: sessionId, view: paneViewOf(sessionId) },
        hidden: true,
      },
      h("div", { class: "pane__nav" }, h("div", { class: "seg" }, segTerm, segFiles, segFwd)),
      body,
    );

    panes.set(sessionId, pane);
    hosts.set(sessionId, host);
    bodies.set(sessionId, body);
    panesEl.append(pane);
    pane.hidden = activeTab.value !== sessionId;
    syncNav(pane);
    return pane;
  }

  /** 让分段控件的高亮跟随当前子视图。 */
  function syncNav(pane: HTMLElement): void {
    const view = pane.dataset.view;
    for (const el of pane.querySelectorAll<HTMLElement>(".pane__nav .seg__item")) {
      el.dataset.active = String(el.dataset.view === view);
    }
  }

  /**
   * 切换「终端 / 文件 / 转发」。
   *
   * ★ 切到文件或转发时 xterm 所在的 .term-host 被 display:none，尺寸归零；
   *   切回来必须重新 fit，否则光标位置会按旧列宽错位。
   */
  function switchView(sessionId: string, view: PaneView): void {
    if (paneViewOf(sessionId) === view) return;
    setPaneView(sessionId, view);
    if (view === "files") {
      void ensurePanel(sessionId).open();
      return;
    }
    if (view === "fwd") {
      void ensureFwdPanel(sessionId).open();
      return;
    }
    requestAnimationFrame(() => {
      const handle = terminals.get(sessionId);
      handle?.fit();
      handle?.syncSize();
      handle?.focus();
    });
  }

  function ensurePanel(sessionId: string): FilePanel {
    const found = panels.get(sessionId);
    if (found) return found;
    const panel = createFilePanel({ sessionId, onError: opts.onError });
    panels.set(sessionId, panel);
    bodies.get(sessionId)!.append(panel.root);
    return panel;
  }

  function ensureFwdPanel(sessionId: string): ForwardPanel {
    const found = fwdPanels.get(sessionId);
    if (found) return found;
    const panel = createForwardPanel({ sessionId, onError: opts.onError });
    fwdPanels.set(sessionId, panel);
    bodies.get(sessionId)!.append(panel.root);
    return panel;
  }

  // 子视图状态广播到每个 pane：分段高亮 + CSS 切换显示哪一块。
  //
  // ★ 必须先读一次 paneViews.value 再遍历：effect 只认「执行时读到的信号」，
  //   建 stage 时 panes 还是空的，若只在循环里读，首次执行零订阅、之后永不重跑。
  effect(() => {
    const views = paneViews.value;
    for (const [id, pane] of panes) {
      pane.dataset.view = views[id] ?? "term";
      syncNav(pane);
    }
  });

  const ro = new ResizeObserver(() => {
    for (const [id, handle] of terminals) {
      const host = hosts.get(id);
      if (!host || panes.get(id)?.hidden) continue;
      // .term-host 被隐藏时会收到 0×0 的观察值，此时 fit 会把行列算没。
      if (host.clientWidth < 4 || host.clientHeight < 4) continue;
      handle.fit();
      handle.syncSize();
    }
  });

  return {
    root,

    hostFor(sessionId) {
      ensurePane(sessionId);
      return hosts.get(sessionId)!;
    },

    setOverlay(sessionId, content) {
      const pane = ensurePane(sessionId);
      pane.querySelector(".overlay")?.remove();
      if (content) bodies.get(sessionId)!.append(h("div", { class: "overlay" }, content));
    },

    openTerminal(tab) {
      const existing = terminals.get(tab.sessionId);
      if (existing) return existing;

      const host = this.hostFor(tab.sessionId);
      const handle = createTerminal(tab.sessionId, host, {
        ...opts.font(),
        onExit: (reason) => {
          this.setOverlay(
            tab.sessionId,
            card(
              "会话已结束",
              reason,
              "error",
              [
                h("button", { class: "btn", "data-variant": "primary", onclick: () => opts.onRetry() }, "重新连接"),
                h("button", { class: "btn", onclick: () => opts.onClose(tab.sessionId) }, "关闭标签"),
              ],
            ),
          );
        },
      });
      terminals.set(tab.sessionId, handle);
      ro.observe(host);
      return handle;
    },

    terminalOf: (sessionId) => terminals.get(sessionId),

    filePanelOf: (sessionId) => panels.get(sessionId),

    forwardPanelOf: (sessionId) => fwdPanels.get(sessionId),

    drop(sessionId) {
      terminals.get(sessionId)?.dispose();
      terminals.delete(sessionId);
      const host = hosts.get(sessionId);
      if (host) ro.unobserve(host);
      panels.get(sessionId)?.dispose();
      panels.delete(sessionId);
      fwdPanels.get(sessionId)?.dispose();
      fwdPanels.delete(sessionId);
      panes.get(sessionId)?.remove();
      panes.delete(sessionId);
      hosts.delete(sessionId);
      bodies.delete(sessionId);
    },

    refitAll() {
      for (const [id, handle] of terminals) {
        const host = hosts.get(id);
        if (!host || panes.get(id)?.hidden) continue;
        if (host.clientWidth < 4 || host.clientHeight < 4) continue;
        handle.fit();
        handle.syncSize();
      }
    },
  };
}
