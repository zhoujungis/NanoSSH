// 终端字节流的搬运层——整个前端里唯一与"外壳"强相关的模块。
//
// ★ 换成裸 WebView2 外壳时，只需重写本文件（把 Wails 事件换成自建 WebSocket）。
//
// 数据流与反压（低延迟的真正来源）：
//   Go 端攒批 → EventsEmit(name, []byte) → base64 字符串
//     → Worker 解码成 Uint8Array（Transferable 零拷贝）
//       → term.write(bytes, cb) → cb 即"渲染完成"
//         → 回 ack 给 Go → Go 才继续读下一段
// 因此链路上任意一刻最多只有一个 chunk 在飞，是正确的端到端反压，
// 不需要在前端再自造节流。

import { backend } from "./api";

export interface TransportHandlers {
  /** 收到一段远端输出；返回的 Promise 在"渲染完成"后 resolve。 */
  onData(bytes: Uint8Array): Promise<void>;
  /** 会话结束（远端关闭 / 连接断开）。 */
  onExit(reason: string): void;
}

type Decoder = {
  push(b64: string): void;
  dispose(): void;
};

/** 优先用 Worker 解码；Worker 不可用时退回主线程同步解码。 */
function createDecoder(onBytes: (bytes: Uint8Array) => void): Decoder {
  let worker: Worker | null = null;
  try {
    worker = new Worker(new URL("./decode.worker.ts", import.meta.url), { type: "module" });
    worker.onmessage = (e: MessageEvent<Uint8Array>) => onBytes(e.data);
  } catch (err) {
    console.warn("[nanossh] Worker 解码不可用，退回主线程解码：", err);
    worker = null;
  }

  if (worker) {
    const w = worker;
    return {
      push: (b64) => w.postMessage(b64),
      dispose: () => w.terminate(),
    };
  }

  return {
    push: (b64) => {
      const bin = atob(b64);
      const out = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
      onBytes(out);
    },
    dispose: () => undefined,
  };
}

export class TermTransport {
  private offData: () => void;
  private offExit: () => void;
  private decoder: Decoder;
  private disposed = false;
  /** 串行化 write → ack，防止慢渲染时 ack 乱序。 */
  private chain: Promise<void> = Promise.resolve();

  constructor(
    private readonly sessionId: string,
    private readonly handlers: TransportHandlers,
  ) {
    this.decoder = createDecoder((bytes) => this.handleBytes(bytes));

    this.offData = backend.on(`term:data:${sessionId}`, (payload) => {
      if (typeof payload === "string") this.decoder.push(payload);
    });
    this.offExit = backend.on(`term:exit:${sessionId}`, (reason) => {
      this.handlers.onExit(typeof reason === "string" ? reason : "会话已结束");
    });
  }

  private handleBytes(bytes: Uint8Array): void {
    if (this.disposed) return;
    this.chain = this.chain
      .then(() => this.handlers.onData(bytes))
      .then(() => (this.disposed ? undefined : backend.ackTermData(this.sessionId)))
      .catch((err) => console.error("[nanossh] 终端写入失败：", err));
  }

  /** 主动结束：退订事件、销毁 Worker，让 Go 端的等待立刻返回。 */
  dispose(): void {
    this.disposed = true;
    this.offData();
    this.offExit();
    this.decoder.dispose();
    void backend.ackTermData(this.sessionId);
  }
}
