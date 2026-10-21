// 新建 / 编辑服务器。
// P2 起三种认证方式全部开放：密码、密钥、ssh-agent。
// 选"密钥"时必须指定用哪一把——否则连接时才报错，属于把问题推给用户。

import { h, ICONS } from "../ui/h";
import { field, openDialog } from "./Dialog";
import { backend, type AuthMethod, type ServerInfo } from "../ipc/api";
import { keys } from "../state/store";

const COUNTRIES = ["US", "JP", "SG", "HK", "DE", "NL", "GB", "FR", "KR", "TW", "CA", "AU", "RU", "CN"];

const AUTH_LABEL: Record<AuthMethod, string> = {
  password: "密码",
  key: "密钥",
  agent: "ssh-agent",
};

export function openServerDialog(
  initial: ServerInfo | null,
  onSave: (value: Partial<ServerInfo>) => void,
): void {
  const name = h("input", { value: initial?.name ?? "", placeholder: "例如 VPS-01", spellcheck: "false" });
  const host = h("input", { value: initial?.host ?? "", placeholder: "203.0.113.24", spellcheck: "false" });
  const port = h("input", { value: String(initial?.port ?? 22), inputmode: "numeric" });
  const user = h("input", { value: initial?.user ?? "", placeholder: "root", spellcheck: "false" });

  const country = h("input", {
    value: initial?.country ?? "",
    placeholder: "US",
    maxlength: "2",
    list: "nanossh-country-list",
    style: { textTransform: "uppercase" },
    spellcheck: "false",
  });
  const datalist = h(
    "datalist",
    { id: "nanossh-country-list" },
    ...COUNTRIES.map((c) => h("option", { value: c })),
  );

  // ---- 认证方式 ----
  const auth = h(
    "select",
    {},
    h("option", { value: "password" }, AUTH_LABEL.password),
    h("option", { value: "key" }, AUTH_LABEL.key),
    h("option", { value: "agent" }, AUTH_LABEL.agent),
  );
  auth.value = initial?.auth ?? "password";

  // 密钥选择器：列出已管理的密钥，名称 + 算法。
  const keySelect = h(
    "select",
    {},
    ...keys.value.map((k) => h("option", { value: k.id }, `${k.name}（${k.type}）`)),
  );
  if (initial?.keyId) keySelect.value = initial.keyId;

  const keyHint = h("span", { class: "field__hint" });
  const keyField = field("使用的密钥", keySelect, keyHint);

  const agentNote = h(
    "div",
    { class: "note" },
    "连接时会直接使用系统 ssh-agent 中已加载的密钥。请确认 agent 正在运行，且已用 ssh-add 添加过密钥。",
  );

  // "清除已保存密码"只在编辑态、密码认证、且确实存过密码时出现。
  const passNote = h("div", { class: "note passnote" });
  passNote.hidden = true;

  const sectionAuth = h("div", { class: "authextra" }, keyField, agentNote, passNote);

  const syncAuth = (): void => {
    const mode = auth.value as AuthMethod;
    keyField.hidden = mode !== "key";
    agentNote.hidden = mode !== "agent";
    if (mode === "key") {
      if (keys.value.length === 0) {
        keyHint.textContent = "还没有密钥，请先到「密钥」页生成或导入一把。";
        keyHint.dataset.tone = "warn";
      } else {
        keyHint.textContent = "私钥口令（若有）由 NanoSSH 代为询问，无需在此填写。";
        delete keyHint.dataset.tone;
      }
    }
    passNote.hidden = !(mode === "password" && hasSavedPassword);
  };
  auth.addEventListener("change", syncAuth);

  let hasSavedPassword = false;
  if (initial?.id) {
    void backend
      .hasPassword(initial.id)
      .then((v) => {
        hasSavedPassword = v;
        if (v) {
          passNote.replaceChildren(
            "已在此设备保存密码。",
            h(
              "button",
              {
                class: "link",
                onclick: async () => {
                  await backend.forgetPassword(initial.id!);
                  hasSavedPassword = false;
                  syncAuth();
                },
              },
              "清除",
            ),
          );
        }
        syncAuth();
      })
      .catch(() => {});
  }

  // ---- 校验 ----
  const errorEl = h("div", { class: "note", "data-tone": "danger" });
  errorEl.hidden = true;
  const showError = (msg: string): void => {
    errorEl.textContent = msg;
    errorEl.hidden = false;
  };

  const submit = (): void => {
    errorEl.hidden = true;
    const mode = auth.value as AuthMethod;
    if (!host.value.trim() || !user.value.trim()) {
      showError("主机与用户名不能为空。");
      return;
    }
    if (mode === "key" && !keySelect.value) {
      showError("请选择用于登录的密钥。");
      return;
    }
    dlg.close();
    onSave({
      id: initial?.id,
      name: name.value.trim() || host.value.trim(),
      host: host.value.trim(),
      port: Number(port.value) || 22,
      user: user.value.trim(),
      country: country.value.trim().toUpperCase(),
      auth: mode,
      keyId: mode === "key" ? keySelect.value : "",
    });
  };

  const dlg = openDialog({
    title: initial ? "编辑服务器" : "新建服务器",
    subtitle: initial ? initial.id : "密码与私钥口令保存在系统凭据管理器，不写进 servers.json",
    iconPath: ICONS.server,
    body: [
      field("名称", name),
      h("div", { class: "field__row" }, field("主机", host), field("端口", port)),
      h("div", { class: "field__row" }, field("用户名", user), field("国家码", country, datalist)),
      field("认证方式", auth),
      sectionAuth,
      errorEl,
    ],
    footer: [
      h("button", { class: "btn", onclick: () => dlg.close() }, "取消"),
      h("button", { class: "btn", "data-variant": "primary", onclick: submit }, initial ? "保存" : "添加"),
    ],
  });

  syncAuth();

  for (const el of [name, host, port, user, country]) {
    el.addEventListener("keydown", (e: KeyboardEvent) => {
      if (e.key === "Enter") submit();
    });
  }
  name.focus();
}

/** 密码输入：服务器配置为密码认证且尚未保存密码时弹出。 */
export function openPasswordDialog(
  server: ServerInfo,
  onSubmit: (password: string) => void,
): void {
  const input = h("input", {
    type: "password",
    placeholder: "输入登录密码",
    autocomplete: "off",
  });

  const submit = (): void => {
    if (!input.value) return;
    dlg.close();
    onSubmit(input.value);
  };

  const dlg = openDialog({
    title: "需要密码",
    subtitle: `${server.user}@${server.host}:${server.port}`,
    iconPath: ICONS.shield,
    body: [
      field("密码", input),
      h("div", { class: "note" }, "认证成功后密码会存入系统凭据管理器，下次连接不再询问。"),
    ],
    footer: [
      h("button", { class: "btn", onclick: () => dlg.close() }, "取消"),
      h("button", { class: "btn", "data-variant": "primary", onclick: submit }, "连接"),
    ],
  });

  input.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter") submit();
  });
  setTimeout(() => input.focus(), 30);
}
