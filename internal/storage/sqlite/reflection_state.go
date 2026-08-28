package sqlite

import (
	"context"
	"fmt"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// ReflectionStateMutation is one guarded, already-projected background
// reflection write. Nil targets remain unchanged. All non-nil targets are
// optimistic-version checked before any row is inserted and committed in one
// SQLite transaction so a partial personality update is never observable.
type ReflectionStateMutation struct {
	Persona              *domain.MutablePersona
	ExpectedPersona      uint64
	Relationship         *domain.RelationshipState
	ExpectedRelationship uint64
	Affect               *domain.AffectiveState
	ExpectedAffect       uint64
	AffectEvents         []domain.AffectiveEvent
}

func (repositories *Repositories) ApplyReflectionState(ctx context.Context, mutation ReflectionStateMutation) error {
	if repositories == nil || repositories.Persona == nil || repositories.Relationship == nil || repositories.Affect == nil {
		return fmt.Errorf("%w: reflection repositories are unavailable", domain.ErrInvalidArgument)
	}
	if mutation.Persona == nil && mutation.Relationship == nil && mutation.Affect == nil {
		return fmt.Errorf("%w: reflection mutation is empty", domain.ErrInvalidArgument)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	tx, err := repositories.Persona.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin atomic reflection state", err)
	}
	defer tx.Rollback()

	var persona domain.MutablePersona
	if mutation.Persona != nil {
		current, loadErr := getPersonaVersionTx(ctx, tx, mutation.Persona.ID, 0)
		if loadErr != nil {
			return loadErr
		}
		if mutation.ExpectedPersona == 0 || current.Version != mutation.ExpectedPersona || mutation.Persona.Version != current.Version+1 {
			return domain.ErrConflict
		}
		persona = normalizePersonaForAppend(*mutation.Persona, current)
		persona.Operation = domain.PersonaOperationUpdate
		persona.ParentID = current.RevisionID
		persona.ParentVersion = current.Version
		if persona.RevisionID == current.RevisionID {
			persona.RevisionID = ""
		}
		if err := domain.ValidatePersonaEvolution(current, persona, repositories.Persona.limits); err != nil {
			return err
		}
	}

	var relationship domain.RelationshipState
	if mutation.Relationship != nil {
		current, loadErr := getRelationshipVersionTx(ctx, tx, mutation.Relationship.ID, 0)
		if loadErr != nil {
			return loadErr
		}
		if mutation.ExpectedRelationship == 0 || current.Version != mutation.ExpectedRelationship || mutation.Relationship.Version != current.Version+1 {
			return domain.ErrConflict
		}
		relationship = normalizeRelationshipForAppend(*mutation.Relationship, current)
		relationship.Operation = domain.RelationshipOperationUpdate
		relationship.ParentID = current.RevisionID
		relationship.ParentVersion = current.Version
		if relationship.RevisionID == current.RevisionID {
			relationship.RevisionID = ""
		}
		if err := domain.ValidateRelationshipEvolution(current, relationship, repositories.Relationship.maxDelta); err != nil {
			return err
		}
	}

	var affect domain.AffectiveState
	if mutation.Affect != nil {
		current, loadErr := getAffectVersionTx(ctx, tx, mutation.Affect.ID, 0)
		if loadErr != nil {
			return loadErr
		}
		if mutation.ExpectedAffect == 0 || current.Version != mutation.ExpectedAffect || mutation.Affect.Version != current.Version+1 {
			return domain.ErrConflict
		}
		affect = normalizeAffectForAppend(*mutation.Affect, current)
		affect.Operation = domain.AffectOperationUpdate
		affect.ParentID = current.RevisionID
		affect.ParentVersion = current.Version
		if affect.RevisionID == current.RevisionID {
			affect.RevisionID = ""
		}
		if err := affect.Validate(); err != nil {
			return err
		}
		for _, event := range mutation.AffectEvents {
			if err := event.Validate(); err != nil {
				return err
			}
		}
	} else if len(mutation.AffectEvents) > 0 {
		return fmt.Errorf("%w: affect events require an affect state mutation", domain.ErrInvalidArgument)
	}

	// Validation and optimistic checks above deliberately precede every write.
	if mutation.Persona != nil {
		metadata := PersonaVersionMetadata{Operation: domain.PersonaOperationUpdate, Reason: persona.Reason, Evidence: persona.Evidence, AuthorRunID: persona.AuthorRunID}
		if err := repositories.Persona.appendPersonaTx(ctx, tx, persona, mutation.ExpectedPersona, &metadata, true); err != nil {
			return err
		}
	}
	if mutation.Relationship != nil {
		metadata := RelationshipVersionMetadata{Operation: domain.RelationshipOperationUpdate, Reason: relationship.Reason, Evidence: relationship.Evidence, AuthorRunID: relationship.AuthorRunID}
		if err := repositories.Relationship.appendRelationshipTx(ctx, tx, relationship, mutation.ExpectedRelationship, &metadata); err != nil {
			return err
		}
	}
	if mutation.Affect != nil {
		metadata := AffectiveVersionMetadata{Operation: domain.AffectOperationUpdate, Reason: affect.Reason, AuthorRunID: affect.AuthorRunID}
		if err := repositories.Affect.appendAffectStateTx(ctx, tx, affect, mutation.ExpectedAffect, &metadata); err != nil {
			return err
		}
		for _, event := range mutation.AffectEvents {
			event.AffectID = affect.ID
			event.StateVersion = affect.Version
			if err := insertAffectiveEvent(ctx, tx, event); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit atomic reflection state", err)
	}
	return nil
}
