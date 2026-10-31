package contract

import (
	"regexp"
	"strings"
)

// ID prefixes. An ID is `<prefix>_<suffix>` where suffix is 10-40 chars of
// [A-Za-z0-9]. The server mints 26-char ULID-style suffixes; clients may mint
// node, link and op IDs (random, at least 16 chars) for optimistic edits.
const (
	PrefixMap         = "map"
	PrefixNode        = "nd"
	PrefixLink        = "ln"
	PrefixOp          = "op"
	PrefixRun         = "run"
	PrefixProposal    = "prp"
	PrefixParticipant = "pt"
	PrefixAsset       = "as"
	PrefixActivity    = "ev"
	PrefixShare       = "sh"
	PrefixBatch       = "bt"
)

var idPattern = regexp.MustCompile(`^([a-z]{2,3})_([A-Za-z0-9]{10,40})$`)

// ValidID reports whether id is well-formed and carries the given prefix.
func ValidID(prefix, id string) bool {
	m := idPattern.FindStringSubmatch(id)
	return m != nil && m[1] == prefix
}

// RefPrefix marks an agent-chosen reference in tool arguments. Tools that take
// a node ID also accept `@<ref>` where ref was set on a node created earlier in
// the same run (see AddNode.Ref). Refs are scoped to one run.
const RefPrefix = "@"

var refPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)

// ValidRef reports whether ref (without the @) is a legal run-scoped ref.
func ValidRef(ref string) bool { return refPattern.MatchString(ref) }

// SplitNodeRef classifies a tool argument that names a node. It returns
// (ref, true) for `@ref` and (id, false) otherwise. Callers still validate.
func SplitNodeRef(s string) (string, bool) {
	if strings.HasPrefix(s, RefPrefix) {
		return s[len(RefPrefix):], true
	}
	return s, false
}

// ValidNodeRef reports whether s is a node ID or an `@ref`.
func ValidNodeRef(s string) bool {
	if v, isRef := SplitNodeRef(s); isRef {
		return ValidRef(v)
	}
	return ValidID(PrefixNode, s)
}
