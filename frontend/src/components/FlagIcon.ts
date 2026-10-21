// 国旗图标：纯内联 SVG，按 ISO-3166 alpha-2 取图。
//
// ★ 为什么不用 emoji 国旗：Windows 的 Segoe UI Emoji 不含 regional-indicator
//   字形，🇺🇸 会渲染成两个字母 "US"。对一个以"高级感"为卖点的服务器列表，
//   这是致命的。这里为常见地区手绘简化几何旗（20×14 下辨识度足够），
//   未收录的地区退回"深色底 + 国家码"的 neutral chip。

import { svg } from "../ui/h";

const VB = "0 0 24 16";

function star(cx: number, cy: number, r: number, fill = "#FFDE00"): string {
  const pts: string[] = [];
  for (let i = 0; i < 10; i++) {
    const rr = i % 2 === 0 ? r : r * 0.382;
    const a = ((-90 + i * 36) * Math.PI) / 180;
    pts.push(`${(cx + rr * Math.cos(a)).toFixed(2)},${(cy + rr * Math.sin(a)).toFixed(2)}`);
  }
  return `<path d="M${pts.join("L")}Z" fill="${fill}"/>`;
}

/** 水平三等分。 */
function hBands(a: string, b: string, c: string): string {
  return (
    `<rect width="24" height="5.34" fill="${a}"/>` +
    `<rect y="5.33" width="24" height="5.34" fill="${b}"/>` +
    `<rect y="10.66" width="24" height="5.34" fill="${c}"/>`
  );
}

/** 垂直三等分。 */
function vBands(a: string, b: string, c: string): string {
  return (
    `<rect width="8" height="16" fill="${a}"/>` +
    `<rect x="8" width="8" height="16" fill="${b}"/>` +
    `<rect x="16" width="8" height="16" fill="${c}"/>`
  );
}

const UNION_JACK =
  `<path d="M0 0l24 16M24 0L0 16" stroke="#ffffff" stroke-width="3.3"/>` +
  `<path d="M0 0l24 16M24 0L0 16" stroke="#C8102E" stroke-width="1.7"/>` +
  `<path d="M12 0v16M0 8h24" stroke="#ffffff" stroke-width="5.6"/>` +
  `<path d="M12 0v16M0 8h24" stroke="#C8102E" stroke-width="3.3"/>`;

