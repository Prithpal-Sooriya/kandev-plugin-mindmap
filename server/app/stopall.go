package app

import (
	"context"
	"sync"

	"kandev-plugin-mindmap/server/contract"
)

// KillReason is the stop reason the kill switch gives agents.
const KillReason = "the host used the kill switch"

// RunStopper is the Kandev side of the kill switch (Service.Stops). There is
// no Host API to stop a session, so each live run gets StopRun: a stop
// message to its agent, revoked run tokens, released claims, status stopped.
// It never calls Service.StopAllRuns, which calls it.
type RunStopper struct{ Svc *Service }

// StopAllRuns stops every non-terminal run, messaging agents in parallel.
// Coordinators go last; their lens runs are already stopped by then.
func (r RunStopper) StopAllRuns(ctx context.Context) error {
	s := r.Svc
	runs, err := s.Store.ListActiveRuns()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	stop := func(id string) {
		if _, err := s.StopRun(ctx, id, "", KillReason); err != nil {
			mu.Lock()
			if first == nil {
				first = err
			}
			mu.Unlock()
		}
	}
	for _, run := range runs {
		if isCoordinator(run) || run.Status == contract.RunStopped {
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			stop(id)
		}(run.ID)
	}
	wg.Wait()
	for _, run := range runs {
		if isCoordinator(run) {
			stop(run.ID)
		}
	}
	return first
}
