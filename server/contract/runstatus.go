package contract

// Run status derivation from Kandev events. Spike finding: agent.completed and
// agent.failed may never fire, so task_session.state_changed is the source of
// truth. Payloads carry task_id, session_id and (usually) new_state; some lack
// new_state and must be ignored.

// Kandev task session states (pkg/api/v1 TaskSessionState).
const (
	SessionCreated         = "CREATED"
	SessionStarting        = "STARTING"
	SessionRunning         = "RUNNING"
	SessionIdle            = "IDLE" // office surface: between turns
	SessionWaitingForInput = "WAITING_FOR_INPUT"
	SessionCompleted       = "COMPLETED"
	SessionFailed          = "FAILED"
	SessionCancelled       = "CANCELLED"
)

// RunFacts is the plugin-side state that disambiguates a session state.
type RunFacts struct {
	Status        RunStatus
	Reported      bool         // report tool called this turn or earlier
	ReportStatus  ReportStatus // what report asked for
	OpenQuestion  bool         // an ask_human question of this run is unanswered
	OpenProposal  bool         // proposal status open or ready
	StopRequested bool         // a human pressed Stop or the kill switch fired
}

// NextRunStatus returns the run status after a task_session.state_changed
// with newState. ok is false when the event must be ignored (empty or unknown
// state). Rules, in order:
//
//	CREATED, STARTING          -> queued (never downgrades a started run)
//	RUNNING                    -> running (also re-opens waiting/review/done after a follow-up)
//	FAILED                     -> failed
//	CANCELLED                  -> stopped
//	WAITING_FOR_INPUT, IDLE, COMPLETED -> settle():
//	    stop requested         -> stopped
//	    open question          -> waiting(question)
//	    reported               -> review if report=review or proposal open, else done
//	    proposal open          -> review
//	    COMPLETED              -> done
//	    otherwise              -> waiting(turn_ended)
func NextRunStatus(f RunFacts, newState string) (RunStatus, WaitingReason, bool) {
	switch newState {
	case SessionCreated, SessionStarting:
		if f.Status == "" || f.Status == RunQueued {
			return RunQueued, "", true
		}
		return f.Status, "", false
	case SessionRunning:
		if f.StopRequested {
			return RunStopped, "", true
		}
		return RunRunning, "", true
	case SessionFailed:
		return RunFailed, "", true
	case SessionCancelled:
		return RunStopped, "", true
	case SessionWaitingForInput, SessionIdle, SessionCompleted:
		switch {
		case f.StopRequested:
			return RunStopped, "", true
		case f.OpenQuestion:
			return RunWaiting, WaitingQuestion, true
		case f.Reported:
			if f.ReportStatus == ReportReview || f.OpenProposal {
				return RunReview, "", true
			}
			return RunDone, "", true
		case f.OpenProposal:
			return RunReview, "", true
		case newState == SessionCompleted:
			return RunDone, "", true
		default:
			return RunWaiting, WaitingTurnEnded, true
		}
	}
	return f.Status, "", false
}

// NodeStatusForRun is the status ring of the node a run works on.
func NodeStatusForRun(s RunStatus) NodeStatus {
	switch s {
	case RunQueued:
		return StatusClaimed
	case RunRunning, RunWaiting:
		return StatusRunning
	case RunReview:
		return StatusReview
	case RunDone:
		return StatusDone
	case RunFailed:
		return StatusFailed
	}
	return StatusIdle // stopped
}
