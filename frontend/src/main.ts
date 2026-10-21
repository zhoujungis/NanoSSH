import "@xterm/xterm/css/xterm.css";
import "./styles/tokens.css";
import "./styles/app.css";
import { mountApp } from "./app";
import { backend } from "./ipc/api";

const root = document.getElementById("app");
if (!root) throw new Error("缺少 #app 挂载点");

// Win11 Mica：后端确认系统背景可用时，把标题栏/侧栏切成透出材质的玻璃表面。
void backend.micaEnabled().then((ok) => {
  if (ok) document.documentElement.classList.add("mica");
});

mountApp(root);
