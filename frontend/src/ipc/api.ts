// 前端唯一的后端访问层。
//
// ★ 架构约束：views / term / components 一律不直接触碰 window.go；
//   所有绑定调用与事件订阅都从这里出去。日后把外壳从 Wails 换成裸 WebView2 时，
//   需要重写的只有本文件与 transport.ts。
//
// 开发态兜底：浏览器里（无 window.go）自动挂载 mock 后端，便于用普通浏览器
// 校验界面与终端渲染。生产构建中 mock 会被 Vite 静态剔除。

import { createMockBackend } from "./mock";

export type AuthMethod = "password" | "key" | "agent";

export interface ServerInfo {
  id: string;
  name: string;
  country?: string;
  host: string;
  port: number;
  user: string;
  auth: AuthMethod;
  keyId?: string;
  terminal?: string;
}

export interface ConnectResult {
  sessionId: string;
  server: ServerInfo;
}

export interface ShellInfo {
  sessionId: string;
  rows: number;
  cols: number;
}

export interface HostKeyPrompt {
  requestId: string;
  host: string;
  port: number;
  keyType: string;
  fingerprint: string;
  changed: boolean;
  oldFingerprints?: string[];
}

export type HostKeyChoice = "once" | "save" | "reject";

/** 一把被管理的私钥。对应后端 sshcore.KeyRef。 */
export interface KeyInfo {
  id: string;
  name: string;
  /** 私钥文件的绝对路径。 */
  path: string;
  /** 算法名：ed25519 / rsa / ecdsa。 */
  type: string;
  /** SHA256:... 指纹，与 ssh-keygen -lf 输出一致。 */
  fingerprint: string;
  /** authorized_keys 格式的单行公钥。 */
  publicKey?: string;
}

export type KeyAlgo = "ed25519" | "rsa" | "ecdsa";

export interface GenerateKeyRequest {
  name: string;
  algo?: KeyAlgo;
  comment?: string;
  /** 非空则私钥受口令保护，并由后端存入凭据管理器。 */
  passphrase?: string;
}

export interface ImportKeyRequest {
  name?: string;
  sourcePath: string;
  passphrase?: string;
}

/** 后端在连接过程中索要私钥口令。 */
export interface PassphrasePrompt {
  requestId: string;
  keyId: string;
  keyName: string;
  /** 上一次口令不正确，前端应提示重试而非当作首次询问。 */
  retry: boolean;
}

/** 远端文件条目。对应后端 sshcore.FileEntry。 */
export interface FileEntry {
  name: string;
  /** 绝对路径，可直接回传给后续操作。 */
  path: string;
  size: number;
  /** 权限串，形如 drwxr-xr-x。 */
  mode: string;
  /** 最近修改时间，Unix 毫秒。 */
  modTime: number;
  isDir: boolean;
  isSymlink: boolean;
}

export interface SftpListing {
  path: string;
  /** 上一级目录，驱动"上一级"按钮。 */
  parent: string;
  entries: FileEntry[];
}

export interface SftpInfo {
  /** 远端家目录，文件面板的起始位置。 */
  home: string;
}

export type TransferDirection = "upload" | "download";
export type TransferState = "running" | "done" | "error" | "cancelled";

export type ForwardState = "running" | "stopped" | "error" | "removed";

/** 一条本地端口转发规则（ssh -L 语义）。对应后端 ForwardRule。 */
export interface ForwardRule {
  id: string;
  sessionId: string;
  /** 本地监听端口（只绑 127.0.0.1）。 */
  listenPort: number;
  remoteHost: string;
  remotePort: number;
  state: ForwardState;
  error?: string;
}

/** 转发规则的状态快照，经 forward:status 事件推送。 */
export interface ForwardStatus extends ForwardRule {
  /** 当前活动连接数。 */
  conns: number;
  /** 本条监听累计接受的连接数。 */
  totalConns: number;
  /** 远端→本地流量字节数。 */
  bytesIn: number;
  /** 本地→远端流量字节数。 */
  bytesOut: number;
}

/** 一次传输的进度快照，经 sftp:transfer 事件推送。 */
export interface TransferStatus {
  id: string;
  sessionId: string;
  direction: TransferDirection;
  name: string;
  remote: string;
  local: string;
  total: number;
  done: number;
  state: TransferState;
  error?: string;
}

