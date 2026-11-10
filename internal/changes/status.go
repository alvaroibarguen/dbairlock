package changes

import "fmt"

// Status is where a change is in its life (PLAN.md section 6).
type Status string

// Statuses.
const (
	StatusDraft         Status = "draft"
	StatusPendingReview Status = "pending_review"
	StatusApproved      Status = "approved"
	StatusExecuting     Status = "executing"
	StatusExecuted      Status = "executed"
	StatusFailed        Status = "failed"
	StatusRejected      Status = "rejected"
	StatusExpired       Status = "expired"
	StatusCancelled     Status = "cancelled"
)

// Statuses returns every status in lifecycle order.
func Statuses() []Status {
	return []Status{
		StatusDraft, StatusPendingReview, StatusApproved, StatusExecuting, StatusExecuted,
		StatusFailed, StatusRejected, StatusExpired, StatusCancelled,
	}
}

// Open reports whether a change in s can still run: draft, in review or
// approved.
func (s Status) Open() bool {
	return s == StatusDraft || s == StatusPendingReview || s == StatusApproved
}

// Event is something that happens to a change.
type Event string

// Events.
const (
	// EventSubmit: the author submits a draft for review.
	EventSubmit Event = "submit"
	// EventApprove: an approval brought the count to the required number.
	EventApprove Event = "approve"
	// EventReject: a reviewer rejected the change.
	EventReject Event = "reject"
	// EventCancel: the author or an admin cancelled it, or the author was
	// removed.
	EventCancel Event = "cancel"
	// EventExpire: the approval lifetime ended.
	EventExpire Event = "expire"
	// EventApprovalLost: an approved change fell below its required
	// approvals before execution began (an approving reviewer was removed).
	EventApprovalLost Event = "approval_lost"
	// EventClaim: the executor claimed the change.
	EventClaim Event = "claim"
	// EventSucceed: the target committed.
	EventSucceed Event = "succeed"
	// EventFail: the execution failed, drifted, or was interrupted.
	EventFail Event = "fail"
	// EventDefer: the target could not be reached before the transaction
	// began; nothing ran and the change waits for another attempt.
	EventDefer Event = "defer"
)

// Events returns every event.
func Events() []Event {
	return []Event{
		EventSubmit, EventApprove, EventReject, EventCancel, EventExpire,
		EventApprovalLost, EventClaim, EventSucceed, EventFail, EventDefer,
	}
}

// transitions is the diagram of PLAN.md section 6, and nothing else:
//
//	draft -> pending_review -> approved -> executing -> executed
//	                |             |            |
//	                |             |            +-> failed
//	                +-> rejected  +-> expired
//	draft, pending_review, approved -> cancelled
//	pending_review -> expired
//	approved -> pending_review   (approval lost)
//	executing -> approved        (target unreachable; retried)
var transitions = map[Status]map[Event]Status{
	StatusDraft: {
		EventSubmit: StatusPendingReview,
		EventCancel: StatusCancelled,
	},
	StatusPendingReview: {
		EventApprove: StatusApproved,
		EventReject:  StatusRejected,
		EventCancel:  StatusCancelled,
		EventExpire:  StatusExpired,
	},
	StatusApproved: {
		EventClaim:        StatusExecuting,
		EventCancel:       StatusCancelled,
		EventExpire:       StatusExpired,
		EventApprovalLost: StatusPendingReview,
	},
	StatusExecuting: {
		EventSucceed: StatusExecuted,
		EventFail:    StatusFailed,
		EventDefer:   StatusApproved,
	},
}

// TransitionError means an event is not allowed in a status.
type TransitionError struct {
	From  Status
	Event Event
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("changes: %q is not allowed for a change that is %s", e.Event, e.From)
}

// Is makes errors.Is(err, ErrInvalidState) match.
func (e *TransitionError) Is(target error) bool { return target == ErrInvalidState }

// transition returns the status a change in status from moves to on ev, or
// a *TransitionError. It is the only place the state machine is defined;
// every status change in this package goes through it.
func transition(from Status, ev Event) (Status, error) {
	if to, ok := transitions[from][ev]; ok {
		return to, nil
	}
	return from, &TransitionError{From: from, Event: ev}
}
