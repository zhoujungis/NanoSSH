// 轻量浮层菜单（服务器行的"更多"操作）。
// 不用原生 contextmenu，视觉与主题统一；点击外部或按 Esc 关闭。

import { h } from "./h";

export interface MenuItem {
  label: string;
  danger?: boolean;
  onSelect(): void;
}

let current: HTMLElement | null = null;

export function closeMenu(): void {
  current?.remove();
  current = null;
}

export function openMenu(at: { x: number; y: number }, items: MenuItem[]): void {
  closeMenu();

  const el = h(
    "div",
    { class: "menu" },
    ...items.map((it) =>
      h(
        "button",
        {
          class: "menu__item",
          dataset: { danger: it.danger ? "true" : "false" },
          onclick: () => {
            closeMenu();
            it.onSelect();
          },
        },
        it.label,
      ),
    ),
  );

  document.body.append(el);
  const rect = el.getBoundingClientRect();
  el.style.left = `${Math.min(at.x, window.innerWidth - rect.width - 8)}px`;
  el.style.top = `${Math.min(at.y, window.innerHeight - rect.height - 8)}px`;

  current = el;

  const dismiss = (e: Event): void => {
    if (e.target instanceof Node && el.contains(e.target)) return;
    closeMenu();
    detach();
  };
  const onKey = (e: KeyboardEvent): void => {
    if (e.key === "Escape") {
      closeMenu();
      detach();
    }
  };
  const detach = (): void => {
    document.removeEventListener("mousedown", dismiss, true);
    document.removeEventListener("keydown", onKey, true);
    window.removeEventListener("blur", () => closeMenu(), true);
  };

  document.addEventListener("mousedown", dismiss, true);
  document.addEventListener("keydown", onKey, true);
  window.addEventListener("blur", closeMenu, { once: true });
}
