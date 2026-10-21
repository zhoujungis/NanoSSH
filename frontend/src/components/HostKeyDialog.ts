// 主机密钥卡片：首次连接（TOFU）与指纹变化两种形态。
//
// ★ 指纹变化是硬阻断：没有"总是忽略"，用户必须显式选择"替换记录"。
//   这是 SSH 客户端最容易被做错、也最不该妥协的一处。

import { h, ICONS } from "../ui/h";
import { openDialog } from "./Dialog";
import type { HostKeyChoice, HostKeyPrompt } from "../ipc/api";

function fingerprint(label: string, value: string, tone?: string): HTMLElement {
  return h(
    "div",
    { class: "fingerprint", dataset: tone ? { tone } : {} },
    h("span", { class: "fingerprint__tag" }, label),
    h("span", { class: "fingerprint__val" }, value),
  );
}

export function openHostKeyDialog(
  prompt: HostKeyPrompt,
  resolve: (choice: HostKeyChoice) => void,
): void {
  let settled = false;
  const done = (choice: HostKeyChoice): void => {
    if (settled) return;
    settled = true;
    dlg.close();
    resolve(choice);
  };

  const body = prompt.changed
    ? [
        h(
          "div",
          { class: "note", "data-tone": "danger" },
          "该主机在 known_hosts 中的记录与本次出示的密钥不一致。这可能是服务端更换了密钥，也可能是中间人攻击。请通过带外渠道核对指纹后再决定。",
        ),
        ...(prompt.oldFingerprints ?? []).map((f) => fingerprint("原记录", f, "old")),
        fingerprint("本次出示", prompt.fingerprint, "new"),
      ]
    : [
        h(
          "div",
          { class: "note" },
          "这是第一次连接到该主机。请核对指纹与服务端一致（可用 ssh-keygen -lf 交叉验证）。",
        ),
        fingerprint(prompt.keyType, prompt.fingerprint),
      ];

  const footer = prompt.changed
    ? [
        h("button", { class: "btn", onclick: () => done("reject") }, "取消连接"),
        h(
          "button",
          { class: "btn", "data-variant": "danger", onclick: () => done("save") },
          "替换记录并连接",
        ),
      ]
    : [
        h("button", { class: "btn", onclick: () => done("reject") }, "取消"),
        h("button", { class: "btn", onclick: () => done("once") }, "仅本次"),
        h(
          "button",
          { class: "btn", "data-variant": "primary", onclick: () => done("save") },
          "信任并保存",
        ),
      ];

  const dlg = openDialog({
    title: prompt.changed ? "主机密钥已变化" : "未知主机",
    subtitle: `${prompt.host}:${prompt.port}`,
    tone: prompt.changed ? "danger" : "info",
    iconPath: prompt.changed ? ICONS.warn : ICONS.shield,
    body,
    footer,
    onDismiss: () => done("reject"),
  });
}
