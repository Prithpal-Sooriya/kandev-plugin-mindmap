package share

import "kandev-plugin-mindmap/server/contract"

// RunCaps are the ceilings for one map. A zero field means that dimension
// is not capped.
type RunCaps struct {
	MaxConcurrent int
	MaxBudget     contract.Budget
}

// CheckRunCaps reports whether starting `starting` more runs with the
// resolved budget would exceed the cap. Phase 2a calls this before every
// launch that does not go through app.Service.StartRun (StartRun calls it
// itself). active is the current number of non-terminal runs on the map.
func CheckRunCaps(active, starting int, resolved contract.Budget, caps RunCaps) error {
	if starting < 1 {
		starting = 1
	}
	if caps.MaxConcurrent > 0 && active+starting > caps.MaxConcurrent {
		return contract.Errf(contract.ErrBudgetExceeded, "this map allows %d concurrent runs and %d are already active", caps.MaxConcurrent, active)
	}
	if over(resolved.MaxNodes, caps.MaxBudget.MaxNodes) {
		return contract.Errf(contract.ErrBudgetExceeded, "run budget of %d nodes exceeds the cap of %d", resolved.MaxNodes, caps.MaxBudget.MaxNodes)
	}
	if over(resolved.MaxDepth, caps.MaxBudget.MaxDepth) {
		return contract.Errf(contract.ErrBudgetExceeded, "run budget depth %d exceeds the cap of %d", resolved.MaxDepth, caps.MaxBudget.MaxDepth)
	}
	if over(resolved.MaxSubagents, caps.MaxBudget.MaxSubagents) {
		return contract.Errf(contract.ErrBudgetExceeded, "run budget of %d sub-agents exceeds the cap of %d", resolved.MaxSubagents, caps.MaxBudget.MaxSubagents)
	}
	if over(resolved.MaxMinutes, caps.MaxBudget.MaxMinutes) {
		return contract.Errf(contract.ErrBudgetExceeded, "run budget of %d minutes exceeds the cap of %d", resolved.MaxMinutes, caps.MaxBudget.MaxMinutes)
	}
	return nil
}

func over(got, cap int) bool { return cap > 0 && got > cap }

// Tighter keeps the smaller positive limit. Zero means "no limit on this side".
func Tighter(a, b int) int {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}

// GuestRunNeedsApproval reports whether a run started by this participant
// must wait for the host. Off unless the map setting or the plugin setting
// is on. The host and agent participants are never held. Phase 2a calls
// this before Launch and, when it is true, leaves the run queued with
// waiting_reason=approval instead of creating a task. The HTTP start route
// refuses until that hold exists.
func GuestRunNeedsApproval(settings contract.MapSettings, plugin bool, starter contract.Participant) bool {
	if starter.IsHost || starter.Kind != contract.ParticipantHuman {
		return false
	}
	return settings.RequireGuestRunApproval || plugin
}
