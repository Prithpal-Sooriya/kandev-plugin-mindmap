// QR Code, byte mode, error correction L, mask 0, versions 1–10.
// Placement follows the ISO/IEC 18004 bit order (the same walk Nayuki's
// public-domain encoder uses). No third-party dependency.
//
// Matrices are row-major: matrix[y][x] is dark when true. There is no quiet zone.

interface Level {
  data: number;
  ec: number;
  g1: number;
  d1: number;
  g2: number;
  d2: number;
  rem: number;
}

// Index by version. data is the number of data codewords.
const LEVEL: Level[] = [
  { data: 0, ec: 0, g1: 0, d1: 0, g2: 0, d2: 0, rem: 0 },
  { data: 19, ec: 7, g1: 1, d1: 19, g2: 0, d2: 0, rem: 0 },
  { data: 34, ec: 10, g1: 1, d1: 34, g2: 0, d2: 0, rem: 7 },
  { data: 55, ec: 15, g1: 1, d1: 55, g2: 0, d2: 0, rem: 7 },
  { data: 80, ec: 20, g1: 1, d1: 80, g2: 0, d2: 0, rem: 7 },
  { data: 108, ec: 26, g1: 1, d1: 108, g2: 0, d2: 0, rem: 7 },
  { data: 136, ec: 18, g1: 2, d1: 68, g2: 0, d2: 0, rem: 7 },
  { data: 156, ec: 20, g1: 2, d1: 78, g2: 0, d2: 0, rem: 0 },
  { data: 194, ec: 24, g1: 2, d1: 97, g2: 0, d2: 0, rem: 0 },
  { data: 232, ec: 30, g1: 2, d1: 116, g2: 0, d2: 0, rem: 0 },
  { data: 274, ec: 18, g1: 2, d1: 68, g2: 2, d2: 69, rem: 0 },
];

const ALIGN: number[][] = [
  [],
  [],
  [6, 18],
  [6, 22],
  [6, 26],
  [6, 30],
  [6, 34],
  [6, 22, 38],
  [6, 24, 42],
  [6, 26, 46],
  [6, 28, 50],
];

// 18-bit version information for versions 7–10 (BCH, already masked).
const VERSION_BITS: Record<number, number> = { 7: 0x07c94, 8: 0x085bc, 9: 0x09a99, 10: 0x0a4d3 };

/** ECC L, mask 0 format bits. Published value, also what drawFormat produces. */
export const FORMAT_L_MASK0 = 0x77c4;

const GF_EXP = new Uint8Array(512);
const GF_LOG = new Uint8Array(256);
(() => {
  let x = 1;
  for (let i = 0; i < 255; i++) {
    GF_EXP[i] = x;
    GF_LOG[x] = i;
    x <<= 1;
    if (x & 0x100) x ^= 0x11d;
  }
  for (let i = 255; i < 512; i++) GF_EXP[i] = GF_EXP[i - 255];
})();

function gfMul(a: number, b: number): number {
  if (a === 0 || b === 0) return 0;
  return GF_EXP[GF_LOG[a]! + GF_LOG[b]!];
}

/** Generator polynomial, highest degree first. gen[0] is the leading 1. */
function rsGenerator(ec: number): number[] {
  let gen = [1];
  for (let i = 0; i < ec; i++) {
    const next = new Array<number>(gen.length + 1).fill(0);
    for (let j = 0; j < gen.length; j++) {
      next[j] ^= gen[j]!;
      next[j + 1] ^= gfMul(gen[j]!, GF_EXP[i]!);
    }
    gen = next;
  }
  return gen;
}

function rsRemainder(data: Uint8Array, ec: number): Uint8Array {
  const gen = rsGenerator(ec);
  const rem = new Uint8Array(data.length + ec);
  rem.set(data);
  for (let i = 0; i < data.length; i++) {
    const factor = rem[i]!;
    if (factor === 0) continue;
    for (let j = 1; j < gen.length; j++) rem[i + j] ^= gfMul(gen[j]!, factor);
  }
  return rem.subarray(data.length);
}

function sizeOf(version: number): number {
  return 17 + 4 * version;
}

function chooseVersion(len: number): number {
  for (let v = 1; v <= 10; v++) {
    const countBits = v < 10 ? 8 : 16;
    const bits = 4 + countBits + len * 8;
    if (bits <= LEVEL[v]!.data * 8) return v;
  }
  throw new Error("link is too long for a QR code");
}

