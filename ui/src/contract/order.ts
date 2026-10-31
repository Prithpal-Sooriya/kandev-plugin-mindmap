// Fractional-index order keys. Same algorithm as server/contract/order.go;
// contract/fixtures/OrderCases.json pins both implementations.

export const ORDER_DIGITS = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
const MAX_KEY = 64;

export function validOrderKey(k: string): boolean {
  if (k === "" || k.length > MAX_KEY || k.endsWith("0")) return false;
  for (const ch of k) if (!ORDER_DIGITS.includes(ch)) return false;
  return true;
}

/**
 * Returns a key strictly between a and b. "" for a means "before
 * everything"; "" for b means "after everything". Throws if a >= b.
 */
export function orderBetween(a: string, b: string): string {
  if ((a !== "" && !validOrderKey(a)) || (b !== "" && !validOrderKey(b))) throw new Error("orderBetween: malformed key");
  if (a !== "" && b !== "" && a >= b) throw new Error("orderBetween: a must sort before b");
  return midpoint(a, b, b === "");
}

function midpoint(a: string, b: string, open: boolean): string {
  if (!open) {
    let n = 0;
    while (n < b.length && (a[n] ?? "0") === b[n]) n++;
    if (n > 0) return b.slice(0, n) + midpoint(a.slice(n), b.slice(n), false);
  }
  const da = a !== "" ? ORDER_DIGITS.indexOf(a[0]!) : 0;
  const db = open ? ORDER_DIGITS.length : ORDER_DIGITS.indexOf(b[0]!);
  if (db - da > 1) return ORDER_DIGITS[Math.floor((da + db + 1) / 2)]!;
  if (!open && b.length > 1) return b.slice(0, 1);
  return ORDER_DIGITS[da]! + midpoint(a.slice(1), "", true);
}

/** Sort comparator for siblings: (order, id) byte-wise, as the server does. */
export function compareSiblings(x: { order: string; id: string }, y: { order: string; id: string }): number {
  if (x.order !== y.order) return x.order < y.order ? -1 : 1;
  return x.id < y.id ? -1 : x.id > y.id ? 1 : 0;
}
