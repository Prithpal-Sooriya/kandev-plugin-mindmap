package contract

import "time"

// Size and rate limits shared by server validation, tool schemas and UI.
// Tool JSON schemas repeat the per-field limits as maxLength/maxItems; the
// contract test checks they agree.
const (
	MaxTitleLen        = 200
	MaxBodyLen         = 64 * 1024
	MaxTags            = 20
	MaxTagLen          = 40
	MaxSources         = 50
	MaxSourceTitleLen  = 300
	MaxQuoteLen        = 1000
	MaxURLLen          = 2048
	MaxLinkLabelLen    = 60
	MaxLinkReasonLen   = 500
	MaxCaptionLen      = 300
	MaxQuestionLen     = 2000
	MaxSummaryLen      = 8000
	MaxOpsPerBatch     = 200 // ops per WS `ops` message or HTTP submit
	MaxAddNodes        = 100 // total nodes (all depths) per add call
	MaxAddDepth        = 3   // nesting levels inside one add call (schema unrolls 3)
	MaxRestructureOps  = 50
	MaxSpawnPerCall    = 8
	MaxReadLimit       = 500
	MaxSearchLimit     = 100
	MaxMapNodes        = 20000
	MaxAssetBytes      = 10 << 20 // decoded size, any route
	MaxBase64Chars     = 1400000  // ~1 MiB decoded; keep agent payloads small
	MaxOrderKeyLen     = 64
	MaxDisplayNameLen  = 40
	MaxDocPathLen      = 200
	MaxMergeSources    = 20
	DefaultReadDepth   = 3
	DefaultReadLimit   = 200
	DefaultSearchLimit = 20
	DefaultClaimTTL    = 30 * time.Minute
	MaxClaimTTL        = 2 * time.Hour
	SoftLockTTL        = 15 * time.Second // refreshed by presence while editing
	LongPollMaxHold    = 20 * time.Second // Kandev webhooks die at 30s
	RunTokenGrace      = 10 * time.Minute // after a terminal run status
	PresenceTimeout    = 30 * time.Second // participant shown offline after this
	AgentToolTimeout   = 25 * time.Second // Kandev cuts plugin calls at 30s

	// Per guest or local session (protocol §2). API is every /api/ call;
	// ops are counted separately inside submit. Asset uploads are also an
	// API call, and have their own per-minute cap.
	OpsPerSec          = 50
	OpsBurst           = 200
	APIPerSec          = 20
	APIBurst           = 60
	RunStartsPerMin    = 10
	AssetUploadsPerMin = 20
	GuestJoinsPerMin   = 10 // new guest sessions minted per share link
	GuestIdleTTL       = 30 * 24 * time.Hour
)