/** Data codewords (header, payload, terminator, 0xEC/0x11 pad) for byte mode. */
export function dataCodewords(text: string, version: number): Uint8Array {
  const level = LEVEL[version]!;
  const bytes = new TextEncoder().encode(text);
  const countBits = version < 10 ? 8 : 16;
  const bits: number[] = [];
  const push = (value: number, n: number) => {
    for (let i = n - 1; i >= 0; i--) bits.push((value >>> i) & 1);
  };
  push(0b0100, 4);
  push(bytes.length, countBits);
  for (const b of bytes) push(b, 8);
  const cap = level.data * 8;
  for (let i = 0; i < 4 && bits.length < cap; i++) bits.push(0);
  while (bits.length % 8 !== 0) bits.push(0);
  const out = new Uint8Array(level.data);
  for (let i = 0; i < bits.length / 8; i++) {
    let v = 0;
    for (let b = 0; b < 8; b++) v = (v << 1) | bits[i * 8 + b]!;
    out[i] = v;
  }
  for (let i = bits.length / 8, pad = 0; i < out.length; i++, pad++) out[i] = pad % 2 === 0 ? 0xec : 0x11;
  return out;
}

function interleave(data: Uint8Array, version: number): Uint8Array {
  const level = LEVEL[version]!;
  const blocks: { data: Uint8Array; ec: Uint8Array }[] = [];
  let off = 0;
  const take = (n: number, len: number) => {
    for (let i = 0; i < n; i++) {
      const part = data.subarray(off, off + len);
      off += len;
      blocks.push({ data: part, ec: rsRemainder(part, level.ec) });
    }
  };
  take(level.g1, level.d1);
  take(level.g2, level.d2);
  const out: number[] = [];
  const maxD = Math.max(level.d1, level.d2);
  for (let i = 0; i < maxD; i++) for (const b of blocks) if (i < b.data.length) out.push(b.data[i]!);
  for (let i = 0; i < level.ec; i++) for (const b of blocks) out.push(b.ec[i]!);
  const bits: number[] = [];
  for (const byte of out) for (let i = 7; i >= 0; i--) bits.push((byte >>> i) & 1);
  for (let i = 0; i < level.rem; i++) bits.push(0);
  return Uint8Array.from(bits);
}

function empty(n: number): boolean[][] {
  return Array.from({ length: n }, () => Array.from({ length: n }, () => false));
}

/** Finder, separator, alignment, timing, format and version modules. */
export function functionModules(version: number): boolean[][] {
  const n = sizeOf(version);
  const fn = empty(n);
  const mark = (x: number, y: number) => {
    if (x >= 0 && y >= 0 && x < n && y < n) fn[y]![x] = true;
  };
  const finder = (x0: number, y0: number) => {
    for (let dy = -1; dy <= 7; dy++) for (let dx = -1; dx <= 7; dx++) mark(x0 + dx, y0 + dy);
  };
  finder(0, 0);
  finder(n - 7, 0);
  finder(0, n - 7);
  const pos = ALIGN[version]!;
  for (let i = 0; i < pos.length; i++) {
    for (let j = 0; j < pos.length; j++) {
      if ((i === 0 && j === 0) || (i === 0 && j === pos.length - 1) || (i === pos.length - 1 && j === 0)) continue;
      for (let dy = -2; dy <= 2; dy++) for (let dx = -2; dx <= 2; dx++) mark(pos[i]! + dx, pos[j]! + dy);
    }
  }
  for (let i = 0; i < n; i++) {
    mark(6, i);
    mark(i, 6);
  }
  mark(8, n - 8);
  // Format information sits on row 8 and column 8, plus the second copy.
  for (let i = 0; i < 9; i++) {
    mark(8, i);
    mark(i, 8);
  }
  for (let i = 0; i < 8; i++) {
    mark(n - 1 - i, 8);
    mark(8, n - 1 - i);
  }
  if (version >= 7) {
    for (let i = 0; i < 18; i++) {
      const a = n - 11 + (i % 3);
      const b = Math.floor(i / 3);
      mark(a, b);
      mark(b, a);
    }
  }
  return fn;
}

