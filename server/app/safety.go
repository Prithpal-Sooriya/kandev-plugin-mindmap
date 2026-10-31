package app

import (
	"context"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/share"
	"kandev-plugin-mindmap/server/store"
)

// StopAllRuns is the Phase 2a hook the kill switch calls before it marks
// runs stopped. Implement StopAllRuns to message agents (or otherwise stop
// the Kandev tasks). Do not call Service.StopAllRuns from the hook: Service
// calls the hook, then marks every non-terminal run stopped and revokes its
// run token. Leave Service.Stops nil until Phase 2a provides an implementation.
type StopAllRuns interface {
	StopAllRuns(ctx context.Context) error
}

// CheckRunCaps is the shared checkpoint for run launch. Service.StartRun
// calls it. Phase 2a must call it too for any launch path that does not go
// through StartRun. requested may be nil (inherit the map default). extra is
// how many runs are about to start (1 for a single run, len(agents) for a swarm).
func (s *Service) CheckRunCaps(mapID string, requested *contract.Budget, extra int) error {
	m, err := s.Store.GetMap(mapID)
	if err != nil {
		return err
	}
	active, err := s.Store.CountActiveRuns(mapID)
	if err != nil {
		return err
	}
	caps, err := s.runCaps(m)
	if err != nil {
		return err
	}
	return share.CheckRunCaps(active, extra, resolveBudget(requested, m.Settings.DefaultBudget), caps)
}

// runCaps is the map ceiling. A shared map with nothing set by the host
// gets a non-zero default concurrency and budget; an unshared map stays
// unlimited where the settings are zero.
func (s *Service) runCaps(m contract.Map) (share.RunCaps, error) {
	_, shared, err := s.Store.LiveShare(m.ID)
	if err != nil {
		return share.RunCaps{}, err
	}
	cfg := share.LoadSettings(s.Store)
	caps := share.RunCaps{
		MaxConcurrent: share.Tighter(m.Settings.MaxConcurrentRun, cfg.MaxConcurrentRuns),
		MaxBudget: contract.Budget{
			MaxNodes:     share.Tighter(budgetField(m.Settings.DefaultBudget).MaxNodes, cfg.MaxNodes),
			MaxDepth:     share.Tighter(budgetField(m.Settings.DefaultBudget).MaxDepth, cfg.MaxDepth),
			MaxSubagents: share.Tighter(budgetField(m.Settings.DefaultBudget).MaxSubagents, cfg.MaxSubagents),
			MaxMinutes:   share.Tighter(budgetField(m.Settings.DefaultBudget).MaxMinutes, cfg.MaxMinutes),
		},
	}
	return share.FillSharedDefaults(caps, shared, store.DefaultBudget), nil
}

// clampGuestBudget lowers a guest's requested budget to the shared-map
// ceiling. The host's own runs are left alone. Profiles are not touched.
func (s *Service) clampGuestBudget(nr store.NewRun) (store.NewRun, error) {
	if nr.StartedBy == "" || nr.ParentRunID != "" {
		return nr, nil
	}
	pt, err := s.Store.GetParticipant(nr.StartedBy)
	if err != nil || pt.IsHost || pt.Kind != contract.ParticipantHuman {
		return nr, nil
	}
	m, err := s.Store.GetMap(nr.MapID)
	if err != nil {
		return nr, err
	}
	caps, err := s.runCaps(m)
	if err != nil {
		return nr, err
	}
	if caps.MaxBudget == (contract.Budget{}) {
		return nr, nil
	}
	resolved := resolveBudget(nr.Budget, m.Settings.DefaultBudget)
	clamped := share.ClampBudget(resolved, caps.MaxBudget)
	nr.Budget = &clamped
	return nr, nil
}

// GuestRunNeedsApproval reports whether this participant's run must wait for
// the host. The map setting and the plugin setting both default off. Phase 2a
// calls share.GuestRunNeedsApproval (or this method) before Launch and, when
// it is true, queues the run with waiting_reason=approval instead of creating
// a task. The HTTP handler refuses guest runs while that hold is unwired.
func (s *Service) GuestRunNeedsApproval(mapID, participantID string) (bool, error) {
	m, err := s.Store.GetMap(mapID)
	if err != nil {
		return false, err
	}
	pt, err := s.Store.GetParticipant(participantID)
	if err != nil {
		return false, err
	}
	cfg := share.LoadSettings(s.Store)
	return share.GuestRunNeedsApproval(m.Settings, cfg.RequireGuestRunApproval, pt), nil
}

// StopAllRuns asks Phase 2a to stop active tasks, then marks every
// non-terminal run stopped and revokes its run token.
func (s *Service) StopAllRuns(ctx context.Context) error {
	if s.Stops != nil {
		if err := s.Stops.StopAllRuns(ctx); err != nil && s.Logf != nil {
			s.Logf("mindmap: stop runs: %v", err)
		}
	}
	runs, err := s.Store.ListActiveRuns()
	if err != nil {
		return err
	}
	for _, r := range runs {
		if _, uerr := s.UpdateRun(r.ID, func(cur *store.RunRecord) bool {
			if cur.Status.Terminal() {
				return false
			}
			cur.Status = contract.RunStopped
			cur.StopRequested = true
			return true
		}); uerr != nil && err == nil {
			err = uerr
		}
		if rerr := s.Store.RevokeRunTokens(r.ID); rerr != nil && err == nil {
			err = rerr
		}
	}
	return err
}

func budgetField(b *contract.Budget) contract.Budget {
	if b == nil {
		return contract.Budget{}
	}
	return *b
}

// resolveBudget mirrors store.resolveBudget: map default, then the request,
// over the built-in default. A positive field wins.
func resolveBudget(req, mapDefault *contract.Budget) contract.Budget {
	b := store.DefaultBudget
	for _, src := range []*contract.Budget{mapDefault, req} {
		if src == nil {
			continue
		}
		if src.MaxDepth > 0 {
			b.MaxDepth = src.MaxDepth
		}
		if src.MaxNodes > 0 {
			b.MaxNodes = src.MaxNodes
		}
		if src.MaxSubagents > 0 {
			b.MaxSubagents = src.MaxSubagents
		}
		if src.MaxMinutes > 0 {
			b.MaxMinutes = src.MaxMinutes
		}
	}
	return b
}
