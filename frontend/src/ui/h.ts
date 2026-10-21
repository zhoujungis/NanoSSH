// 极简 DOM 构造器。不引框架，避免 React 的运行时与 VDOM 开销，
// 也避免组件库把视觉拖向"通用后台外观"。

export type Child = Node | string | number | null | undefined | false;

type Props = Record<string, unknown>;

const SVG_NS = "http://www.w3.org/2000/svg";

function applyProps(el: Element, props: Props): void {
  for (const [key, value] of Object.entries(props)) {
    if (value == null || value === false) continue;

    if (key === "class") {
      el.setAttribute("class", String(value));
    } else if (key === "html") {
      el.innerHTML = String(value);
    } else if (key === "dataset") {
      for (const [k, v] of Object.entries(value as Props)) {
        if (v != null) (el as HTMLElement).dataset[k] = String(v);
      }
    } else if (key === "style") {
      if (typeof value === "string") el.setAttribute("style", value);
      else Object.assign((el as HTMLElement).style, value);
    } else if (key.startsWith("on") && typeof value === "function") {
      el.addEventListener(key.slice(2).toLowerCase(), value as EventListener);
    } else if (key === "value" && el instanceof HTMLInputElement) {
      el.value = String(value);
    } else {
      el.setAttribute(key, value === true ? "" : String(value));
    }
  }
}

function append(el: Element, children: Child[]): void {
  for (const c of children) {
    if (c == null || c === false) continue;
    el.append(typeof c === "object" ? c : document.createTextNode(String(c)));
  }
}

/** 创建 HTML 元素。 */
export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  props?: Props | null,
  ...children: Child[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  if (props) applyProps(el, props);
  append(el, children);
  return el;
}

/** 创建 SVG 元素（子元素由调用方 append，便于内联构建图标）。 */
export function svg(tag: string, props?: Props | null): SVGElement {
  const el = document.createElementNS(SVG_NS, tag);
  if (props) applyProps(el, props);
  return el;
}

/** 内联 SVG 图标：以路径字符串构建，避免引入图标字体或图标库。 */
export function icon(path: string, size = 14, strokeWidth = 1.6): SVGElement {
  return svg("svg", {
    width: size,
    height: size,
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    "stroke-width": strokeWidth,
    "stroke-linecap": "round",
    "stroke-linejoin": "round",
    html: path,
  });
}

export const ICONS = {
  search:
    '<circle cx="11" cy="11" r="7"/><path d="M20 20l-3.6-3.6"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  close: '<path d="M6 6l12 12M18 6L6 18"/>',
  minimize: '<path d="M5 12h14"/>',
  maximize: '<rect x="5" y="5" width="14" height="14" rx="1.5"/>',
  restore:
    '<rect x="8" y="4.5" width="11.5" height="11.5" rx="1.5"/><path d="M4.5 8v11.5h11.5"/>',
  more: '<circle cx="12" cy="5" r="1.4"/><circle cx="12" cy="12" r="1.4"/><circle cx="12" cy="19" r="1.4"/>',
  terminal:
    '<rect x="3" y="4" width="18" height="16" rx="2.5"/><path d="M7 9l3.5 3-3.5 3"/><path d="M13.5 15h4"/>',
  shield:
    '<path d="M12 3l7 3v6c0 4.2-2.9 7.6-7 9-4.1-1.4-7-4.8-7-9V6z"/>',
  warn: '<path d="M12 3.5l9 15.5H3z"/><path d="M12 9.5v4.5"/><path d="M12 17h.01"/>',
  chevron: '<path d="M9 6l6 6-6 6"/>',
  folder: '<path d="M3 7.5A2 2 0 015 5.5h3.5l2 2H19a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2z"/>',
  server:
    '<rect x="3" y="4" width="18" height="6" rx="2"/><rect x="3" y="14" width="18" height="6" rx="2"/><path d="M7 7h.01M7 17h.01"/>',
  refresh: '<path d="M20 11a8 8 0 10-2.6 6.4"/><path d="M20 5v6h-6"/>',
  key: '<circle cx="7.5" cy="16.5" r="3.5"/><path d="M10.2 13.8L19 5"/><path d="M15.5 8.5l2.5 2.5"/><path d="M13 11l2 2"/>',
  copy: '<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V6a2 2 0 012-2h9"/>',
  check: '<path d="M20 6.5L9 17.5l-5-5"/>',
  trash: '<path d="M4 7h16"/><path d="M9.5 7V4.5h5V7"/><path d="M6.5 7l.9 12.5h9.2L17.5 7"/>',
  lock: '<rect x="4" y="10.5" width="16" height="10" rx="2.5"/><path d="M8 10.5V7a4 4 0 018 0v3.5"/>',
  import: '<path d="M12 14V3.5"/><path d="M8 7.5l4-4 4 4"/><path d="M4.5 20.5h15"/>',
  file: '<path d="M6.5 3h7l4.5 4.5V21h-11.5z"/><path d="M13.5 3v4.5H18"/>',
  eye: '<path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="3"/>',
  eyeOff:
    '<path d="M3 3l18 18"/><path d="M10.6 6.1A9.6 9.6 0 0112 6c6 0 9.5 6 9.5 6a17 17 0 01-3.4 4"/><path d="M6.6 7.6A16.6 16.6 0 002.5 12S6 18 12 18c1.4 0 2.7-.3 3.8-.8"/>',
  chevronUp: '<path d="M6 15l6-6 6 6"/>',
  newFolder:
    '<path d="M3 7.5A2 2 0 015 5.5h3.5l2 2H19a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2z"/><path d="M12 11.5v5M9.5 14h5"/>',
  upload: '<path d="M12 15.5V4"/><path d="M8 8l4-4 4 4"/><path d="M4.5 15v4a1.5 1.5 0 001.5 1.5h12a1.5 1.5 0 001.5-1.5v-4"/>',
  download:
    '<path d="M12 4v11.5"/><path d="M8 12l4 4 4-4"/><path d="M4.5 15v4a1.5 1.5 0 001.5 1.5h12a1.5 1.5 0 001.5-1.5v-4"/>',
  edit: '<path d="M4 20h4l10-10-4-4L4 16z"/><path d="M13.5 6.5l4 4"/>',
  forward: '<path d="M3 12h13"/><path d="M11 6l6 6-6 6"/><path d="M20 5v14"/>',
  power: '<path d="M12 4v8"/><path d="M7.2 7.2a7 7 0 109.6 0"/>',
};
