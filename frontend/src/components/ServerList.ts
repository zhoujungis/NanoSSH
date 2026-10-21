// 服务器轨（左侧栏）：常驻可见，是应用的"脊柱"而非落地页。
// 打开应用的第一屏就是它——没有 dashboard、没有欢迎页。
//
// P2 起，轨道顶部的分段控件在「服务器 / 密钥」之间切换：轨道内容与舞台内容一起换，
// 但终端 pane 始终挂载（只是隐藏），所以切回来不用重连。

import { effect } from "@preact/signals-core";
import { h, ICONS, icon } from "../ui/h";
import { openMenu } from "../ui/menu";
import { flagIcon } from "./FlagIcon";
import { keyRow } from "./KeyView";
import type { KeyInfo, ServerInfo } from "../ipc/api";
import {
  dataDir,
  filteredKeys,
  filteredServers,
  query,
  railView,
  tabOfServer,
  version,
} from "../state/store";

export interface RailHandlers {
  onOpen(server: ServerInfo): void;
  onEdit(server: ServerInfo): void;
  onDelete(server: ServerInfo): void;
  onNewServer(): void;
  onNewKey(): void;
  onOpenKey(key: KeyInfo): void;
  onDeleteKey(key: KeyInfo): void;
  onCopyDataDir(): void;
}

export function createRail(hd: RailHandlers): HTMLElement {
  const list = h("div", { class: "rail__list" });

  const row = (server: ServerInfo): HTMLElement => {
    const session = tabOfServer.value.get(server.id);
    const state = session?.state ?? "idle";
    return h(
      "button",
      {
        class: "srv",
        dataset: {
          id: server.id,
          active: String(session != null),
          state,
          hasState: String(state !== "idle"),
        },
        title: `${server.name} · ${server.user}@${server.host}:${server.port}`,
        onclick: () => hd.onOpen(server),
        oncontextmenu: (e: MouseEvent) => {
          e.preventDefault();
          openMenu({ x: e.clientX, y: e.clientY }, [
            { label: "打开", onSelect: () => hd.onOpen(server) },
            { label: "编辑", onSelect: () => hd.onEdit(server) },
            { label: "删除", danger: true, onSelect: () => hd.onDelete(server) },
          ]);
        },
      },
      h("span", { class: "srv__flag" }, flagIcon(server.country)),
      h(
        "span",
        { class: "srv__main" },
        h("span", { class: "srv__name" }, server.name || server.host),
        h("span", { class: "srv__meta" }, `${server.user}@${server.host}:${server.port}`),
      ),
      h("span", { class: "srv__dot" }),
      h(
        "span",
        {
          class: "srv__more",
          onclick: (e: MouseEvent) => {
            e.stopPropagation();
            const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
            openMenu({ x: r.right - 150, y: r.bottom + 4 }, [
              { label: "编辑", onSelect: () => hd.onEdit(server) },
              { label: "删除", danger: true, onSelect: () => hd.onDelete(server) },
            ]);
          },
        },
        icon(ICONS.more, 13, 1.4),
      ),
    );
  };

  // ---- 分段切换（服务器 / 密钥） ----

  const segBtn = (view: "servers" | "keys", label: string): HTMLButtonElement =>
    h(
      "button",
      {
        class: "seg__item",
        dataset: { view },
        onclick: () => {
          if (railView.value === view) return;
          railView.value = view;
          query.value = "";
        },
      },
      label,
    );

  const segServers = segBtn("servers", "服务器");
  const segKeys = segBtn("keys", "密钥");

  const count = h("span", { class: "rail__count" });

  const search = h("input", {
    type: "search",
    placeholder: "搜索名称、主机或用户",
    spellcheck: "false",
    oninput: (e: Event) => {
      query.value = (e.target as HTMLInputElement).value;
    },
  });

  const versionEl = h("span", { class: "rail__ver" });
  const dirBtn = h("button", { class: "link", onclick: () => hd.onCopyDataDir() }, "复制配置目录");

  const addBtn = h(
    "button",
    { class: "icon-btn", onclick: () => (railView.value === "keys" ? hd.onNewKey() : hd.onNewServer()) },
    icon(ICONS.plus, 15),
  );

  const root = h(
    "aside",
    { class: "rail" },
    h(
      "div",
      { class: "rail__head" },
      h("div", { class: "seg" }, segServers, segKeys),
      count,
      h("span", { class: "rail__spacer" }),
      addBtn,
    ),
    h("div", { class: "rail__search" }, icon(ICONS.search, 14), search),
    list,
    h("div", { class: "rail__foot" }, dirBtn, h("span", { style: { flex: "1" } }), versionEl),
  );

  const render = (): void => {
    const view = railView.value;
    root.dataset.view = view;
    segServers.dataset.active = String(view === "servers");
    segKeys.dataset.active = String(view === "keys");
    addBtn.title = view === "keys" ? "生成或导入密钥" : "新建服务器（Ctrl+N）";
    search.placeholder = view === "keys" ? "搜索名称、算法或指纹" : "搜索名称、主机或用户";

    list.replaceChildren();

    if (view === "servers") {
      const items = filteredServers.value;
      count.textContent = String(items.length);
      if (items.length === 0) {
        list.append(
          h(
            "div",
            { class: "rail__empty" },
            query.value
              ? "没有匹配的服务器"
              : h("span", {}, "还没有服务器。", h("br"), "点右上角 + 添加第一台。"),
          ),
        );
        return;
      }
      for (const s of items) list.append(row(s));
      return;
    }

    const items = filteredKeys.value;
    count.textContent = String(items.length);
    if (items.length === 0) {
      list.append(
        h(
          "div",
          { class: "rail__empty" },
          query.value
            ? "没有匹配的密钥"
            : h("span", {}, "还没有密钥。", h("br"), "点右上角 + 生成一把，或导入已有的私钥。"),
        ),
      );
      return;
    }
    for (const k of items) list.append(keyRow(k, hd));
  };

  effect(render);
  effect(() => {
    versionEl.textContent = version.value;
    dirBtn.title = dataDir.value || "配置目录";
  });

  return root;
}
