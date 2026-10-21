// 私钥口令询问：后端在连接过程中通过 passphrase:prompt 事件发起。
//
// Retry 为 true 表示上一次口令不对——此时要把"重试"讲清楚，并保留已输入的内容，
// 否则用户会以为是另一把钥匙、重新走一遍握手。

import { h, ICONS } from "../ui/h";
import { openDialog, secretField } from "./Dialog";
import type { PassphrasePrompt } from "../ipc/api";

export function openPassphraseDialog(
  prompt: PassphrasePrompt,
  answer: (passphrase: string, cancelled: boolean) => void,
): void {
  let settled = false;
  const done = (passphrase: string, cancelled: boolean): void => {
    if (settled) return;
    settled = true;
    dlg.close();
    answer(passphrase, cancelled);
  };

  const fieldHandle = secretField("私钥口令", "输入用于解锁私钥的口令");

  const body = [
    prompt.retry
      ? h(
          "div",
          { class: "note", "data-tone": "danger" },
          "口令不正确，请重试。注意区分大小写；连续 3 次失败本次连接会被放弃。",
        )
      : h(
          "div",
          { class: "note" },
          "这把私钥受口令保护。口令会保存在系统凭据管理器，之后连接不再询问。",
        ),
    fieldHandle.wrap,
  ];

  const dlg = openDialog({
    title: prompt.retry ? "口令不正确" : "私钥需要口令",
    subtitle: prompt.keyName,
    tone: prompt.retry ? "danger" : "info",
    iconPath: prompt.retry ? ICONS.warn : ICONS.lock,
    body,
    footer: [
      h("button", { class: "btn", onclick: () => done("", true) }, "取消"),
      h(
        "button",
        { class: "btn", "data-variant": "primary", onclick: () => submit() },
        prompt.retry ? "重试" : "解锁",
      ),
    ],
    onDismiss: () => done("", true),
  });

  const submit = (): void => {
    const value = fieldHandle.input.value;
    if (!value) {
      fieldHandle.input.focus();
      return;
    }
    done(value, false);
  };

  fieldHandle.input.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter") submit();
  });
  setTimeout(() => fieldHandle.input.focus(), 30);
}
