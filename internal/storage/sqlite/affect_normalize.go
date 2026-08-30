package sqlite

import (
	"fmt"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func normalizeAffectForCreate(state domain.AffectiveState) domain.AffectiveState {
	if state.Version == 0 {
		state.Version = 1
	}
	if state.Operation == "" {
		state.Operation = domain.AffectOperationCreate
	}
	if state.CreatedAt.IsZero() && !state.UpdatedAt.IsZero() {
		state.CreatedAt = state.UpdatedAt
	}
	if state.UpdatedAt.IsZero() && !state.CreatedAt.IsZero() {
		state.UpdatedAt = state.CreatedAt
	}
	if state.Emotions == nil {
		if state.Dimensions != nil {
			state.Emotions = state.Dimensions
		} else {
			state.Emotions = state.Values
		}
	}
	state.Emotions = cloneFloatMap(state.Emotions)
	if state.AsOf.IsZero() {
		state.AsOf = state.UpdatedAt
	}
	return state
}

func normalizeAffectForAppend(state, current domain.AffectiveState) domain.AffectiveState {
	state.CreatedAt = current.CreatedAt
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	if state.Operation == "" || state.Operation == domain.AffectOperationCreate {
		state.Operation = domain.AffectOperationUpdate
	}
	if state.Emotions == nil {
		state.Emotions = state.Dimensions
		if state.Emotions == nil {
			state.Emotions = state.Values
		}
	}
	state.Emotions = cloneFloatMap(state.Emotions)
	state.Dimensions = nil
	state.Values = nil
	return state
}

func cloneAffect(state domain.AffectiveState) domain.AffectiveState {
	state.Emotions = cloneFloatMap(state.Emotions)
	state.Dimensions = cloneFloatMap(state.Dimensions)
	state.Values = cloneFloatMap(state.Values)
	return state
}

func affectVersionRecord(state domain.AffectiveState) AffectiveVersionRecord {
	return AffectiveVersionRecord{State: state, RevisionID: state.RevisionID, ParentID: state.ParentID,
		ParentVersion: state.ParentVersion, Operation: state.Operation, Reason: state.Reason, AuthorRunID: state.AuthorRunID}
}

func affectMetadata(value any) (*AffectiveVersionMetadata, error) {
	switch metadata := value.(type) {
	case AffectiveVersionMetadata:
		return &metadata, nil
	case *AffectiveVersionMetadata:
		if metadata == nil {
			return nil, fmt.Errorf("%w: nil affect metadata", domain.ErrInvalidArgument)
		}
		copy := *metadata
		return &copy, nil
	default:
		return nil, fmt.Errorf("%w: unsupported affect metadata %T", domain.ErrInvalidArgument, value)
	}
}
