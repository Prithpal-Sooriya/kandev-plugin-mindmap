package share

import (
	"errors"
	"strconv"
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

func TestIdleBucketsEvicted(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(DefaultLimits())
	l.SetClock(func() time.Time { return now })
	for i := 0; i < 20; i++ {
		if err := l.Allow(LimitAPI, "s"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if l.Sessions() != 20 {
		t.Fatalf("sessions %d", l.Sessions())
	}
	now = now.Add(IdleBucketTTL - time.Second)
	if err := l.Allow(LimitAPI, "fresh"); err != nil {
		t.Fatal(err)
	}
	if l.Sessions() != 21 {
		t.Fatalf("early sweep %d", l.Sessions())
	}
	now = now.Add(time.Second)
	if err := l.Allow(LimitAPI, "later"); err != nil {
		t.Fatal(err)
	}
	if l.Sessions() != 2 {
		t.Fatalf("idle sessions left: %d", l.Sessions())
	}

	spent := NewLimiter(LimitConfig{
		OpsRate: 100, OpsBurst: 10, APIRate: 100, APIBurst: 10,
		RunRate: 0.001, RunBurst: 1, AssetRate: 1, AssetBurst: 1,
		JoinRate: 1, JoinBurst: 1,
	})
	spent.SetClock(func() time.Time { return now })
	if err := spent.Allow(LimitRuns, "runs"); err != nil {
		t.Fatal(err)
	}
	if err := spent.Allow(LimitRuns, "runs"); err == nil {
		t.Fatal("burst of 1 should refuse the second start")
	}
	now = now.Add(time.Minute)
	if err := spent.Allow(LimitRuns, "runs"); err == nil {
		t.Fatal("a minute of idle must not reset the run bucket")
	}
}
