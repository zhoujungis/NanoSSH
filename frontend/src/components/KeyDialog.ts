// 密钥对话框：生成新密钥 / 导入已有私钥。
//
// 两种模式共用一张卡片，用顶部分段控件切换。口令是可选字段——留空即明文私钥；
// 填了则由后端存进系统凭据管理器，之后连接时不会再问。
// 导入遇到"私钥已加密"时错误就地显示，用户补上口令即可重试，不必从头再来。

import { h, ICONS, icon } from "../ui/h";
import { field, openDialog, secretField, type DialogHandle } from "./Dialog";
import { backend, type KeyAlgo, type KeyInfo } from "../ipc/api";

const ALGOS: { value: KeyAlgo; label: string; hint: string }[] = [
  { value: "ed25519", label: "Ed25519", hint: "推荐 · 最短最快，现代服务端首选" },
  { value: "rsa", label: "RSA", hint: "兼容更老的服务端，密钥更大" },
  { value: "ecdsa", label: "ECDSA", hint: "NIST P-256 曲线" },
];

/** 去掉后端错误里的 "key: " / "connect: " 前缀，只留可读的部分。 */
function humanError(err: unknown): string {
  const raw = err instanceof Error ? err.message : String(err);
  return raw.replace(/^[a-z]+:\s*/i, "");
}

export function openKeyDialog(onCreated: (key: KeyInfo) => void): void {
  let mode: "generate" | "import" = "generate";
  let busy = false;

  // ---- 生成表单 ----
  const genName = h("input", { placeholder: "例如 工作机 ed25519", spellcheck: "false" });
  const genComment = h("input", { placeholder: "默认与名称相同", spellcheck: "false" });
  const genPass = secretField("私钥口令（可选）", "留空则生成无口令私钥");

  let algo: KeyAlgo = "ed25519";
  const algoHint = h("span", { class: "field__hint" }, ALGOS[0].hint);
  const algoBtns = ALGOS.map((a) =>
    h(
      "button",
      {
        class: "seg__item",
        type: "button",
        dataset: { algo: a.value, active: String(a.value === algo) },
        title: a.hint,
        onclick: () => {
          algo = a.value;
          for (const b of algoBtns) b.dataset.active = String(b.dataset.algo === algo);
          algoHint.textContent = ALGOS.find((x) => x.value === algo)?.hint ?? "";
        },
      },
      a.label,
    ),
  );

  const genForm = h(
    "div",
    { class: "keyform" },
    field("名称", genName),
    field("算法", h("div", { class: "seg" }, ...algoBtns), algoHint),
    field("备注（写进公钥行尾）", genComment),
    genPass.wrap,
  );

  // ---- 导入表单 ----
  const srcPath = h("input", { placeholder: "尚未选择文件", readonly: true, spellcheck: "false" });
  const impName = h("input", { placeholder: "留空则取文件名", spellcheck: "false" });
  const impPass = secretField("私钥口令（可选）", "受口令保护的私钥才需要填写");

  const pickBtn = h(
    "button",
    {
      class: "btn",
      type: "button",
      onclick: async () => {
        try {
          const p = await backend.pickKeyFile();
          if (!p) return;
          srcPath.value = p;
          if (!impName.value) impName.value = p.split(/[\\/]/).pop() ?? "";
          clearError();
        } catch (err) {
          showError(humanError(err));
        }
      },
    },
    icon(ICONS.folder, 13.5, 1.6),
    "选择文件",
  );

  const impForm = h(
    "div",
    { class: "keyform" },
    field("私钥文件", h("div", { class: "pickrow" }, srcPath, pickBtn)),
    field("名称", impName),
    impPass.wrap,
    h(
      "div",
      { class: "note" },
      "私钥会被复制进 NanoSSH 的密钥目录保存，原文件不受影响。导入前会先校验它确实可解，避免留下坏记录。",
    ),
  );
  impForm.hidden = true;

  // ---- 错误显示 ----
  const errorEl = h("div", { class: "note", "data-tone": "danger" });
  errorEl.hidden = true;
  function clearError(): void {
    errorEl.hidden = true;
    errorEl.textContent = "";
  }
  function showError(msg: string): void {
    errorEl.textContent = msg;
    errorEl.hidden = false;
  }

  // ---- 提交 ----
  const submit = h("button", { class: "btn", "data-variant": "primary" }, "生成");
  const cancel = h("button", { class: "btn", onclick: () => dlg.close() }, "取消");

  async function run(): Promise<void> {
    if (busy) return;
    clearError();

    if (mode === "generate") {
      const name = genName.value.trim();
      if (!name) {
        showError("请填写密钥名称。");
        genName.focus();
        return;
      }
      busy = true;
      submit.textContent = "生成中…";
      try {
        const key = await backend.generateKey({
          name,
          algo,
          comment: genComment.value.trim() || undefined,
          passphrase: genPass.input.value || undefined,
        });
        dlg.close();
        onCreated(key);
      } catch (err) {
        showError(humanError(err));
      } finally {
        busy = false;
        submit.textContent = "生成";
      }
      return;
    }

    if (!srcPath.value.trim()) {
      showError("请先选择私钥文件。");
      return;
    }
    busy = true;
    submit.textContent = "导入中…";
    try {
      const key = await backend.importKey({
        name: impName.value.trim() || undefined,
        sourcePath: srcPath.value.trim(),
        passphrase: impPass.input.value || undefined,
      });
      dlg.close();
      onCreated(key);
    } catch (err) {
      const msg = humanError(err);
      const needsPass = msg.includes("口令") || msg.includes("加密");
      showError(needsPass ? `${msg}。请填写私钥口令后重试。` : msg);
      if (needsPass) impPass.input.focus();
    } finally {
      busy = false;
      submit.textContent = "导入";
    }
  }

  submit.onclick = () => void run();

  // ---- 模式切换 ----
  const segGen = h("button", { class: "seg__item", dataset: { active: "true" } }, icon(ICONS.plus, 13, 1.8), "生成");
  const segImp = h("button", { class: "seg__item" }, icon(ICONS.import, 13, 1.8), "导入");

  const dlg: DialogHandle = openDialog({
    title: "生成密钥",
    subtitle: "私钥保存在 NanoSSH 的配置目录，公钥贴到服务端即可免密登录",
    iconPath: ICONS.key,
    body: [h("div", { class: "seg seg--wide" }, segGen, segImp), genForm, impForm, errorEl],
    footer: [cancel, submit],
  });

  const titleEl = dlg.el.querySelector<HTMLElement>(".dialog__title")!;

  const setMode = (next: "generate" | "import"): void => {
    mode = next;
    segGen.dataset.active = String(next === "generate");
    segImp.dataset.active = String(next === "import");
    genForm.hidden = next !== "generate";
    impForm.hidden = next !== "import";
    titleEl.textContent = next === "generate" ? "生成密钥" : "导入私钥";
    submit.textContent = next === "generate" ? "生成" : "导入";
    clearError();
    (next === "generate" ? genName : srcPath).focus();
  };
  segGen.onclick = () => setMode("generate");
  segImp.onclick = () => setMode("import");

  for (const input of [genName, genComment, impName]) {
    input.addEventListener("keydown", (e: KeyboardEvent) => {
      if (e.key === "Enter") void run();
    });
  }
  genName.focus();
}
