package share

import (
	"errors"
	"testing"
	"time"

	"kandev-plugin-mindmap/server/contract"
)

func TestTokenBucket(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(LimitConfig{OpsRate: 1, OpsBurst: 2, APIRate: 1, APIBurst: 1, RunRate: 1, RunBurst: 1, AssetRate: 1, AssetBurst: 1})
	l.SetClock(func() time.Time { return now })
	if err := l.AllowN(LimitOps, "s", 2); err != nil {
		t.Fatal(err)
	}
	err := l.Allow(LimitOps, "s")
	var ce *contract.Error
	if !errors.As(err, &ce) || ce.Code != contract.ErrRateLimited || ce.RetryMS < 1 {
		t.Fatalf("rate: %v", err)
	}
	if err := l.Allow(LimitAPI, "s"); err != nil {
		t.Fatal(err)
	}
	if err := l.Allow(LimitAPI, "other"); err != nil {
		t.Fatal("buckets are per session")
	}
	now = now.Add(2 * time.Second)
	if err := l.Allow(LimitOps, "s"); err != nil {
		t.Fatalf("refill: %v", err)
	}
	if err := l.Allow(LimitRuns, ""); err != nil {
		t.Fatal("empty session is not limited")
	}
}
