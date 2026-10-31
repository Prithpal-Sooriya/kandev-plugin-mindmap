package contract

import (
	"errors"
	"fmt"
)

// ErrorCode is a stable, machine-readable error identifier used on every
// surface: HTTP bodies, WS reject/error messages, and agent tool results.
type ErrorCode string

const (
	ErrBadRequest       ErrorCode = "bad_request"       // malformed JSON or arguments
	ErrInvalidOp        ErrorCode = "invalid_op"        // op failed structural validation
	ErrNotFound         ErrorCode = "not_found"         // map/node/run/asset does not exist (or is hidden)
	ErrUnauthorized     ErrorCode = "unauthorized"      // missing/expired/revoked token on a non-guest route
	ErrForbidden        ErrorCode = "forbidden"         // token valid but scope does not cover the target
	ErrNotARun          ErrorCode = "not_a_run"         // agent tool called from a task with no mind map run
	ErrRunEnded         ErrorCode = "run_ended"         // run is terminal and past its grace period
	ErrCycle            ErrorCode = "cycle"             // move would make a node its own ancestor
	ErrRootImmutable    ErrorCode = "root_immutable"    // root cannot be moved, deleted or merged
	ErrDeleted          ErrorCode = "deleted"           // target node is tombstoned
	ErrConflict         ErrorCode = "conflict"          // op no longer applies (e.g. proposal item vs live map)
	ErrClaimed          ErrorCode = "claimed"           // node claimed by another participant
	ErrLocked           ErrorCode = "locked"            // field soft-locked by another participant
	ErrBudgetExceeded   ErrorCode = "budget_exceeded"   // run budget (nodes/depth/subagents) exhausted
	ErrRateLimited      ErrorCode = "rate_limited"      // per-session or per-run rate limit
	ErrTooLarge         ErrorCode = "too_large"         // payload or batch over a limit
	ErrUnsupportedMedia ErrorCode = "unsupported_media" // asset MIME type not allowed
	ErrFetchFailed      ErrorCode = "fetch_failed"      // attach url/file could not be read
	ErrProposalClosed   ErrorCode = "proposal_closed"   // proposal already resolved
	ErrResync           ErrorCode = "resync"            // client is too far behind; take a fresh snapshot
	ErrVersion          ErrorCode = "version"           // contract version not supported
	ErrNotImplemented   ErrorCode = "not_implemented"
	ErrNotInstalled     ErrorCode = "not_installed" // tunnel binary is not on PATH; message includes the install command
	ErrUnavailable      ErrorCode = "unavailable"   // host API or tunnel unavailable; retry later
	ErrInternal         ErrorCode = "internal"
)

var ErrorCodes = []ErrorCode{
	ErrBadRequest, ErrInvalidOp, ErrNotFound, ErrUnauthorized, ErrForbidden, ErrNotARun, ErrRunEnded,
	ErrCycle, ErrRootImmutable, ErrDeleted, ErrConflict, ErrClaimed, ErrLocked, ErrBudgetExceeded,
	ErrRateLimited, ErrTooLarge, ErrUnsupportedMedia, ErrFetchFailed, ErrProposalClosed, ErrResync,
	ErrVersion, ErrNotImplemented, ErrNotInstalled, ErrUnavailable, ErrInternal,
}

// HTTPStatus maps a code to the HTTP status used on loopback and webhook
// routes. Guest routes return 404 instead of 401/403 so a wrong token is
// indistinguishable from a missing map.
func (c ErrorCode) HTTPStatus() int {
	switch c {
	case ErrBadRequest, ErrInvalidOp, ErrCycle, ErrRootImmutable, ErrDeleted:
		return 400
	case ErrUnauthorized:
		return 401
	case ErrForbidden, ErrNotARun, ErrRunEnded, ErrBudgetExceeded:
		return 403
	case ErrNotFound:
		return 404
	case ErrConflict, ErrClaimed, ErrLocked, ErrProposalClosed, ErrResync:
		return 409
	case ErrTooLarge:
		return 413
	case ErrUnsupportedMedia:
		return 415
	case ErrVersion:
		return 426
	case ErrRateLimited:
		return 429
	case ErrNotImplemented:
		return 501
	case ErrNotInstalled, ErrUnavailable:
		return 503
	case ErrFetchFailed:
		return 502
	default:
		return 500
	}
}

// Error is the wire error object. HTTP error bodies are `{"error": Error}`.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	// Field points at the offending argument or op field, e.g. "nodes[2].title".
	Field string `json:"field,omitempty"`
	// Retry, when set, is the suggested wait in milliseconds (rate_limited, unavailable).
	RetryMS int `json:"retry_ms,omitempty"`
}

func (e *Error) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Field)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Errf builds an *Error.
func Errf(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// FieldErr builds an *Error that names the offending field.
func FieldErr(code ErrorCode, field, format string, args ...any) *Error {
	return &Error{Code: code, Field: field, Message: fmt.Sprintf(format, args...)}
}

// AsError extracts an *Error from err, wrapping anything else as internal.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: ErrInternal, Message: err.Error()}
}

// ErrorBody is the HTTP error envelope.
type ErrorBody struct {
	Error Error `json:"error"`
}
