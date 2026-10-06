package wager

// State represents the processing state of a wager transaction.
type State string

const (
	StatePending          State = "PENDING"
	StatePendingReference State = "PENDING_REFERENCE"
	StateProcessed        State = "PROCESSED"
	StateRejected         State = "REJECTED"
	StateFailed           State = "FAILED"
)

// IsTerminal reports whether the state is immutable.
func (s State) IsTerminal() bool {
	switch s {
	case StateProcessed, StateRejected, StateFailed:
		return true
	default:
		return false
	}
}

func (s State) valid() bool {
	switch s {
	case StatePending, StatePendingReference, StateProcessed, StateRejected, StateFailed:
		return true
	default:
		return false
	}
}

func canTransition(from, to State) bool {
	if from == to {
		return false
	}
	if from.IsTerminal() {
		return false
	}

	switch from {
	case StatePending:
		switch to {
		case StateProcessed, StateRejected, StateFailed, StatePendingReference:
			return true
		}
	case StatePendingReference:
		switch to {
		case StateProcessed, StateRejected, StateFailed:
			return true
		}
	}
	return false
}
