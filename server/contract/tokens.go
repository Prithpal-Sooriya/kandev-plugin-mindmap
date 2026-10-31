package contract

import (
	"regexp"
	"strconv"
	"strings"
)

// TokenKind is the scope class of a bearer token. Raw tokens are
// "<kind>1.<secret>" or, for bridge tokens, "<kind>1.<port>.<secret>", where
// secret is 32 random bytes in base64url without padding (43 chars). Only the
// SHA-256 of the full token string is stored. Compare hashes in constant time.
type TokenKind string

const (
	TokenLocal    TokenKind = "mml" // in-Kandev UI session, issued by the `session` webhook
	TokenShare    TokenKind = "mms" // secret path segment of a share link: /m/<token>/
	TokenGuest    TokenKind = "mmg" // guest browser session, cookie mm_guest
	TokenRun      TokenKind = "mmr" // per-run bridge token, injected into the launch prompt
	TokenPersonal TokenKind = "mmp" // saved personal bridge token, for use outside Kandev
)

var TokenKinds = []TokenKind{TokenLocal, TokenShare, TokenGuest, TokenRun, TokenPersonal}

const (
	GuestCookieName = "mm_guest"
	ContractHeader  = "X-Mindmap-Contract"
)

var secretPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// ParsedToken is a token split into its parts.
type ParsedToken struct {
	Kind   TokenKind
	Port   int // bridge tokens only: the plugin's loopback port
	Secret string
}

// ParseToken splits a raw token. Bridge tokens (mmr, mmp) carry the loopback
// port so the bridge needs a single string to connect; path tokens do not.
func ParseToken(raw string) (ParsedToken, bool) {
	parts := strings.Split(raw, ".")
	if len(parts) < 2 || !strings.HasSuffix(parts[0], "1") {
		return ParsedToken{}, false
	}
	kind := TokenKind(strings.TrimSuffix(parts[0], "1"))
	bridge := kind == TokenRun || kind == TokenPersonal
	switch {
	case bridge && len(parts) == 3:
		port, err := strconv.Atoi(parts[1])
		if err != nil || port < 1 || port > 65535 || !secretPattern.MatchString(parts[2]) {
			return ParsedToken{}, false
		}
		return ParsedToken{Kind: kind, Port: port, Secret: parts[2]}, true
	case !bridge && len(parts) == 2:
		for _, k := range TokenKinds {
			if k == kind && secretPattern.MatchString(parts[1]) {
				return ParsedToken{Kind: kind, Secret: parts[1]}, true
			}
		}
	}
	return ParsedToken{}, false
}

// FormatToken assembles a raw token. port is ignored for non-bridge kinds.
func FormatToken(kind TokenKind, port int, secret string) string {
	if kind == TokenRun || kind == TokenPersonal {
		return string(kind) + "1." + strconv.Itoa(port) + "." + secret
	}
	return string(kind) + "1." + secret
}
