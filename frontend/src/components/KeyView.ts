// 密钥视图：轨道里的密钥列表 + 舞台上的密钥详情。
//
// 设计意图与服务器视图一致——轨道是索引，舞台是内容。选中一把钥匙即选中，
// 不需要"编辑页"：详情里的公钥、指纹、路径都是只读事实，唯一动作是复制与删除。

import { effect } from "@preact/signals-core";
import { h, ICONS, icon } from "../ui/h";
import { openMenu } from "../ui/menu";
import { windowCtl } from "../ipc/api";
import type { KeyInfo } from "../ipc/api";
import { activeKey, keyById, keys, servers } from "../state/store";

export interface KeyRowHandlers {
  onOpenKey(key: KeyInfo): void;
  onDeleteKey(key: KeyInfo): void;
}

const ALGO_LABEL: Record<string, string> = {
  ed25519: "Ed25519",
  rsa: "RSA",
  ecdsa: "ECDSA",
};

function algoLabel(type: string): string {
  return ALGO_LABEL[type.toLowerCase()] ?? type.toUpperCase();
}

/** 把 SHA256:xxx 缩成 SHA256:qB7wK3n1…N3oQ，给列表用。 */
function shortFp(fp: string): string {
  const body = fp.includes(":") ? fp.slice(fp.indexOf(":") + 1) : fp;
  if (body.length <= 18) return fp;
  return `SHA256:${body.slice(0, 8)}…${body.slice(-4)}`;
}

/** 复制按钮：点击后短暂变成"已复制"，给出确定的反馈而不是静默。 */
function copyBtn(getText: () => string, label = "复制"): HTMLButtonElement {
  const btn = h(
    "button",
    { class: "copybtn", type: "button", title: "复制到剪贴板" },
    icon(ICONS.copy, 12.5, 1.7),
    h("span", {}, label),
  );
  let timer = 0;
  btn.addEventListener("click", () => {
    void windowCtl.copyText(getText());
    btn.dataset.copied = "true";
    btn.replaceChildren(icon(ICONS.check, 12.5, 1.9), h("span", {}, "已复制"));
    window.clearTimeout(timer);
    timer = window.setTimeout(() => {
      btn.dataset.copied = "false";
      btn.replaceChildren(icon(ICONS.copy, 12.5, 1.7), h("span", {}, label));
    }, 2000);
  });
  return btn;
}

/** 轨道中的一行密钥。activeKey 在这里读取，让外层 effect 自动跟随选中项。 */
export function keyRow(key: KeyInfo, hd: KeyRowHandlers): HTMLElement {
  return h(
    "button",
    {
      class: "krow",
      dataset: { id: key.id, active: String(activeKey.value === key.id) },
      title: `${key.name} · ${key.type}`,
      onclick: () => hd.onOpenKey(key),
      oncontextmenu: (e: MouseEvent) => {
        e.preventDefault();
        openMenu({ x: e.clientX, y: e.clientY }, [
          {
            label: "复制公钥",
            onSelect: () => void windowCtl.copyText(key.publicKey ?? ""),
          },
          { label: "删除", danger: true, onSelect: () => hd.onDeleteKey(key) },
        ]);
      },
    },
    h("span", { class: "krow__badge" }, icon(ICONS.key, 13, 1.6)),
    h(
      "span",
      { class: "krow__main" },
      h("span", { class: "krow__name" }, key.name),
      h("span", { class: "krow__meta" }, `${algoLabel(key.type)} · ${shortFp(key.fingerprint)}`),
    ),
    h(
      "span",
      {
        class: "krow__more",
        onclick: (e: MouseEvent) => {
          e.stopPropagation();
          const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
          openMenu({ x: r.right - 150, y: r.bottom + 4 }, [
            { label: "复制公钥", onSelect: () => void windowCtl.copyText(key.publicKey ?? "") },
            { label: "删除", danger: true, onSelect: () => hd.onDeleteKey(key) },
          ]);
        },
      },
      icon(ICONS.more, 13, 1.4),
    ),
  );
}

// ---------------- 舞台 ----------------

export interface KeyStageOptions {
  onDelete(key: KeyInfo): void;
}

function section(label: string, hint: string, value: string, opts?: { block?: boolean }): HTMLElement {
  return h(
    "section",
    { class: "ksec" },
    h(
      "div",
      { class: "ksec__label" },
      label,
      hint ? h("span", { class: "ksec__hint" }, hint) : null,
    ),
    h(
      "div",
      { class: "ksec__row", dataset: { block: String(opts?.block ?? false) } },
      h("code", { class: "ksec__val", title: value }, value),
      copyBtn(() => value),
    ),
  );
}

export function createKeyStage(opts: KeyStageOptions): HTMLElement {
  const root = h("main", { class: "stage stage--key" });

  const render = (): void => {
    const list = keys.value;
    const key = activeKey.value ? keyById.value.get(activeKey.value) : undefined;

    if (!key) {
      root.replaceChildren(
        h(
          "div",
          { class: "empty" },
          h("div", { class: "empty__mark" }, icon(ICONS.key, 34, 1.2)),
          h("div", { class: "empty__title" }, list.length ? "选一把密钥查看详情" : "还没有密钥"),
          h(
            "div",
            { class: "empty__hint" },
            list.length
              ? "左侧是密钥列表，单击即选中。"
              : h(
                  "span",
                  {},
                  "点右上角 ",
                  h("kbd", {}, "+"),
                  " 生成一把新密钥，或导入已有的私钥文件。",
                  h("br"),
                  "生成后把公钥贴到服务端的 authorized_keys 即可免密登录。",
                ),
          ),
        ),
      );
      return;
    }

    const usedBy = servers.value.filter((s) => s.keyId === key.id);

    root.replaceChildren(
      h(
        "div",
        { class: "keydoc" },
        h(
          "article",
          { class: "keycard" },
          h(
            "header",
            { class: "keycard__head" },
            h("div", { class: "keycard__badge" }, icon(ICONS.key, 18, 1.5)),
            h(
              "div",
              { class: "keycard__id" },
              h("div", { class: "keycard__name", title: key.name }, key.name),
              h(
                "div",
                { class: "keycard__meta" },
                h("span", { class: "chip" }, algoLabel(key.type)),
                usedBy.length
                  ? h("span", { class: "keycard__use" }, `被 ${usedBy.length} 台服务器使用`)
                  : h("span", { class: "keycard__use", "data-tone": "muted" }, "尚未被任何服务器使用"),
              ),
            ),
            h(
              "button",
              { class: "btn", "data-variant": "danger", onclick: () => opts.onDelete(key) },
              icon(ICONS.trash, 13.5, 1.6),
              "删除",
            ),
          ),
          section("指纹", "与 ssh-keygen -lf 输出一致", key.fingerprint),
          section(
            "公钥",
            "粘贴到服务端 ~/.ssh/authorized_keys",
            key.publicKey ?? "（后端未提供公钥行）",
            { block: true },
          ),
          section("私钥文件", "由 NanoSSH 管理，可直接与 ~/.ssh 互拷", key.path),
          h(
            "footer",
            { class: "keycard__foot" },
            icon(ICONS.lock, 13, 1.6),
            h(
              "span",
              {},
              "私钥以 OpenSSH 格式落盘于配置目录，权限随用户目录 ACL；密钥口令保存在系统凭据管理器。",
            ),
          ),
        ),
      ),
    );
  };

  effect(render);
  return root;
}
