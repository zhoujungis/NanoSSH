// 会话内的文件面板：远端目录浏览 + 上传 / 下载 / 重命名 / 删除 / 新建文件夹。
//
// ★ 它挂在同一条已认证的 SSH 连接上（后端只多开一个 sftp 子系统通道），
//   所以和终端之间来回切换不需要重连，也不占第二个会话标签。

import { h, ICONS, icon } from "../ui/h";
import {
  backend,
  type FileEntry,
  type SftpListing,
  type TransferStatus,
} from "../ipc/api";
import { field, openDialog } from "./Dialog";

export interface FilePanelOptions {
  sessionId: string;
  /** 面板自身不弹框，错误统一交给上层呈现，保证视觉一致。 */
  onError(message: string): void;
}

export interface FilePanel {
  root: HTMLElement;
  /** 面板首次显示时调用，打开远端家目录。 */
  open(): void;
  /** 接收一次传输进度快照（由 sftp:transfer 事件派发过来）。 */
  onTransfer(st: TransferStatus): void;
  /** 会话关闭时调用：停掉定时器并拒绝后续一切回调。 */
  dispose(): void;
}

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

function formatSize(n: number, isDir: boolean): string {
  if (isDir) return "—";
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

function formatWhen(ms: number): string {
  const d = new Date(ms);
  const now = new Date();
  const day = (x: Date): string => x.toDateString();
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  if (day(d) === day(now)) return `今天 ${hm}`;
  if (day(d) === day(new Date(now.getTime() - 86400000))) return `昨天 ${hm}`;
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

function human(err: unknown): string {
  return (err instanceof Error ? err.message : String(err)).replace(/^[a-z]+:\s*/i, "");
}

/** 工具条按钮：细边、低对比，只在 hover 时浮起来。 */
function toolBtn(title: string, path: string, onclick: () => void): HTMLElement {
  return h(
    "button",
    { class: "fbtn", type: "button", title, onclick },
    icon(path, 13, 1.7),
    h("span", {}, title),
  );
}

/** 行内图标按钮：只在 hover 时出现，避免每一行都长出一排噪音。 */
function rowBtn(title: string, path: string, danger: boolean, onclick: (e: MouseEvent) => void) {
  return h(
    "button",
    {
      class: "ficon",
      type: "button",
      title,
      dataset: danger ? { danger: "true" } : {},
      onclick: (e: MouseEvent) => {
        e.stopPropagation();
        onclick(e);
      },
    },
    icon(path, 13, 1.7),
  );
}

export function createFilePanel(opts: FilePanelOptions): FilePanel {
  let listing: SftpListing | null = null;
  let opened = false;
  let dead = false;
  const timers = new Set<number>();

  /** 排队一个可回收的延时任务：会话关闭时一并清掉，不留下打到已断开连接的回调。 */
  function later(fn: () => void, ms: number): void {
    const id = window.setTimeout(() => {
      timers.delete(id);
      fn();
    }, ms);
    timers.add(id);
  }

  /** 上报错误；会话已关闭时静默，不弹出指向死连接的对话框。 */
  function fail(message: string): void {
    if (!dead) opts.onError(message);
  }

  const pathEl = h("div", { class: "fbar__path" });
  const rowsEl = h("div", { class: "frows" });
  const txsEl = h("div", { class: "txs", hidden: true });
  const txEls = new Map<string, HTMLElement>();

  const refreshBtn = toolBtn("刷新", ICONS.refresh, () => void load());
  const upBtn = toolBtn("上一级", ICONS.chevronUp, () => {
    if (listing && listing.parent !== listing.path) void load(listing.parent);
  });

  const root = h(
    "div",
    { class: "fpanel" },
    h(
      "div",
      { class: "fbar" },
      h("div", { class: "fbar__nav" }, upBtn, refreshBtn),
      pathEl,
      h(
        "div",
        { class: "fbar__acts" },
        toolBtn("新建文件夹", ICONS.newFolder, () => promptNewFolder()),
        toolBtn("上传", ICONS.upload, () => void upload()),
      ),
    ),
    rowsEl,
    txsEl,
  );

  // ---------- 数据 ----------

  let inFlight = false;
  /** 上一次请求被挡在门外时要补看的目录；null 表示无需补刷。 */
  let rerun: { dir: string } | null = null;

  async function load(dir?: string): Promise<void> {
    const target = dir ?? listing?.path ?? "";
    if (dead) return;
    if (inFlight) {
      // ★ 不静默丢弃：删除/上传完成后如果正好有请求在飞，丢掉这次刷新
      //   就会把"旧列表"留在屏幕上。记下来，等在飞的那次结束再补一次。
      rerun = { dir: target };
      return;
    }
    inFlight = true;
    render();
    try {
      listing = await backend.sftpList(opts.sessionId, target);
    } catch (err) {
      fail(human(err));
    } finally {
      inFlight = false;
      render();
      const next = rerun;
      rerun = null;
      if (next) void load(next.dir);
    }
  }

  async function open(): Promise<void> {
    if (opened || dead) return;
    try {
      const info = await backend.sftpOpen(opts.sessionId);
      opened = true;
      await load(info.home);
    } catch (err) {
      fail(human(err));
    }
  }

  // ---------- 操作 ----------

  function promptName(
    title: string,
    subtitle: string,
    initial: string,
    submit: (name: string) => void,
  ): void {
    const input = h("input", {
      value: initial,
      placeholder: "名称",
      spellcheck: false,
      autocomplete: "off",
    });
    let dlg: { close(): void };
    const done = (): void => {
      const name = input.value.trim();
      if (!name) {
        input.focus();
        return;
      }
      dlg.close();
      submit(name);
    };
    dlg = openDialog({
      title,
      subtitle,
      iconPath: ICONS.folder,
      body: [field("名称", input, h("div", { class: "field__hint" }, "不能包含路径分隔符"))],
      footer: [
        h("button", { class: "btn", onclick: () => dlg.close() }, "取消"),
        h("button", { class: "btn", "data-variant": "primary", onclick: done }, "确定"),
      ],
    });
    input.addEventListener("keydown", (e: KeyboardEvent) => {
      if (e.key === "Enter") {
        e.preventDefault();
        done();
      }
    });
    requestAnimationFrame(() => {
      input.focus();
      input.select();
    });
  }

  function promptNewFolder(): void {
    if (!listing) return;
    promptName("新建文件夹", listing.path, "", (name) => {
      void backend
        .sftpMkdir(opts.sessionId, listing!.path, name)
        .then(() => load())
        .catch((err) => fail(human(err)));
    });
  }

  function promptRename(entry: FileEntry): void {
    if (!listing) return;
    promptName("重命名", listing.path, entry.name, (name) => {
      if (name === entry.name) return;
      void backend
        .sftpRename(opts.sessionId, entry.path, name)
        .then(() => load())
        .catch((err) => fail(human(err)));
    });
  }

  function confirmDelete(entry: FileEntry): void {
    const dlg = openDialog({
      title: entry.isDir ? "删除文件夹" : "删除文件",
      subtitle: entry.name,
      tone: "danger",
      iconPath: ICONS.warn,
      body: [
        h(
          "div",
          { class: "note", "data-tone": "danger" },
          entry.isDir
            ? "只删除空文件夹；非空时操作会被远端拒绝。此操作不可恢复。"
            : "此操作不可恢复。",
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
                .sftpDelete(opts.sessionId, entry.path, entry.isDir)
                .then(() => load())
                .catch((err) => fail(human(err)));
            },
          },
          "删除",
        ),
      ],
    });
  }

  async function upload(): Promise<void> {
    if (!listing) return;
    let paths: string[];
    try {
      paths = await backend.pickUploadFiles();
    } catch (err) {
      fail(human(err));
      return;
    }
    if (!paths.length) return;
    try {
      const sts = await backend.sftpUpload(opts.sessionId, listing.path, paths);
      for (const st of sts) onTransfer(st);
    } catch (err) {
      fail(human(err));
    }
  }

  async function download(entry: FileEntry): Promise<void> {
    let local: string;
    try {
      local = await backend.pickSavePath(entry.name);
    } catch (err) {
      fail(human(err));
      return;
    }
    if (!local) return;
    try {
      onTransfer(await backend.sftpDownload(opts.sessionId, entry.path, local));
    } catch (err) {
      fail(human(err));
    }
  }

  // ---------- 传输进度 ----------

  function dropTx(id: string): void {
    txEls.get(id)?.remove();
    txEls.delete(id);
    txsEl.hidden = txEls.size === 0;
  }

  function onTransfer(st: TransferStatus): void {
    if (dead) return;
    let row = txEls.get(st.id);
    if (!row) {
      row = h(
        "div",
        { class: "tx", dataset: { state: st.state, dir: st.direction } },
        h("div", { class: "tx__icon" }, icon(st.direction === "upload" ? ICONS.upload : ICONS.download, 13, 1.7)),
        h("div", { class: "tx__main" }, h("div", { class: "tx__name" }, st.name), h("div", { class: "tx__bar" })),
        h("div", { class: "tx__pct" }, "0%"),
        h(
          "button",
          {
            class: "ficon",
            type: "button",
            title: "取消",
            onclick: () => void backend.sftpCancelTransfer(st.id).catch(() => undefined),
          },
          icon(ICONS.close, 13, 1.7),
        ),
      );
      txEls.set(st.id, row);
      txsEl.append(row);
      txsEl.hidden = false;
    }

    row.dataset.state = st.state;
    const pct = st.total > 0 ? Math.min(100, Math.round((st.done / st.total) * 100)) : 0;
    const bar = row.querySelector<HTMLElement>(".tx__bar");
    if (bar) bar.style.setProperty("--pct", `${st.state === "done" ? 100 : pct}%`);
    const label = row.querySelector<HTMLElement>(".tx__pct");
    if (label) {
      label.textContent =
        st.state === "running"
          ? `${pct}%`
          : st.state === "done"
            ? "完成"
            : st.state === "cancelled"
              ? "已取消"
              : st.error || "失败";
    }

    if (st.state !== "running") {
      if (st.state === "done") void load();
      later(() => dropTx(st.id), st.state === "error" ? 8000 : 3500);
    }
  }

  // ---------- 渲染 ----------

  function entryRow(entry: FileEntry): HTMLElement {
    const kind = entry.isDir ? ICONS.folder : ICONS.file;
    return h(
      "div",
      {
        class: "frow",
        dataset: { dir: String(entry.isDir) },
        title: entry.path,
        ondblclick: () => {
          if (entry.isDir) void load(entry.path);
          else void download(entry);
        },
      },
      h(
        "div",
        { class: "frow__name" },
        icon(kind, 15, 1.55),
        h("span", { class: "frow__label" }, entry.name),
        entry.isSymlink ? h("span", { class: "frow__link" }, "链接") : null,
      ),
      h("div", { class: "frow__cell" }, formatSize(entry.size, entry.isDir)),
      h("div", { class: "frow__cell" }, formatWhen(entry.modTime)),
      h("div", { class: "frow__cell frow__mode" }, entry.mode),
      h(
        "div",
        { class: "frow__acts" },
        entry.isDir
          ? null
          : rowBtn("下载", ICONS.download, false, () => void download(entry)),
        rowBtn("重命名", ICONS.edit, false, () => promptRename(entry)),
        rowBtn("删除", ICONS.trash, true, () => confirmDelete(entry)),
      ),
    );
  }

  function render(): void {
    if (!listing) {
      pathEl.textContent = "";
      rowsEl.replaceChildren(
        h(
          "div",
          { class: "fempty" },
          icon(ICONS.folder, 30, 1.25),
          h("div", { class: "fempty__title" }, inFlight ? "正在打开远端目录…" : "还没有打开目录"),
        ),
      );
      return;
    }

    pathEl.textContent = listing.path;
    pathEl.title = listing.path;
    (upBtn as HTMLElement).toggleAttribute("disabled", listing.parent === listing.path);

    const head = h(
      "div",
      { class: "frow frow--head" },
      h("div", { class: "frow__name" }, "名称"),
      h("div", { class: "frow__cell" }, "大小"),
      h("div", { class: "frow__cell" }, "修改时间"),
      h("div", { class: "frow__cell frow__mode" }, "权限"),
      h("div", { class: "frow__acts" }),
    );

    const body = listing.entries.length
      ? listing.entries.map(entryRow)
      : [
          h(
            "div",
            { class: "fempty" },
            h("div", { class: "fempty__title" }, "这个目录是空的"),
            h(
              "div",
              { class: "fempty__hint" },
              "把文件拖进来是后续版本的事；现在可以点右上角「上传」。",
            ),
          ),
        ];

    rowsEl.replaceChildren(head, ...body);
  }

  render();

  return {
    root,
    open,
    onTransfer,
    dispose(): void {
      dead = true;
      for (const id of timers) window.clearTimeout(id);
      timers.clear();
      txEls.clear();
    },
  };
}
