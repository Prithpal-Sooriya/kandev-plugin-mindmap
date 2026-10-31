// Minimal JSON Schema (draft 7 subset) validator for the keywords used in
// contract/tools: type, properties, required, additionalProperties:false,
// enum, items, minItems, maxItems, minimum, maximum, maxLength. Anything else
// in a tool schema fails the "known keywords" test so this stays honest.
export type JsonSchema = Record<string, unknown>;

export const KNOWN_KEYWORDS = new Set([
  "type", "properties", "required", "additionalProperties", "enum", "items",
  "minItems", "maxItems", "minimum", "maximum", "maxLength", "description", "format",
]);

export function validateSchema(schema: JsonSchema, value: unknown, path = "$"): string[] {
  const errs: string[] = [];
  const t = schema.type;
  const typeOk =
    t === "object" ? typeof value === "object" && value !== null && !Array.isArray(value)
    : t === "array" ? Array.isArray(value)
    : t === "string" ? typeof value === "string"
    : t === "integer" ? Number.isInteger(value)
    : t === "number" ? typeof value === "number"
    : t === "boolean" ? typeof value === "boolean"
    : t === undefined;
  if (!typeOk) return [`${path}: expected ${String(t)}`];
  if (Array.isArray(schema.enum) && !schema.enum.includes(value)) errs.push(`${path}: not in enum`);
  if (typeof value === "string" && typeof schema.maxLength === "number" && value.length > schema.maxLength) errs.push(`${path}: too long`);
  if (typeof value === "number") {
    if (typeof schema.minimum === "number" && value < schema.minimum) errs.push(`${path}: below minimum`);
    if (typeof schema.maximum === "number" && value > schema.maximum) errs.push(`${path}: above maximum`);
  }
  if (Array.isArray(value)) {
    if (typeof schema.minItems === "number" && value.length < schema.minItems) errs.push(`${path}: too few items`);
    if (typeof schema.maxItems === "number" && value.length > schema.maxItems) errs.push(`${path}: too many items`);
    const items = schema.items as JsonSchema | undefined;
    if (items) value.forEach((v, i) => errs.push(...validateSchema(items, v, `${path}[${i}]`)));
  }
  if (t === "object") {
    const obj = value as Record<string, unknown>;
    const props = (schema.properties ?? {}) as Record<string, JsonSchema>;
    for (const r of (schema.required ?? []) as string[]) if (!(r in obj)) errs.push(`${path}.${r}: required`);
    for (const [k, v] of Object.entries(obj)) {
      const ps = props[k];
      if (!ps) {
        if (schema.additionalProperties === false) errs.push(`${path}.${k}: not allowed`);
        continue;
      }
      errs.push(...validateSchema(ps, v, `${path}.${k}`));
    }
  }
  return errs;
}

export function unknownKeywords(schema: JsonSchema, path = "$"): string[] {
  const out: string[] = [];
  for (const [k, v] of Object.entries(schema)) {
    if (!KNOWN_KEYWORDS.has(k)) out.push(`${path}.${k}`);
    if (k === "properties") for (const [pk, ps] of Object.entries(v as Record<string, JsonSchema>)) out.push(...unknownKeywords(ps, `${path}.${pk}`));
    if (k === "items") out.push(...unknownKeywords(v as JsonSchema, `${path}[]`));
  }
  return out;
}
