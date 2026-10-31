// Error codes. Mirrors server/contract/errors.go.

export const ERROR_CODES = [
  "bad_request", "invalid_op", "not_found", "unauthorized", "forbidden", "not_a_run", "run_ended",
  "cycle", "root_immutable", "deleted", "conflict", "claimed", "locked", "budget_exceeded",
  "rate_limited", "too_large", "unsupported_media", "fetch_failed", "proposal_closed", "resync",
  "version", "not_implemented", "not_installed", "unavailable", "internal",
] as const;
export type ErrorCode = (typeof ERROR_CODES)[number];

export const HTTP_STATUS: Record<ErrorCode, number> = {
  bad_request: 400, invalid_op: 400, cycle: 400, root_immutable: 400, deleted: 400,
  unauthorized: 401,
  forbidden: 403, not_a_run: 403, run_ended: 403, budget_exceeded: 403,
  not_found: 404,
  conflict: 409, claimed: 409, locked: 409, proposal_closed: 409, resync: 409,
  too_large: 413, unsupported_media: 415, version: 426, rate_limited: 429,
  not_implemented: 501, not_installed: 503, fetch_failed: 502, unavailable: 503, internal: 500,
};

/** Thrown by client helpers when the server returns a contract Error. */
export class ContractError extends Error {
  constructor(
    readonly code: ErrorCode,
    message: string,
    readonly field?: string,
    readonly retryMs?: number,
  ) {
    super(field ? `${code}: ${message} (${field})` : `${code}: ${message}`);
    this.name = "ContractError";
  }
}
