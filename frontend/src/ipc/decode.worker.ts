// base64 → Uint8Array，并把结果零拷贝转交主线程。
//
// 为什么要有 Worker：Wails 的 EventsEmit 会把 []byte 序列化成 base64 字符串
// （带宽 +33%），解码这一步若放在主线程、且数据量大时会直接阻塞渲染。
// 把它放进 Worker，主线程只负责 term.write。解码结果通过 Transferable
// 转移 ArrayBuffer 所有权，避免一次拷贝。

// 用结构化的最小接口，规避 DOM 与 WebWorker 两套 lib 类型定义的冲突。
const ctx = self as unknown as {
  onmessage: ((e: MessageEvent<string>) => void) | null;
  postMessage: (msg: unknown, transfer?: Transferable[]) => void;
};

function decodeBase64(input: string): Uint8Array {
  const bin = atob(input);
  const len = bin.length;
  const out = new Uint8Array(len);
  for (let i = 0; i < len; i++) out[i] = bin.charCodeAt(i);
  return out;
}

ctx.onmessage = (e: MessageEvent<string>) => {
  const buf = decodeBase64(e.data);
  ctx.postMessage(buf, [buf.buffer]);
};
