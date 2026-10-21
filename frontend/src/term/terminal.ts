// xterm.js 装配。六个 addon 的取舍与版本见实现计划：
// fit / webgl / unicode11 / web-links / search 五个全上，image addon 明确不要。
//
// ★ unicode11 是 CJK 宽度正确的刚需：默认的 Unicode 6 表会把部分宽字符算窄，
//   导致中文与框线错位。webgl 负责高频重绘，若初始化失败（虚拟机 / 软件渲染）
//   自动退回 DOM 渲染器，功能不受影响，只是长输出时更吃 CPU。

import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { WebglAddon } from "@xterm/addon-webgl";
import type { TransportHandlers } from "../ipc/transport";
import { TermTransport } from "../ipc/transport";
import { backend } from "../ipc/api";

export interface TermHandle {
  term: Terminal;
  /** 使终端铺满容器；返回新的行列数。 */
  fit(): { rows: number; cols: number };
  focus(): void;
  dispose(): void;
  /** 立即把本地尺寸同步给远端（去抖后调用）。 */
  syncSize(): void;
}

const THEME = {
  background: "#0b0d10",
  foreground: "#dfe6f0",
  cursor: "#5b9dff",
  cursorAccent: "#0b0d10",
  selectionBackground: "rgba(91, 157, 255, 0.30)",
  black: "#1d2330",
  red: "#f2777a",
  green: "#9dd66c",
  yellow: "#f2c66d",
  blue: "#6aa9ff",
  magenta: "#c39ac9",
  cyan: "#7fd3c9",
  white: "#cfd6e1",
  brightBlack: "#4a5464",
  brightRed: "#ff8b82",
  brightGreen: "#b6e88a",
  brightYellow: "#ffdd94",
  brightBlue: "#93c2ff",
  brightMagenta: "#e0b6e6",
  brightCyan: "#a8e8de",
  brightWhite: "#f4f7fb",
};

export function createTerminal(
  sessionId: string,
  host: HTMLElement,
  opts: { fontFamily: string; fontSize: number; scrollback: number; onExit: (reason: string) => void },
): TermHandle {
  const term = new Terminal({
    theme: THEME,
    fontFamily: opts.fontFamily,
    fontSize: opts.fontSize,
    lineHeight: 1.28,
    letterSpacing: 0,
    cursorBlink: true,
    cursorStyle: "bar",
    scrollback: opts.scrollback,
    allowProposedApi: true,
    macOptionIsMeta: true,
    // 让 WebView2 自己处理 IME，配合 unicode11 保证 CJK 光标定位正确。
    drawBoldTextInBrightColors: true,
    minimumContrastRatio: 1,
  });

  const fit = new FitAddon();
  term.loadAddon(fit);

  const unicode = new Unicode11Addon();
  term.loadAddon(unicode);
  term.unicode.activeVersion = "11";

  term.loadAddon(new WebLinksAddon());
  const search = new SearchAddon();
  term.loadAddon(search);
  void search;

  term.open(host);

  // WebGL 渲染器必须在 open 之后挂载；失败则保持默认 DOM 渲染器。
  try {
    const webgl = new WebglAddon();
    webgl.onContextLoss(() => webgl.dispose());
    term.loadAddon(webgl);
  } catch (err) {
    console.warn("[nanossh] WebGL 渲染器不可用，退回 DOM 渲染：", err);
  }

  const transport = new TermTransport(sessionId, {
    onData: (bytes) => new Promise<void>((resolve) => term.write(bytes, resolve)),
    onExit: (reason) => opts.onExit(reason),
  } satisfies TransportHandlers);

  term.onData((data) => {
    void backend.writeStdin(sessionId, data);
  });

  const doFit = (): { rows: number; cols: number } => {
    try {
      fit.fit();
    } catch {
      /* 容器尚未布局完成时会抛错，忽略即可 */
    }
    return { rows: term.rows, cols: term.cols };
  };

  let debounce: number | undefined;
  const syncSize = (): void => {
    window.clearTimeout(debounce);
    debounce = window.setTimeout(() => {
      // ★ 参数顺序：Go 侧一律 (rows, cols)——行在前、列在后。
      void backend.resizeShell(sessionId, term.rows, term.cols);
    }, 150);
  };

  return {
    term,
    fit: doFit,
    focus: () => term.focus(),
    syncSize,
    dispose: () => {
      window.clearTimeout(debounce);
      transport.dispose();
      term.dispose();
    },
  };
}
