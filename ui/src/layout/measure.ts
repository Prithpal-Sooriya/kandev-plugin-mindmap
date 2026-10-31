// Deterministic node sizing without DOM measurement. Cards render at exactly
// the size computed here (text is clamped to MAX_LINES), so layout is pure,
// testable, and never waits for a measure pass.

export const CARD = {
  padX: 12,
  padY: 8,
  lineH: 18,
  rootLineH: 22,
  minW: 64,
  maxTextW: 220,
  rootMaxTextW: 260,
  maxLines: 3,
  badgeRowH: 16,
} as const;

// Approximate advance widths (px) for a 13px UI sans-serif. Close enough that
// the clamped card never overflows visibly; exact metrics are not needed.
function charWidth(c: string): number {
  const code = c.charCodeAt(0);
  if (code >= 0x2e80) return 13; // CJK and wide symbols
  if (c === " ") return 3.6;
  if ("iljtfI.,:;'|!".includes(c)) return 3.8;
  if ("mwMW@".includes(c)) return 11;
  if (c >= "A" && c <= "Z") return 8.6;
  if (c >= "0" && c <= "9") return 7.3;
  return 7;
}

/** Measures a run of text for a normal card (13px) or the root (600 16px). */
export type TextMeasurer = (text: string, root: boolean) => number;
let measurer: TextMeasurer | null = null;

/**
 * Uses real font metrics when available (browser); tests and the server-side
 * benchmark use the deterministic table. Call before the first layout.
 */
export function setTextMeasurer(m: TextMeasurer | null): void {
  measurer = m;
}

/** Installs a canvas-based measurer for the given font family (cached per string). */
export function installCanvasMeasurer(family: string): boolean {
  const ctx = typeof document !== "undefined" ? document.createElement("canvas").getContext("2d") : null;
  if (!ctx) return false;
  const cache = new Map<string, number>();
  setTextMeasurer((text, root) => {
    const key = (root ? "R" : "N") + text;
    let w = cache.get(key);
    if (w === undefined) {
      ctx.font = root ? `600 16px ${family}` : `13px ${family}`;
      w = ctx.measureText(text).width;
      if (cache.size > 20_000) cache.clear();
      cache.set(key, w);
    }
    return w;
  });
  return true;
}

export function textWidth(s: string, root = false): number {
  if (measurer) return measurer(s, root);
  let w = 0;
  for (const c of s) w += charWidth(c);
  return root ? w * (16 / 13) * 1.06 : w;
}

/** Greedy word wrap; returns the line count (uncapped) and the widest line. */
export function wrap(s: string, maxW: number, root = false): { lines: number; width: number } {
  const words = s.split(/\s+/).filter(Boolean);
  if (!words.length) return { lines: 1, width: 0 };
  const space = textWidth(" ", root);
  let lines = 1;
  let cur = 0;
  let widest = 0;
  for (const word of words) {
    let ww = textWidth(word, root);
    if (cur === 0) {
      while (ww > maxW) {
        // A single long word: hard-break it.
        lines++;
        ww -= maxW;
        widest = maxW;
      }
      cur = ww;
    } else if (cur + space + ww <= maxW) cur += space + ww;
    else {
      widest = Math.max(widest, cur);
      lines++;
      cur = Math.min(ww, maxW);
      while (ww > maxW) {
        lines++;
        ww -= maxW;
      }
    }
  }
  return { lines, width: Math.max(widest, cur) };
}

export interface SizeInput {
  title: string;
  isRoot: boolean;
  /** Whether a badge row is shown under the title (sources, confidence, assets). */
  badges: boolean;
}

export function measureCard(n: SizeInput): { w: number; h: number; lines: number } {
  const maxW = n.isRoot ? CARD.rootMaxTextW : CARD.maxTextW;
  const lineH = n.isRoot ? CARD.rootLineH : CARD.lineH;
  const { lines, width } = wrap(n.title || " ", maxW, n.isRoot);
  const shown = Math.min(lines, CARD.maxLines);
  const padX = n.isRoot ? CARD.padX + 6 : CARD.padX;
  const padY = n.isRoot ? CARD.padY + 4 : CARD.padY;
  // +18 for the kind icon column on non-root cards.
  // +2 px slack so sub-pixel rounding never wraps a line that was measured to fit.
  const w = Math.max(CARD.minW, Math.ceil(Math.min(width, maxW)) + 2 + padX * 2 + (n.isRoot ? 0 : 18));
  const h = shown * lineH + padY * 2 + (n.badges ? CARD.badgeRowH : 0);
  return { w, h, lines: shown };
}