export interface Backend {
  version(): Promise<string>;
  dataDir(): Promise<string>;
  listServers(): Promise<ServerInfo[]>;
  saveServer(s: Partial<ServerInfo>): Promise<ServerInfo>;
  deleteServer(id: string): Promise<void>;
  connect(req: { serverId: string; password?: string }): Promise<ConnectResult>;
  hostKeyDecision(requestId: string, decision: HostKeyChoice): Promise<void>;
  /** 该服务器是否已有保存的密码（Windows 凭据管理器）。 */
  hasPassword(serverId: string): Promise<boolean>;
  /** 清除已保存的密码。 */
  forgetPassword(serverId: string): Promise<void>;
  /** 回复私钥口令询问；cancelled 为 true 表示用户放弃。 */
  passphraseDecision(requestId: string, passphrase: string, cancelled: boolean): Promise<void>;
  listKeys(): Promise<KeyInfo[]>;
  generateKey(req: GenerateKeyRequest): Promise<KeyInfo>;
  importKey(req: ImportKeyRequest): Promise<KeyInfo>;
  deleteKey(id: string): Promise<void>;
  /** 弹出系统文件选择器，返回私钥路径；空串表示取消。 */
  pickKeyFile(): Promise<string>;
  startShell(req: { sessionId: string; rows: number; cols: number }): Promise<ShellInfo>;
  writeStdin(sessionId: string, data: string): Promise<void>;
  resizeShell(sessionId: string, rows: number, cols: number): Promise<void>;
  ackTermData(sessionId: string): Promise<void>;
  closeSession(sessionId: string): Promise<void>;
  /** 在会话上打开 SFTP 通道（复用同一条 SSH 连接），返回远端家目录。 */
  sftpOpen(sessionId: string): Promise<SftpInfo>;
  sftpList(sessionId: string, dir: string): Promise<SftpListing>;
  sftpMkdir(sessionId: string, parent: string, name: string): Promise<void>;
  sftpRename(sessionId: string, path: string, newName: string): Promise<void>;
  sftpDelete(sessionId: string, path: string, isDir: boolean): Promise<void>;
  /** 批量上传，立即返回进度描述符；进度走 sftp:transfer 事件。 */
  sftpUpload(sessionId: string, remoteDir: string, localPaths: string[]): Promise<TransferStatus[]>;
  sftpDownload(sessionId: string, remotePath: string, localPath: string): Promise<TransferStatus>;
  sftpCancelTransfer(id: string): Promise<void>;
  /** 弹出系统文件选择器（多选），空列表表示取消。 */
  pickUploadFiles(): Promise<string[]>;
  /** 弹出"另存为"对话框，空串表示取消。 */
  pickSavePath(defaultName: string): Promise<string>;
  /** 转发规则（P4）：列表 / 添加（即启动）/ 删除 / 启停。 */
  forwardList(sessionId: string): Promise<ForwardStatus[]>;
  forwardAdd(
    sessionId: string,
    listenPort: number,
    remoteHost: string,
    remotePort: number,
  ): Promise<ForwardStatus>;
  forwardRemove(id: string): Promise<void>;
  forwardStart(id: string): Promise<ForwardStatus>;
  forwardStop(id: string): Promise<ForwardStatus>;
  /** 订阅后端事件，返回退订函数。 */
  on(name: string, cb: (...data: unknown[]) => void): () => void;
  /** 关闭请求：托盘可用时收进托盘继续运行，否则退出（语义由后端裁决）。 */
  requestClose(): Promise<void>;
  /** 是否启用了 Win11 Mica 系统背景；是则前端切换为半透明玻璃表面。 */
  micaEnabled(): Promise<boolean>;
}

interface WailsApp {
  [method: string]: (...args: unknown[]) => Promise<unknown>;
}

interface WailsRuntime {
  EventsOn(name: string, cb: (...data: unknown[]) => void): () => void;
  EventsOff(name: string, ...rest: string[]): void;
  WindowMinimise(): void;
  WindowToggleMaximise(): void;
  Quit(): void;
  ClipboardSetText(text: string): Promise<boolean>;
}

function wailsApp(): WailsApp | undefined {
  const w = window as unknown as { go?: { shell?: { App?: WailsApp } } };
  return w.go?.shell?.App;
}

function wailsRuntime(): WailsRuntime | undefined {
  return (window as unknown as { runtime?: WailsRuntime }).runtime;
}