const FLAGS: Record<string, string> = {
  US:
    `<rect width="24" height="16" fill="#ffffff"/>` +
    [0, 2, 4, 6, 8, 10, 12]
      .map((i) => `<rect y="${((i * 16) / 13).toFixed(2)}" width="24" height="1.23" fill="#B22234"/>`)
      .join("") +
    `<rect width="10" height="8.62" fill="#3C3B6E"/>` +
    [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11]
      .map((i) => {
        const col = i % 4;
        const row = Math.floor(i / 4);
        return `<circle cx="${(1.6 + col * 2.3).toFixed(2)}" cy="${(1.55 + row * 2).toFixed(2)}" r="0.52" fill="#fff"/>`;
      })
      .join(""),

  DE: hBands("#000000", "#DD0000", "#FFCE00"),
  NL: hBands("#AE1C28", "#ffffff", "#21468B"),
  RU: hBands("#ffffff", "#0039A6", "#D52B1E"),
  PL: hBands("#ffffff", "#DC143C", "#DC143C"),
  UA: hBands("#0057B7", "#FFD700", "#FFD700"),
  IT: vBands("#009246", "#ffffff", "#CE2B37"),
  FR: vBands("#002395", "#ffffff", "#ED2939"),

  JP: `<rect width="24" height="16" fill="#ffffff"/><circle cx="12" cy="8" r="4.4" fill="#BC002D"/>`,

  CN:
    `<rect width="24" height="16" fill="#DE2910"/>` +
    star(4.6, 4.3, 2.9) +
    star(9.4, 2.2, 0.95) +
    star(11, 4.4, 0.95) +
    star(10.6, 7.1, 0.95) +
    star(8.7, 9.2, 0.95),

  HK:
    `<rect width="24" height="16" fill="#DE2910"/>` +
    Array.from({ length: 5 }, (_, i) => {
      const a = ((i * 72 - 90) * Math.PI) / 180;
      return `<ellipse cx="${(12 + Math.cos(a) * 2.5).toFixed(2)}" cy="${(8 + Math.sin(a) * 2.5).toFixed(2)}" rx="1.5" ry="1.5" fill="#ffffff"/>`;
    }).join("") +
    `<circle cx="12" cy="8" r="1.2" fill="#DE2910"/>`,

  TW:
    `<rect width="24" height="16" fill="#FE0000"/>` +
    `<rect width="12" height="8" fill="#000095"/>` +
    `<circle cx="6" cy="4" r="2.2" fill="#ffffff"/>` +
    `<circle cx="6" cy="4" r="1.35" fill="#000095"/>`,
  SG:
    `<rect width="24" height="16" fill="#ffffff"/>` +
    `<rect width="24" height="8" fill="#EE2536"/>` +
    `<circle cx="5.4" cy="4" r="2.9" fill="#ffffff"/>` +
    `<circle cx="7" cy="4" r="2.5" fill="#EE2536"/>` +
    `<circle cx="9.4" cy="2.2" r="0.6" fill="#fff"/><circle cx="11.5" cy="3.4" r="0.6" fill="#fff"/>` +
    `<circle cx="12.4" cy="5.4" r="0.6" fill="#fff"/><circle cx="10.4" cy="6.4" r="0.6" fill="#fff"/>` +
    `<circle cx="8.3" cy="5.2" r="0.6" fill="#fff"/>`,

  KR:
    `<rect width="24" height="16" fill="#ffffff"/>` +
    `<circle cx="12" cy="8" r="4.1" fill="#CD2E3A"/>` +
    `<path d="M12 3.9a4.1 4.1 0 000 8.2 2.05 2.05 0 000-4.1 2.05 2.05 0 000-4.1z" fill="#0047A0"/>`,

  GB: `<rect width="24" height="16" fill="#012169"/>${UNION_JACK}`,
  AU:
    `<rect width="24" height="16" fill="#00247D"/>` +
    `<g transform="scale(0.5)">${UNION_JACK}</g>` +
    `<circle cx="6" cy="12" r="0.9" fill="#fff"/>` +
    `<circle cx="18" cy="4.5" r="0.8" fill="#fff"/><circle cx="20" cy="11" r="0.8" fill="#fff"/>` +
    `<circle cx="15.6" cy="12.4" r="0.7" fill="#fff"/><circle cx="17.8" cy="9.2" r="0.6" fill="#fff"/>` +
    `<circle cx="14.4" cy="7" r="0.5" fill="#fff"/><circle cx="19.6" cy="14" r="0.5" fill="#fff"/>`,
  NZ:
    `<rect width="24" height="16" fill="#00247D"/>` +
    `<g transform="scale(0.5)">${UNION_JACK}</g>` +
    `<circle cx="17.4" cy="6" r="0.85" fill="#fff"/><circle cx="19.8" cy="10.4" r="0.7" fill="#fff"/>` +
    `<circle cx="15.4" cy="11.2" r="0.7" fill="#fff"/><circle cx="16.8" cy="13.9" r="0.6" fill="#fff"/>`,
  CA:
    `<rect width="24" height="16" fill="#ffffff"/>` +
    `<rect width="6.5" height="16" fill="#D52B1E"/><rect x="17.5" width="6.5" height="16" fill="#D52B1E"/>` +
    `<path d="M12 3.2l1 2.1 1.9-.7-.7 2.2 2.1 1.3-2.3.7.3 1.7-2-.9-2 .9.3-1.7-2.3-.7 2.1-1.3-.7-2.2 1.9.7z" fill="#D52B1E"/>`,

  IN:
    hBands("#FF9933", "#ffffff", "#138808") +
    `<circle cx="12" cy="8" r="1.7" fill="none" stroke="#000080" stroke-width="0.5"/>` +
    `<circle cx="12" cy="8" r="0.45" fill="#000080"/>`,

  BR:
    `<rect width="24" height="16" fill="#009C3B"/>` +
    `<path d="M12 1.6l10.4 6.4L12 14.4 1.6 8z" fill="#FFDF00"/>` +
    `<circle cx="12" cy="8" r="2.8" fill="#002776"/>` +
    `<path d="M9.3 7a8 8 0 015.4 1.9" stroke="#fff" stroke-width="0.5" fill="none"/>`,

  ES: `<rect width="24" height="16" fill="#AA151B"/><rect y="4" width="24" height="8" fill="#F1BF00"/>`,
  SE: `<rect width="24" height="16" fill="#006AA7"/><path d="M7.6 0v16M0 8h24" stroke="#FECC00" stroke-width="3.2"/>`,
  CH: `<rect width="24" height="16" fill="#D52B1E"/><path d="M12 4.4v7.2M8.4 8h7.2" stroke="#fff" stroke-width="2.2"/>`,
  TR:
    `<rect width="24" height="16" fill="#E30A17"/>` +
    `<circle cx="10" cy="8" r="3.2" fill="#fff"/><circle cx="11.4" cy="8" r="2.6" fill="#E30A17"/>` +
    star(14.4, 8, 1.8, "#fff"),
  VN: `<rect width="24" height="16" fill="#DA251D"/>${star(12, 8, 3.4)}`,
  TH: `<rect width="24" height="16" fill="#A51931"/><rect y="2.66" width="24" height="10.68" fill="#F4F5F8"/><rect y="5.33" width="24" height="5.34" fill="#2D2A4A"/>`,
  ID: hBands("#CE1126", "#ffffff", "#ffffff"),
  MY:
    `<rect width="24" height="16" fill="#ffffff"/>` +
    Array.from({ length: 7 }, (_, i) => `<rect y="${((i * 16) / 14 + 1.15).toFixed(2)}" width="24" height="1.15" fill="#CC0001"/>`).join("") +
    `<rect width="12" height="8" fill="#010066"/>` +
    `<circle cx="5" cy="4" r="2.4" fill="#FFCC00"/><circle cx="6.3" cy="4" r="2" fill="#010066"/>` +
    star(8.6, 4, 1.3, "#FFCC00"),
  AE:
    `<rect width="24" height="16" fill="#ffffff"/>` +
    `<rect width="24" height="5.34" fill="#00843D"/><rect y="10.66" width="24" height="5.34" fill="#000000"/>` +
    `<rect width="6" height="16" fill="#FF0000"/>`,
};

function fallback(code: string): string {
  return (
    `<rect width="24" height="16" fill="#1B212B"/>` +
    `<text x="12" y="11.6" font-size="8" font-family="monospace" font-weight="600" ` +
    `fill="#8B95A5" text-anchor="middle" letter-spacing="0.4">${code.slice(0, 2).toUpperCase()}</text>`
  );
}

/** 返回一个 20×14 的国旗 SVG。未知国家码会退回国家码 chip。 */
export function flagIcon(country: string | undefined): SVGElement {
  const code = (country ?? "").toUpperCase();
  const inner = FLAGS[code] ?? fallback(code || "??");
  return svg("svg", {
    width: 20,
    height: 14,
    viewBox: VB,
    "aria-hidden": "true",
    html:
      `<defs><clipPath id="nf-${code || "xx"}">` +
      `<rect width="24" height="16" rx="2.6"/></clipPath></defs>` +
      `<g clip-path="url(#nf-${code || "xx"})">${inner}</g>`,
  });
}

/** 国家码 chip，用于列表行的次要信息。 */
export function countryChip(country: string | undefined): string {
  return (country ?? "").toUpperCase().slice(0, 2) || "--";
}
