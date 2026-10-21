// 会话内的转发面板：本地端口转发（ssh -L）的规则列表与启停。
//
// ★ 与文件面板同理，它挂在同一条已认证的 SSH 连接上（后端只多开
//   direct-tcpip 通道），所以和终端之间来回切换不需要重连，也不占第二个会话标签。

import { h, ICONS, icon } from "../ui/h";
import { backend, type ForwardStatus } from "../ipc/api";
import { field, openDialog } from "./Dialog";

export interface ForwardPanelOptions {
  sessionId: string;
  /** 面板自身不弹框，错误统一交给上层呈现，保证视觉一致。 */
  onError(message: string): void;
}

export interface ForwardPanel {
  root: HTMLElement;
  /** 面板首次显示时调用，拉取规则列表。 */
  open(): void;
  /** 接收一条规则的状态快照（由 forward:status 事件派发过来）。 */
  onStatus(st: ForwardStatus): void;
  /** 会话关闭时调用：拒绝后续一切回调。 */
  dispose(): void;
}

function human(err: unknown): string {
  return (err instanceof Error ? err.message : String(err)).replace(/^[a-z]+:\s*/i, "");
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

function toolBtn(title: string, path: string, onclick: () => void): HTMLElement {
  return h(
    "button",
    { class: "fbtn", type: "button", title, onclick },
    icon(path, 13, 1.7),
    h("span", {}, title),
  );
}

function rowBtn(title: string, path: string, danger: boolean, onclick: () => void) {
  return h(
    "button",
    {
      class: "ficon",
      type: "button",
      title,
      dataset: danger ? { danger: "true" } : {},
      onclick: (e: MouseEvent) => {
        e.stopPropagation();
        onclick();
      },
    },
    icon(path, 13, 1.7),
  );
}

const STATE_LABEL: Record<ForwardStatus["state"], string> = {
  running: "运行中",
  stopped: "已停止",
  error: "出错",
  removed: "已删除",
};

export function createForwardPanel(opts: ForwardPanelOptions): ForwardPanel {
  let rules: ForwardStatus[] = [];
  let opened = false;
  let dead = false;
  let busy = false;

  /** 上报错误；会话已关闭时静默，不弹出指向死连接的对话框。 */
  function fail(message: string): void {
    if (!dead) opts.onError(message);
  }

  const rowsEl = h("div", { class: "fwrows" });
  const rowEls = new Map<string, HTMLElement>();

  const root = h(
    "div",
    { class: "fwpanel" },
    h(
      "div",
      { class: "fbar" },
      h(
        "div",
        { class: "fwbar__intro" },
        h("div", { class: "fwbar__title" }, "端口转发"),
        h(
          "div",
          { class: "fwbar__hint" },
          "本地端口只监听 127.0.0.1，流量走当前这条 SSH 连接。",
        ),
      ),
      h("div", { class: "fbar__acts" }, toolBtn("添加转发", ICONS.plus, () => promptAdd())),
    ),
    rowsEl,
  );

  // ---------- 数据 ----------

  async function load(): Promise<void> {
    if (dead || busy) return;
    busy = true;
    try {
      rules = await backend.forwardList(opts.sessionId);
    } catch (err) {
      fail(human(err));
    } finally {
      busy = false;
      render();
    }
  }

  async function open(): Promise<void> {
    if (opened || dead) return;
    opened = true;
    await load();
  }

  // ---------- 操作 ----------

  /** 三个输入框的数字解析：空串或非数字一律算 0，交由后端校验拒掉。 */
  function portOf(input: HTMLInputElement): number {
    return Number.parseInt(input.value.trim(), 10) || 0;
  }

  function promptAdd(): void {
    const localInput = h("input", {
      value: "",
      placeholder: "例如 8080",
      inputmode: "numeric",
      autocomplete: "off",
      spellcheck: false,
    });
    const hostInput = h("input", {
      value: "",
      placeholder: "例如 127.0.0.1 或 db.internal",
      autocomplete: "off",
      spellcheck: false,
    });
    const portInput = h("input", {
      value: "",
      placeholder: "例如 5432",
      inputmode: "numeric",
      autocomplete: "off",
      spellcheck: false,
    });

    let dlg: { close(): void };
    const done = (): void => {
      const listenPort = portOf(localInput);
      const remotePort = portOf(portInput);
      const remoteHost = hostInput.value.trim();
      if (!listenPort) {
        localInput.focus();
        return;
      }
      if (!remoteHost) {
        hostInput.focus();
        return;
      }
      if (!remotePort) {
        portInput.focus();
        return;
      }
      dlg.close();
      void backend
        .forwardAdd(opts.sessionId, listenPort, remoteHost, remotePort)
        .then((st) => {
          rules = [...rules, st];
          render();
        })
        .catch((err) => fail(human(err)));
    };

    dlg = openDialog({
      title: "添加端口转发",
      subtitle: "ssh -L 语义：本地端口 → 远端主机端口",
      iconPath: ICONS.forward,
      body: [
        field("本地端口", localInput, h("div", { class: "field__hint" }, "在本机的 127.0.0.1 上监听")),
        field("目标主机", hostInput),
        field("目标端口", portInput, h("div", { class: "field__hint" }, "从远端服务器的角度访问的目标")),
      ],
      footer: [
        h("button", { class: "btn", onclick: () => dlg.close() }, "取消"),
        h("button", { class: "btn", "data-variant": "primary", onclick: done }, "添加并启动"),
      ],
    });

    for (const input of [localInput, hostInput, portInput] as HTMLInputElement[]) {
      input.addEventListener("keydown", (e: KeyboardEvent) => {
        if (e.key === "Enter") {
          e.preventDefault();
          done();
        }
      });
    }
    requestAnimationFrame(() => localInput.focus());
  }

  function toggle(rule: ForwardStatus): void {
    const call =
      rule.state === "running" ? backend.forwardStop(rule.id) : backend.forwardStart(rule.id);
    void call
      .then((st) => {
        rules = rules.map((r) => (r.id === st.id ? st : r));
        render();
      })
      .catch((err) => fail(human(err)));
  }

  function confirmRemove(rule: ForwardStatus): void {
    const dlg = openDialog({
      title: "删除转发规则",
      subtitle: `127.0.0.1:${rule.listenPort} → ${rule.remoteHost}:${rule.remotePort}`,
      tone: "danger",
      iconPath: ICONS.warn,
      body: [
        h(
          "div",
          { class: "note", "data-tone": "danger" },
          "会立即停止监听并断开正在经过它的连接。",
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
                .forwardRemove(rule.id)
                .then(() => {
                  rules = rules.filter((r) => r.id !== rule.id);
                  render();
                })
                .catch((err) => fail(human(err)));
            },
          },
          "删除",
        ),
      ],
    });
  }

  // ---------- 状态推送 ----------

  function onStatus(st: ForwardStatus): void {
    if (dead) return;
    if (st.state === "removed") {
      if (!rules.some((r) => r.id === st.id)) return;
      rules = rules.filter((r) => r.id !== st.id);
      render();
      return;
    }
    const i = rules.findIndex((r) => r.id === st.id);
    if (i < 0) return; // 不是本面板创建的规则，不打扰
    rules = rules.map((r) => (r.id === st.id ? st : r));

    // 只补丁这一行的活动数字，不整表重绘：统计是高频更新，
    // 重绘会把 hover 态和按钮按下态一起冲掉。
    const row = rowEls.get(st.id);
    if (!row) {
      render();
      return;
    }
    row.dataset.state = st.state;
    const badge = row.querySelector<HTMLElement>(".fwrow__state");
    if (badge) badge.textContent = st.error || STATE_LABEL[st.state];
    const conns = row.querySelector<HTMLElement>(".fwrow__conns");
    if (conns) conns.textContent = String(st.conns);
    const traffic = row.querySelector<HTMLElement>(".fwrow__traffic");
    if (traffic) traffic.textContent = `↑ ${formatBytes(st.bytesOut)} · ↓ ${formatBytes(st.bytesIn)}`;
    const total = row.querySelector<HTMLElement>(".fwrow__total");
    if (total) total.textContent = String(st.totalConns);
  }

  // ---------- 渲染 ----------

  function ruleRow(rule: ForwardStatus): HTMLElement {
    const running = rule.state === "running";
    return h(
      "div",
      {
        class: "fwrow",
        dataset: { state: rule.state, id: rule.id },
        title: rule.error || `经 SSH 转发到 ${rule.remoteHost}:${rule.remotePort}`,
      },
      h(
        "div",
        { class: "fwrow__route" },
        h("span", { class: "fwrow__port" }, `127.0.0.1:${rule.listenPort}`),
        h("span", { class: "fwrow__arrow" }, icon(ICONS.forward, 13, 1.7)),
        h("span", { class: "fwrow__host" }, `${rule.remoteHost}:${rule.remotePort}`),
      ),
      h(
        "div",
        { class: "fwrow__cell fwrow__state" },
        rule.error || STATE_LABEL[rule.state],
      ),
      h(
        "div",
        { class: "fwrow__cell fwrow__nums" },
        h("span", { class: "fwrow__conns" }, String(rule.conns)),
        h("span", { class: "fwrow__nums-hint" }, "活动"),
        h("span", { class: "fwrow__total" }, String(rule.totalConns)),
        h("span", { class: "fwrow__nums-hint" }, "累计"),
      ),
      h(
        "div",
        { class: "fwrow__cell fwrow__traffic" },
        `↑ ${formatBytes(rule.bytesOut)} · ↓ ${formatBytes(rule.bytesIn)}`,
      ),
      h(
        "div",
        { class: "fwrow__acts" },
        rowBtn(running ? "停止" : "启动", ICONS.power, false, () => toggle(rule)),
        rowBtn("删除", ICONS.trash, true, () => confirmRemove(rule)),
      ),
    );
  }

  function render(): void {
    rowEls.clear();

    if (!rules.length) {
      rowsEl.replaceChildren(
        h(
          "div",
          { class: "fempty" },
          icon(ICONS.forward, 30, 1.25),
          h("div", { class: "fempty__title" }, "还没有转发规则"),
          h(
            "div",
            { class: "fempty__hint" },
            "把远端服务映射到本机端口，例如把内网的 127.0.0.1:5432 映射到本地 8080。",
          ),
        ),
      );
      return;
    }

    const head = h(
      "div",
      { class: "fwrow fwrow--head" },
      h("div", { class: "fwrow__route" }, "转发路由"),
      h("div", { class: "fwrow__cell fwrow__state" }, "状态"),
      h("div", { class: "fwrow__cell fwrow__nums" }, "连接"),
      h("div", { class: "fwrow__cell fwrow__traffic" }, "流量"),
      h("div", { class: "fwrow__acts" }),
    );

    const body = rules.map((r) => {
      const el = ruleRow(r);
      rowEls.set(r.id, el);
      return el;
    });
    rowsEl.replaceChildren(head, ...body);
  }

  render();

  return {
    root,
    open,
    onStatus,
    dispose(): void {
      dead = true;
      rowEls.clear();
    },
  };
}