function drawFixed(modules: boolean[][], version: number): boolean[][] {
  const n = modules.length;
  const fn = empty(n);
  const set = (x: number, y: number, dark: boolean) => {
    if (x < 0 || y < 0 || x >= n || y >= n || fn[y]![x]) return;
    modules[y]![x] = dark;
    fn[y]![x] = true;
  };
  const force = (x: number, y: number, dark: boolean) => {
    if (x < 0 || y < 0 || x >= n || y >= n) return;
    modules[y]![x] = dark;
    fn[y]![x] = true;
  };
  const finder = (x0: number, y0: number) => {
    for (let dy = -1; dy <= 7; dy++) {
      for (let dx = -1; dx <= 7; dx++) {
        const dist = Math.max(Math.abs(dx - 3), Math.abs(dy - 3));
        force(x0 + dx, y0 + dy, dist !== 2 && dist !== 4);
      }
    }
  };
  finder(0, 0);
  finder(n - 7, 0);
  finder(0, n - 7);
  const pos = ALIGN[version]!;
  for (let i = 0; i < pos.length; i++) {
    for (let j = 0; j < pos.length; j++) {
      if ((i === 0 && j === 0) || (i === 0 && j === pos.length - 1) || (i === pos.length - 1 && j === 0)) continue;
      const x = pos[i]!;
      const y = pos[j]!;
      for (let dy = -2; dy <= 2; dy++) {
        for (let dx = -2; dx <= 2; dx++) force(x + dx, y + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
      }
    }
  }
  for (let i = 0; i < n; i++) {
    set(6, i, i % 2 === 0);
    set(i, 6, i % 2 === 0);
  }
  force(8, n - 8, true);
  const format = FORMAT_L_MASK0;
  const bit = (i: number) => ((format >>> i) & 1) === 1;
  for (let i = 0; i <= 5; i++) force(8, i, bit(i));
  force(8, 7, bit(6));
  force(8, 8, bit(7));
  force(7, 8, bit(8));
  for (let i = 9; i < 15; i++) force(14 - i, 8, bit(i));
  for (let i = 0; i < 8; i++) force(n - 1 - i, 8, bit(i));
  for (let i = 8; i < 15; i++) force(8, n - 15 + i, bit(i));
  if (version >= 7) {
    const bits = VERSION_BITS[version]!;
    for (let i = 0; i < 18; i++) {
      const on = ((bits >>> i) & 1) === 1;
      const a = n - 11 + (i % 3);
      const b = Math.floor(i / 3);
      force(b, a, on);
      force(a, b, on);
    }
  }
  return fn;
}

function walk(n: number, fn: boolean[][], visit: (x: number, y: number) => void): void {
  let right = n - 1;
  while (right > 0) {
    if (right === 6) right = 5;
    for (let vert = 0; vert < n; vert++) {
      for (let j = 0; j < 2; j++) {
        const x = right - j;
        const upward = ((right + 1) & 2) === 0;
        const y = upward ? n - 1 - vert : vert;
        if (!fn[y]![x]) visit(x, y);
      }
    }
    right -= 2;
  }
}

export interface QRCode {
  /** Dark modules, row-major, no quiet zone. */
  matrix: boolean[][];
  version: number;
  /** Bits that were placed, before mask 0, including remainder zeros. */
  bits: boolean[];
}

/** Encode `text` as a QR matrix. Throws if it does not fit in version 10. */
export function encodeQR(text: string): QRCode {
  const version = chooseVersion(new TextEncoder().encode(text).length);
  const n = sizeOf(version);
  const modules = empty(n);
  const fn = drawFixed(modules, version);
  const raw = interleave(dataCodewords(text, version), version);
  const bits = [...raw].map((b) => b === 1);
  let i = 0;
  walk(n, fn, (x, y) => {
    let dark = i < bits.length ? bits[i]! : false;
    if ((x + y) % 2 === 0) dark = !dark;
    modules[y]![x] = dark;
    i++;
  });
  return { matrix: modules, version, bits };
}

export function qrMatrix(text: string): boolean[][] {
  return encodeQR(text).matrix;
}

/** Read data bits back out of a matrix, undoing mask 0. */
export function readDataBits(matrix: boolean[][]): boolean[] {
  const n = matrix.length;
  const version = (n - 17) / 4;
  const fn = functionModules(version);
  const out: boolean[] = [];
  walk(n, fn, (x, y) => {
    let dark = matrix[y]![x]!;
    if ((x + y) % 2 === 0) dark = !dark;
    out.push(dark);
  });
  return out;
}

/** Reconstruct the 15 format bits from the top-left copy (bit 0 is the LSB). */
export function readFormatBits(matrix: boolean[][]): number {
  const at = (x: number, y: number) => (matrix[y]![x] ? 1 : 0);
  const pos: [number, number][] = [
    [8, 0], [8, 1], [8, 2], [8, 3], [8, 4], [8, 5], [8, 7], [8, 8], [7, 8],
    [5, 8], [4, 8], [3, 8], [2, 8], [1, 8], [0, 8],
  ];
  let bits = 0;
  pos.forEach(([x, y], i) => {
    bits |= at(x, y) << i;
  });
  return bits;
}
