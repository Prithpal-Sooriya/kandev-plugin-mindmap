// A tiny runtime shape library. Contract types are declared once as shapes and
// the TS types are inferred from them, so the runtime check used by the
// fixture test (and by clients on untrusted input) can never drift from the
// static types. No dependencies; works in the host-React bundle and the
// standalone guest app.

export interface Ctx {
  errors: string[];
  /** Optional coverage recorder: object shape name -> key -> seen non-empty. */
  cov?: Map<string, Map<string, boolean>>;
}

export interface Shape<T> {
  readonly kind: string;
  check(value: unknown, path: string, ctx: Ctx): void;
  /** Phantom field carrying the static type. Never set at runtime. */
  readonly _type?: T;
}

export type Infer<S> = S extends Shape<infer T> ? T : never;

type Simplify<T> = { [K in keyof T]: T[K] } & {};

function fail(ctx: Ctx, path: string, msg: string): void {
  ctx.errors.push(`${path || "$"}: ${msg}`);
}

function shape<T>(kind: string, check: Shape<T>["check"]): Shape<T> {
  return { kind, check };
}

export const str = (max?: number): Shape<string> =>
  shape("string", (v, p, c) => {
    if (typeof v !== "string") return fail(c, p, `expected string, got ${typeOf(v)}`);
    if (max !== undefined && v.length > max) fail(c, p, `longer than ${max}`);
  });

const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;

/** RFC 3339 timestamp string (Go time.Time). */
export const time: Shape<string> = shape("time", (v, p, c) => {
  if (typeof v !== "string" || !RFC3339.test(v)) fail(c, p, `expected RFC 3339 time, got ${JSON.stringify(v)}`);
});

export const num: Shape<number> = shape("number", (v, p, c) => {
  if (typeof v !== "number" || !Number.isFinite(v)) fail(c, p, `expected number, got ${typeOf(v)}`);
});

export const int: Shape<number> = shape("integer", (v, p, c) => {
  if (typeof v !== "number" || !Number.isInteger(v)) fail(c, p, `expected integer, got ${JSON.stringify(v)}`);
});

export const bool: Shape<boolean> = shape("boolean", (v, p, c) => {
  if (typeof v !== "boolean") fail(c, p, `expected boolean, got ${typeOf(v)}`);
});

/** One of a fixed set of strings. */
export const oneOf = <const T extends readonly string[]>(values: T): Shape<T[number]> =>
  shape("enum", (v, p, c) => {
    if (typeof v !== "string" || !values.includes(v)) fail(c, p, `expected one of ${values.join("|")}, got ${JSON.stringify(v)}`);
  });

export const arr = <T>(item: Shape<T>): Shape<T[]> =>
  shape("array", (v, p, c) => {
    if (!Array.isArray(v)) return fail(c, p, `expected array, got ${typeOf(v)}`);
    v.forEach((x, i) => item.check(x, `${p}[${i}]`, c));
  });

export const record = <T>(value: Shape<T>): Shape<Record<string, T>> =>
  shape("record", (v, p, c) => {
    if (!isPlainObject(v)) return fail(c, p, `expected object, got ${typeOf(v)}`);
    for (const [k, x] of Object.entries(v)) value.check(x, `${p}.${k}`, c);
  });

/** Any JSON value except null (tool args, structured results, schemas). */
export const json: Shape<unknown> = shape("json", (v, p, c) => {
  if (v === null) return fail(c, p, "null is not allowed in the contract");
  if (Array.isArray(v)) v.forEach((x, i) => json.check(x, `${p}[${i}]`, c));
  else if (isPlainObject(v)) for (const [k, x] of Object.entries(v)) json.check(x, `${p}.${k}`, c);
});

/** Lazy reference, for recursive shapes. */
export const lazy = <T>(get: () => Shape<T>): Shape<T> => shape("lazy", (v, p, c) => get().check(v, p, c));

type Fields = Record<string, Shape<unknown>>;
type ObjType<R extends Fields, O extends Fields> = Simplify<
  { [K in keyof R]: Infer<R[K]> } & { [K in keyof O]?: Infer<O[K]> }
>;

export interface ObjShape<T> extends Shape<T> {
  readonly name: string;
  readonly required: Fields;
  readonly optional: Fields;
}

/**
 * A strict object: required keys must be present, optional keys may be
 * omitted, unknown keys and nulls are errors. Optional fields mirror Go
 * `omitempty`: senders omit them when empty.
 */
export function obj<R extends Fields, O extends Fields = {}>(name: string, required: R, optional?: O): ObjShape<ObjType<R, O>> {
  const opt = (optional ?? {}) as O;
  return {
    kind: "object",
    name,
    required,
    optional: opt,
    check(v, p, c) {
      if (!isPlainObject(v)) return fail(c, p, `expected ${name} object, got ${typeOf(v)}`);
      for (const k of Object.keys(v)) {
        if (!(k in required) && !(k in opt)) fail(c, `${p}.${k}`, `unknown field in ${name}`);
      }
      for (const [k, s] of Object.entries(required)) {
        if (!(k in v)) fail(c, `${p}.${k}`, `missing required field of ${name}`);
        else s.check(v[k], `${p}.${k}`, c);
        record_(c, name, k, v[k], false);
      }
      for (const [k, s] of Object.entries(opt)) {
        if (k in v && v[k] !== undefined) s.check(v[k], `${p}.${k}`, c);
        record_(c, name, k, v[k], true);
      }
    },
  };
}

type Variants = Record<string, Shape<unknown>>;
type TaggedType<B, V extends Variants> = {
  [K in keyof V & string]: Simplify<B & { type: K; payload: Infer<V[K]> }>;
}[keyof V & string];

/**
 * An envelope `{type, payload, ...base}` where payload's shape is chosen by
 * type. Used for Op and Message, matching Go's custom (Un)MarshalJSON.
 */
export function tagged<R extends Fields, O extends Fields, V extends Variants>(
  name: string,
  variants: V,
  baseRequired: R,
  baseOptional?: O,
): Shape<TaggedType<ObjType<R, O>, V>> & { readonly variants: V } {
  const tags = Object.keys(variants);
  const envelope = obj(name, { ...baseRequired, type: oneOf(tags), payload: json }, baseOptional);
  return {
    kind: "tagged",
    variants,
    check(v, p, c) {
      envelope.check(v, p, c);
      if (!isPlainObject(v) || typeof v.type !== "string" || !(v.type in variants)) return;
      const s = variants[v.type];
      if (s) s.check(v.payload, `${p}.payload`, c);
    },
  };
}

/** Validates value against shape; returns error strings (empty when valid). */
export function validate<T>(s: Shape<T>, value: unknown): string[] {
  const ctx: Ctx = { errors: [] };
  s.check(value, "", ctx);
  return ctx.errors;
}

/** Type guard form of validate. */
export function is<T>(s: Shape<T>, value: unknown): value is T {
  return validate(s, value).length === 0;
}

// Coverage: an optional field counts once it is present (Go pointer fields may
// legitimately carry "" or false); a required field once it is non-empty.
function record_(c: Ctx, name: string, key: string, v: unknown, optional: boolean): void {
  if (!c.cov) return;
  let m = c.cov.get(name);
  if (!m) c.cov.set(name, (m = new Map()));
  const nonEmpty = v !== undefined && (optional || (v !== "" && v !== 0 && v !== false));
  m.set(key, (m.get(key) ?? false) || nonEmpty);
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function typeOf(v: unknown): string {
  return v === null ? "null" : Array.isArray(v) ? "array" : typeof v;
}
