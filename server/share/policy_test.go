package share

import (
	"errors"
	"testing"

	"kandev-plugin-mindmap/server/contract"
)

func TestCheckRunCaps(t *testing.T) {
	caps := RunCaps{MaxConcurrent: 2, MaxBudget: contract.Budget{MaxNodes: 10, MaxDepth: 3, MaxSubagents: 2, MaxMinutes: 15}}
	ok := contract.Budget{MaxNodes: 10, MaxDepth: 3, MaxSubagents: 2, MaxMinutes: 15}
	if err := CheckRunCaps(1, 1, ok, caps); err != nil {
		t.Fatal(err)
	}
	if err := CheckRunCaps(2, 1, ok, caps); !isCode(err, contract.ErrBudgetExceeded) {
		t.Fatalf("concurrency: %v", err)
	}
	over := ok
	over.MaxNodes = 11
	if err := CheckRunCaps(0, 1, over, caps); !isCode(err, contract.ErrBudgetExceeded) {
		t.Fatalf("nodes: %v", err)
	}
	over = ok
	over.MaxDepth = 4
	if err := CheckRunCaps(0, 1, over, caps); !isCode(err, contract.ErrBudgetExceeded) {
		t.Fatalf("depth: %v", err)
	}
	if err := CheckRunCaps(100, 5, contract.Budget{MaxNodes: 999}, RunCaps{}); err != nil {
		t.Fatalf("zero caps are unlimited: %v", err)
	}
}

func TestSharedDefaultsAndClamp(t *testing.T) {
	fallback := contract.Budget{MaxDepth: 4, MaxNodes: 60, MaxSubagents: 4, MaxMinutes: 30}
	filled := FillSharedDefaults(RunCaps{}, true, fallback)
	if filled.MaxConcurrent != SharedDefaultConcurrent || filled.MaxBudget != fallback {
		t.Fatalf("shared defaults: %+v", filled)
	}
	if got := FillSharedDefaults(RunCaps{}, false, fallback); got.MaxConcurrent != 0 || got.MaxBudget != (contract.Budget{}) {
		t.Fatalf("unshared stays unlimited: %+v", got)
	}
	host := RunCaps{MaxConcurrent: 2, MaxBudget: contract.Budget{MaxMinutes: 90, MaxSubagents: 8}}
	kept := FillSharedDefaults(host, true, fallback)
	if kept.MaxConcurrent != 2 || kept.MaxBudget.MaxMinutes != 90 || kept.MaxBudget.MaxSubagents != 8 || kept.MaxBudget.MaxNodes != 60 {
		t.Fatalf("host cap overwritten: %+v", kept)
	}
	clamped := ClampBudget(contract.Budget{MaxMinutes: 100000, MaxSubagents: 500, MaxNodes: 10}, filled.MaxBudget)
	if clamped.MaxMinutes != 30 || clamped.MaxSubagents != 4 || clamped.MaxNodes != 10 {
		t.Fatalf("clamp: %+v", clamped)
	}
}

func TestTighter(t *testing.T) {
	if Tighter(0, 4) != 4 || Tighter(3, 0) != 3 || Tighter(2, 5) != 2 || Tighter(9, 4) != 4 {
		t.Fatalf("tighter %d %d %d %d", Tighter(0, 4), Tighter(3, 0), Tighter(2, 5), Tighter(9, 4))
	}
}

func TestGuestRunNeedsApproval(t *testing.T) {
	host := contract.Participant{IsHost: true, Kind: contract.ParticipantHuman}
	guest := contract.Participant{Kind: contract.ParticipantHuman}
	agent := contract.Participant{Kind: contract.ParticipantAgent}
	off := contract.MapSettings{}
	on := contract.MapSettings{RequireGuestRunApproval: true}
	if GuestRunNeedsApproval(off, false, guest) {
		t.Fatal("default is off")
	}
	if !GuestRunNeedsApproval(on, false, guest) || !GuestRunNeedsApproval(off, true, guest) {
		t.Fatal("map or plugin setting holds a guest")
	}
	if GuestRunNeedsApproval(on, true, host) || GuestRunNeedsApproval(on, true, agent) {
		t.Fatal("host and agents are not held")
	}
}

func isCode(err error, code contract.ErrorCode) bool {
	var ce *contract.Error
	return errors.As(err, &ce) && ce.Code == code
}
