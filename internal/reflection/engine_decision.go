package reflection

import (
	"errors"
)

func decisionForError(err error) Decision {
	switch {
	case err == nil:
		return DecisionApplied
	case isError(err, ErrInsufficientEvidence):
		return DecisionNoEvidence
	case isError(err, ErrPinnedTrait):
		return DecisionPinnedTrait
	case isError(err, ErrDeltaExceeded), isError(err, ErrOutOfRange):
		return DecisionDeltaLimit
	case isError(err, ErrOpinionLimit):
		return DecisionDeltaLimit
	case isError(err, ErrUntrustedEvidence), isError(err, ErrForbiddenMutation):
		return DecisionUntrusted
	case isError(err, ErrBudgetExceeded):
		return DecisionBudget
	default:
		return ""
	}
}

func isError(err, target error) bool {
	return errors.Is(err, target)
}
