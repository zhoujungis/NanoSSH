// 通用对话框骨架：遮罩 + 卡片 + 头部 + 正文 + 页脚。
// 主机密钥卡、新建/编辑服务器、密码输入都复用它，保证视觉一致。

import { h, ICONS, icon } from "../ui/h";

export interface DialogOptions {
  title: string;
  subtitle?: string;
  tone?: "info" | "danger";
  iconPath?: string;
  body: Node[];
  footer: Node[];
  /** 点遮罩 / 按 Esc 时触发，通常是"取消"。 */
  onDismiss?: () => void;
}

export interface DialogHandle {
  /** 对话框卡片元素，便于调用方 querySelector 取输入框。 */
  el: HTMLElement;
  close(): void;
}

export function openDialog(opts: DialogOptions): DialogHandle {
  const onKey = (e: KeyboardEvent): void => {
    if (e.key === "Escape") {
      close();
      opts.onDismiss?.();
    }
  };

  const dismiss = (): void => {
    close();
    opts.onDismiss?.();
  };

  const card = h(
    "div",
    { class: "dialog", dataset: { tone: opts.tone ?? "info" } },
    h(
      "div",
      { class: "dialog__head" },
      h("div", { class: "dialog__icon" }, icon(opts.iconPath ?? ICONS.shield, 16)),
      h(
        "div",
        { class: "dialog__heading" },
        h("div", { class: "dialog__title" }, opts.title),
        opts.subtitle
          ? h("div", { class: "dialog__sub" }, opts.subtitle)
          : null,
      ),
    ),
    h("div", { class: "dialog__body" }, ...opts.body),
    h("div", { class: "dialog__foot" }, ...opts.footer),
  );

  const scrim = h("div", { class: "scrim" }, card);
  scrim.addEventListener("mousedown", (e) => {
    if (e.target === scrim) dismiss();
  });
  document.addEventListener("keydown", onKey, true);
  document.body.append(scrim);

  function close(): void {
    document.removeEventListener("keydown", onKey, true);
    scrim.remove();
  }

  return { el: card, close };
}

/** 表单字段：标签 + 控件。 */
export function field(label: string, control: HTMLElement, extra?: Node): HTMLElement {
  return h(
    "div",
    { class: "field" },
    h("label", { class: "field__label" }, label),
    control,
    extra ?? null,
  );
}

/** 口令输入：附带眼睛开关，避免口令被旁人读到。 */
export function secretField(
  label: string,
  placeholder: string,
): { wrap: HTMLElement; input: HTMLInputElement } {
  const input = h("input", { type: "password", placeholder, autocomplete: "new-password" });
  const eye = h(
    "button",
    {
      class: "reveal",
      type: "button",
      title: "显示口令",
      onclick: () => {
        const shown = input.type === "text";
        input.type = shown ? "password" : "text";
        eye.replaceChildren(icon(shown ? ICONS.eye : ICONS.eyeOff, 14, 1.6));
        eye.title = shown ? "显示口令" : "隐藏口令";
      },
    },
    icon(ICONS.eye, 14, 1.6),
  );
  return { wrap: field(label, h("div", { class: "secret" }, input, eye)), input };
}
