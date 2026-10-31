// ID and token helpers. Mirrors server/contract/ids.go and tokens.go.
import { TOKEN_KINDS } from "./enums";

export const ID_PREFIX = {
  map: "map", node: "nd", link: "ln", op: "op", run: "run", proposal: "prp", participant: "pt",
  asset: "as", activity: "ev", share: "sh", batch: "bt",
} as const;
export type IdPrefix = (typeof ID_PREFIX)[keyof typeof ID_PREFIX];

const ID_RE = /^([a-z]{2,3})_([A-Za-z0-9]{10,40})$/;
const REF_RE = /^[a-z0-9][a-z0-9_-]{0,47}$/;
const ALNUM = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

export function validId(prefix: IdPrefix, id: string): boolean {
  const m = ID_RE.exec(id);
  return m !== null && m[1] === prefix;
}

/** Mints a client-side id (20 random alphanumerics) for optimistic ops. */
export function newId(prefix: IdPrefix): string {
  const bytes = new Uint8Array(20);
  crypto.getRandomValues(bytes);
  let s = "";
  for (const b of bytes) s += ALNUM[b % ALNUM.length];
  return `${prefix}_${s}`;
}

/** A node argument in tool calls: a node id or "@ref". */
export function validNodeRef(s: string): boolean {
  return s.startsWith("@") ? REF_RE.test(s.slice(1)) : validId("nd", s);
}

export type TokenKind = (typeof TOKEN_KINDS)[number];
const SECRET_RE = /^[A-Za-z0-9_-]{43}$/;

/** Splits "<kind>1.<secret>" or, for bridge tokens, "<kind>1.<port>.<secret>". */
export function parseToken(raw: string): { kind: TokenKind; port?: number; secret: string } | null {
  const parts = raw.split(".");
  const head = parts[0] ?? "";
  if (!head.endsWith("1")) return null;
  const kind = head.slice(0, -1) as TokenKind;
  if (!TOKEN_KINDS.includes(kind)) return null;
  const bridge = kind === "mmr" || kind === "mmp";
  if (bridge && parts.length === 3) {
    const port = Number(parts[1]);
    if (!Number.isInteger(port) || port < 1 || port > 65535 || !SECRET_RE.test(parts[2]!)) return null;
    return { kind, port, secret: parts[2]! };
  }
  if (!bridge && parts.length === 2 && SECRET_RE.test(parts[1]!)) return { kind, secret: parts[1]! };
  return null;
}
