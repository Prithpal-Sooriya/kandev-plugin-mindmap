package share

import (
	"math"
	"sync"
	"time"

	"kandev-plugin-mindmap/server/contract"
)

// LimitKind is one token bucket per session.
type LimitKind int

const (
	LimitOps LimitKind = iota
	LimitAPI
	LimitRuns
	LimitAssets
)

// LimitConfig is the sustained rate (tokens per second) and burst of each bucket.
type LimitConfig struct {
	OpsRate, OpsBurst     float64
	APIRate, APIBurst     float64
	RunRate, RunBurst     float64
	AssetRate, AssetBurst float64
}

// DefaultLimits are the protocol §2 caps.
func DefaultLimits() LimitConfig {
	return LimitConfig{
		OpsRate: OpsRate(), OpsBurst: contract.OpsBurst,
		APIRate: contract.APIPerSec, APIBurst: contract.APIBurst,
		RunRate: float64(contract.RunStartsPerMin) / 60, RunBurst: contract.RunStartsPerMin,
		AssetRate: float64(contract.AssetUploadsPerMin) / 60, AssetBurst: contract.AssetUploadsPerMin,
	}
}

func OpsRate() float64 { return contract.OpsPerSec }

// Limiter is a set of token buckets keyed by session id.
type Limiter struct {
	cfg LimitConfig
	now func() time.Time

	mu       sync.Mutex
	sessions map[string]*buckets
}

type buckets struct {
	ops, api, runs, assets *bucket
}

type bucket struct {
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

// NewLimiter builds a limiter. A zero config uses DefaultLimits.
func NewLimiter(cfg LimitConfig) *Limiter {
	if cfg.OpsBurst == 0 && cfg.APIBurst == 0 {
		cfg = DefaultLimits()
	}
	return &Limiter{cfg: cfg, now: time.Now, sessions: map[string]*buckets{}}
}

// SetClock replaces the clock (tests).
func (l *Limiter) SetClock(now func() time.Time) { l.now = now }

// Allow takes one token. A refusal is *contract.Error rate_limited with retry_ms.
func (l *Limiter) Allow(kind LimitKind, session string) error {
	return l.AllowN(kind, session, 1)
}

// AllowN takes n tokens (n ops in one submit). n <= 0 is a no-op.
func (l *Limiter) AllowN(kind LimitKind, session string, n int) error {
	if session == "" || n <= 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.bucket(session, kind)
	ok, retry := b.take(float64(n), l.now())
	if ok {
		return nil
	}
	ms := int(retry / time.Millisecond)
	if ms < 1 {
		ms = 1
	}
	return &contract.Error{Code: contract.ErrRateLimited, Message: "slow down", RetryMS: ms}
}

func (l *Limiter) bucket(session string, kind LimitKind) *bucket {
	s := l.sessions[session]
	if s == nil {
		s = &buckets{
			ops:    newBucket(l.cfg.OpsRate, l.cfg.OpsBurst),
			api:    newBucket(l.cfg.APIRate, l.cfg.APIBurst),
			runs:   newBucket(l.cfg.RunRate, l.cfg.RunBurst),
			assets: newBucket(l.cfg.AssetRate, l.cfg.AssetBurst),
		}
		l.sessions[session] = s
	}
	switch kind {
	case LimitAPI:
		return s.api
	case LimitRuns:
		return s.runs
	case LimitAssets:
		return s.assets
	default:
		return s.ops
	}
}

func newBucket(rate, burst float64) *bucket {
	if rate <= 0 {
		rate = 0.001
	}
	if burst < 1 {
		burst = 1
	}
	return &bucket{rate: rate, burst: burst, tokens: burst}
}

func (b *bucket) take(n float64, now time.Time) (bool, time.Duration) {
	if !b.last.IsZero() {
		elapsed := now.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens = math.Min(b.burst, b.tokens+elapsed*b.rate)
		}
	}
	b.last = now
	if b.tokens >= n {
		b.tokens -= n
		return true, 0
	}
	need := n - b.tokens
	return false, time.Duration(need / b.rate * float64(time.Second))
}
