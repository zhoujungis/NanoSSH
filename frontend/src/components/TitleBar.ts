// 自绘标题栏：品牌 + 当前位置面包屑 + 窗口控制。
// 拖拽由 Wails 的 --wails-draggable 机制接管（见 app.css 的 .titlebar），
// 交互元素必须显式声明 no-drag，否则点不动。

import { effect } from "@preact/signals-core";
import { h, ICONS, icon } from "../ui/h";
import { activeTab, tabs } from "../state/store";
import { windowCtl } from "../ipc/api";

function mark(): SVGElement {
  return icon(
    '<rect x="2.5" y="3.5" width="19" height="17" rx="3"/><path d="M6.5 9.5l3.5 3-3.5 3"/><path d="M12.5 15.5h5"/>',
    17,
    1.7,
  );
}

export function createTitleBar(): HTMLElement {
  const crumb = h("div", { class: "titlebar__crumb" });

  effect(() => {
    const id = activeTab.value;
    const tab = id ? tabs.value.find((t) => t.sessionId === id) : undefined;
    if (!tab) {
      crumb.replaceChildren(h("span", {}, "服务器列表"));
      return;
    }
    crumb.replaceChildren(
      h("span", {}, "服务器列表"),
      h("span", { class: "titlebar__sep" }, "/"),
      h("b", {}, tab.server.name || tab.server.host),
      h("span", { class: "titlebar__sep" }, "·"),
      h(
        "span",
        { style: { fontFamily: "var(--mono-font)" } },
        `${tab.server.user}@${tab.server.host}:${tab.server.port}`,
      ),
    );
  });

  const spacer = h("div", { class: "titlebar__spacer", style: "--wails-draggable: drag" });

  const controls = h(
    "div",
    { class: "wctl", style: "--wails-draggable: no-drag" },
    h("button", { "data-act": "min", title: "最小化", onclick: () => windowCtl.minimise() }, icon(ICONS.minimize, 13, 1.5)),
    h("button", { "data-act": "max", title: "最大化", onclick: () => windowCtl.toggleMaximise() }, icon(ICONS.maximize, 12, 1.4)),
    h("button", { "data-act": "close", title: "关闭", onclick: () => windowCtl.close() }, icon(ICONS.close, 13, 1.5)),
  );

  return h(
    "header",
    { class: "titlebar", style: "--wails-draggable: drag" },
    h(
      "div",
      { class: "titlebar__brand" },
      h("span", { style: { color: "var(--accent)" } }, mark()),
      h("span", { class: "titlebar__name" }, "NanoSSH"),
    ),
    crumb,
    spacer,
    controls,
  );
}
