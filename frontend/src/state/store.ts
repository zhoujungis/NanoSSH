// 应用状态：用 @preact/signals-core（约 2KB）做细粒度响应式，
// 不引 React——5 个页面、单一活动视图，VDOM 与运行时都是纯负担。

import { computed, signal } from "@preact/signals-core";
import { backend, type KeyInfo, type ServerInfo } from "../ipc/api";

export type TabState = "connecting" | "open" | "closed";

/** 左侧轨道的当前分区：服务器列表 / 密钥管理。 */
export type RailView = "servers" | "keys";

/** 会话标签内部的子视图：终端面板 / 文件面板 / 端口转发。 */
export type PaneView = "term" | "files" | "fwd";

export interface SessionTab {
  sessionId: string;
  server: ServerInfo;
  state: TabState;
  /** 关闭 / 失败时展示的原因。 */
  note?: string;
}

export const servers = signal<ServerInfo[]>([]);
export const query = signal("");
export const tabs = signal<SessionTab[]>([]);
export const activeTab = signal<string | null>(null);
export const version = signal("");
export const dataDir = signal("");

/**
 * 每个会话标签当前的子视图（终端 / 文件）。
 *
 * 放在独立信号而不是 SessionTab 上：子视图切换是高频的界面状态，
 * 塞进 tabs 会让标签条整条重绘，把鼠标悬停态一起冲掉。
 */
export const paneViews = signal<Record<string, PaneView>>({});

export function setPaneView(sessionId: string, view: PaneView): void {
  paneViews.value = { ...paneViews.value, [sessionId]: view };
}

export function paneViewOf(sessionId: string | null): PaneView {
  if (!sessionId) return "term";
  return paneViews.value[sessionId] ?? "term";
}

/** 轨道分区。切换时轨道内容与舞台内容一起换。 */
export const railView = signal<RailView>("servers");

export const keys = signal<KeyInfo[]>([]);
/** 密钥视图中被选中的密钥 id；决定舞台展示哪一把。 */
export const activeKey = signal<string | null>(null);

/** 正在建立连接的服务器；用于舞台上的"连接中"卡片。 */
export const connecting = signal<ServerInfo | null>(null);
/** 最近一次连接失败；用于舞台上的错误卡片。 */
export const connectError = signal<{ server: ServerInfo; message: string } | null>(null);

export const filteredServers = computed(() => {
  const q = query.value.trim().toLowerCase();
  const list = servers.value;
  if (!q) return list;
  return list.filter((s) =>
    [s.name, s.host, s.user, s.country ?? "", String(s.port)].some((v) =>
      v.toLowerCase().includes(q),
    ),
  );
});

export const filteredKeys = computed(() => {
  const q = query.value.trim().toLowerCase();
  const list = keys.value;
  if (!q) return list;
  return list.filter((k) =>
    [k.name, k.type, k.fingerprint].some((v) => v.toLowerCase().includes(q)),
  );
});

export const keyById = computed(() => {
  const map = new Map<string, KeyInfo>();
  for (const k of keys.value) map.set(k.id, k);
  return map;
});

export const tabOfServer = computed(() => {
  const map = new Map<string, SessionTab>();
  for (const t of tabs.value) {
    const cur = map.get(t.server.id);
    if (!cur || t.state === "open") map.set(t.server.id, t);
  }
  return map;
});

export async function loadServers(): Promise<void> {
  servers.value = await backend.listServers();
}

export async function loadKeys(): Promise<void> {
  keys.value = await backend.listKeys();
  const ids = new Set(keys.value.map((k) => k.id));
  if (activeKey.value && !ids.has(activeKey.value)) activeKey.value = null;
  if (!activeKey.value && keys.value.length) activeKey.value = keys.value[0].id;
}

/** 把一把新密钥并入列表并选中它。 */
export function addKeyLocal(key: KeyInfo): void {
  keys.value = [...keys.value, key];
  activeKey.value = key.id;
}

export function removeKeyLocal(id: string): void {
  keys.value = keys.value.filter((k) => k.id !== id);
  if (activeKey.value === id) {
    activeKey.value = keys.value.length ? keys.value[0].id : null;
  }
}

export async function loadMeta(): Promise<void> {
  const [v, d] = await Promise.all([backend.version(), backend.dataDir().catch(() => "")]);
  version.value = v;
  dataDir.value = d;
}

export async function persistServer(input: Partial<ServerInfo>): Promise<ServerInfo> {
  const saved = await backend.saveServer(input);
  const list = servers.value.slice();
  const i = list.findIndex((s) => s.id === saved.id);
  if (i >= 0) list[i] = saved;
  else list.push(saved);
  servers.value = list;
  return saved;
}

export async function removeServer(id: string): Promise<void> {
  await backend.deleteServer(id);
  servers.value = servers.value.filter((s) => s.id !== id);
}

export function upsertTab(tab: SessionTab): void {
  const list = tabs.value.slice();
  const i = list.findIndex((t) => t.sessionId === tab.sessionId);
  if (i >= 0) list[i] = tab;
  else list.push(tab);
  tabs.value = list;
}

export function patchTab(sessionId: string, patch: Partial<SessionTab>): void {
  tabs.value = tabs.value.map((t) => (t.sessionId === sessionId ? { ...t, ...patch } : t));
}

export function closeTabLocal(sessionId: string): void {
  const list = tabs.value.filter((t) => t.sessionId !== sessionId);
  tabs.value = list;
  if (paneViews.value[sessionId]) {
    const next = { ...paneViews.value };
    delete next[sessionId];
    paneViews.value = next;
  }
  if (activeTab.value === sessionId) {
    activeTab.value = list.length ? list[list.length - 1].sessionId : null;
  }
}