/** 把 window.go.shell.App 包装成类型安全的 Backend。 */
function createWailsBackend(app: WailsApp, rt: WailsRuntime): Backend {
  const call = <T>(method: string, ...args: unknown[]): Promise<T> => {
    const fn = app[method];
    if (typeof fn !== "function") {
      return Promise.reject(new Error(`后端缺少绑定方法 ${method}`));
    }
    return fn(...args) as Promise<T>;
  };

  return {
    version: () => call("Version"),
    dataDir: () => call("DataDir"),
    listServers: () => call("ListServers"),
    saveServer: (s) => call("SaveServer", s),
    deleteServer: (id) => call("DeleteServer", id),
    connect: (req) => call("Connect", req),
    hostKeyDecision: (requestId, decision) => call("HostKeyDecision", requestId, decision),
    hasPassword: (serverId) => call("HasPassword", serverId),
    forgetPassword: (serverId) => call("ForgetPassword", serverId),
    passphraseDecision: (requestId, passphrase, cancelled) =>
      call("PassphraseDecision", requestId, passphrase, cancelled),
    listKeys: () => call("ListKeys"),
    generateKey: (req) => call("GenerateKey", req),
    importKey: (req) => call("ImportKey", req),
    deleteKey: (id) => call("DeleteKey", id),
    pickKeyFile: () => call("PickKeyFile"),
    startShell: (req) => call("StartShell", req),
    writeStdin: (sessionId, data) => call("WriteStdin", sessionId, data),
    resizeShell: (sessionId, rows, cols) => call("ResizeShell", sessionId, rows, cols),
    ackTermData: (sessionId) => call("AckTermData", sessionId),
    closeSession: (sessionId) => call("CloseSession", sessionId),
    sftpOpen: (sessionId) => call("SftpOpen", sessionId),
    sftpList: (sessionId, dir) => call("SftpList", sessionId, dir),
    sftpMkdir: (sessionId, parent, name) => call("SftpMkdir", sessionId, parent, name),
    sftpRename: (sessionId, path, newName) => call("SftpRename", sessionId, path, newName),
    sftpDelete: (sessionId, path, isDir) => call("SftpDelete", sessionId, path, isDir),
    sftpUpload: (sessionId, remoteDir, localPaths) =>
      call("SftpUpload", sessionId, remoteDir, localPaths),
    sftpDownload: (sessionId, remotePath, localPath) =>
      call("SftpDownload", sessionId, remotePath, localPath),
    sftpCancelTransfer: (id) => call("SftpCancelTransfer", id),
    pickUploadFiles: () => call("PickUploadFiles"),
    pickSavePath: (defaultName) => call("PickSavePath", defaultName),
    forwardList: (sessionId) => call("ForwardList", sessionId),
    forwardAdd: (sessionId, listenPort, remoteHost, remotePort) =>
      call("ForwardAdd", sessionId, listenPort, remoteHost, remotePort),
    forwardRemove: (id) => call("ForwardRemove", id),
    forwardStart: (id) => call("ForwardStart", id),
    forwardStop: (id) => call("ForwardStop", id),
    on: (name, cb) => {
      rt.EventsOn(name, cb);
      return () => rt.EventsOff(name);
    },
    requestClose: () => call("RequestClose"),
    micaEnabled: () => call("MicaEnabled"),
  };
}

function createBackend(): Backend {
  const app = wailsApp();
  const rt = wailsRuntime();
  if (app && rt) return createWailsBackend(app, rt);

  if (import.meta.env.DEV) {
    console.warn("[nanossh] 未检测到 Wails 运行时，使用 mock 后端（仅开发态）。");
    return createMockBackend();
  }
  throw new Error("NanoSSH 需要 Wails 运行时才能工作。");
}

export const backend: Backend = createBackend();
export const isMock = wailsApp() === undefined;

/** 窗口控制：抽成独立出口，便于在无 Wails 环境下静默降级。 */
export const windowCtl = {
  minimise(): void {
    wailsRuntime()?.WindowMinimise();
  },
  toggleMaximise(): void {
    wailsRuntime()?.WindowToggleMaximise();
  },
  close(): void {
    // 关闭语义（入托盘 or 退出）由后端裁决，前端只发起请求。
    void backend.requestClose();
  },
  async copyText(text: string): Promise<void> {
    const rt = wailsRuntime();
    if (rt) {
      await rt.ClipboardSetText(text);
      return;
    }
    await navigator.clipboard?.writeText(text);
  },
};
