package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// AgentRepository persists owner-created top-level agent identities. It does
// not store mutable persona state and is never used for anonymous subagents.
type AgentRepository struct {
	db *sql.DB
}

func NewAgentRepository(database *sql.DB) *AgentRepository {
	return &AgentRepository{db: database}
}

func (r *AgentRepository) Create(ctx context.Context, profile domain.AgentProfile) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := profile.Validate(); err != nil {
		return err
	}
	createdAt, err := timeValue(profile.CreatedAt)
	if err != nil {
		return err
	}
	updatedAt, err := timeValue(profile.UpdatedAt)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO agent_profiles(id, name, age, gender, preferences, backstory, provider_id, model,
			fallback_enabled, fallback_provider_id, fallback_model, execution_budget, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, string(profile.ID), strings.TrimSpace(profile.Name), profile.Age,
		strings.TrimSpace(profile.Gender), strings.TrimSpace(profile.Preferences), strings.TrimSpace(profile.Backstory),
		strings.TrimSpace(profile.ProviderID), strings.TrimSpace(profile.Model), profile.FallbackEnabled,
		strings.TrimSpace(profile.FallbackProviderID), strings.TrimSpace(profile.FallbackModel), profile.ExecutionBudget.Normalized(), createdAt, updatedAt)
	return wrappedSQLError("create agent profile", err)
}

// CreateAgentWithDefaults creates an owner-controlled profile and its initial
// mutable personality projections as one transaction. The projections must
// all belong to the same profile; this prevents a roster entry from becoming
// active with only a partially initialized personality.
func (repositories *Repositories) CreateAgentWithDefaults(ctx context.Context, profile domain.AgentProfile, persona domain.MutablePersona, relationship domain.RelationshipState, affect domain.AffectiveState) error {
	return repositories.createAgentWithDefaults(ctx, profile, persona, relationship, affect, nil)
}

// CreateAgentWithPersonalizationDefaults additionally persists the immutable
// owner-authored v2 seed in the same transaction as all mutable projections.
// The legacy method remains available to storage callers that intentionally
// exercise the older aggregate boundary.
func (repositories *Repositories) CreateAgentWithPersonalizationDefaults(ctx context.Context, profile domain.AgentProfile, persona domain.MutablePersona, relationship domain.RelationshipState, affect domain.AffectiveState, personalization domain.PersonalizationSeed) error {
	return repositories.createAgentWithDefaults(ctx, profile, persona, relationship, affect, &personalization)
}

func (repositories *Repositories) createAgentWithDefaults(ctx context.Context, profile domain.AgentProfile, persona domain.MutablePersona, relationship domain.RelationshipState, affect domain.AffectiveState, personalization *domain.PersonalizationSeed) error {
	if repositories == nil || repositories.Agents == nil || repositories.Persona == nil || repositories.Relationship == nil || repositories.Affect == nil || (personalization != nil && repositories.Personalization == nil) {
		return fmt.Errorf("%w: agent repositories are unavailable", domain.ErrInvalidArgument)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := profile.Validate(); err != nil {
		return err
	}
	if persona.ID != profile.ID || relationship.ID != profile.ID || affect.ID != profile.ID {
		return fmt.Errorf("%w: agent defaults must share profile id", domain.ErrInvalidArgument)
	}
	persona = normalizePersonaForCreate(persona)
	relationship = normalizeRelationshipForCreate(relationship)
	affect = normalizeAffectForCreate(affect)
	if err := persona.ValidateWithLimits(repositories.Persona.limits); err != nil {
		return err
	}
	if err := relationship.Validate(); err != nil {
		return err
	}
	if err := affect.Validate(); err != nil {
		return err
	}
	if personalization != nil {
		if personalization.AgentID != profile.ID || personalization.Version != 1 {
			return fmt.Errorf("%w: agent personalization seed must be its initial profile revision", domain.ErrInvalidArgument)
		}
		if err := personalization.Validate(); err != nil {
			return err
		}
		expectedRelationship, err := domain.NewOwnerRelationshipState(*personalization, relationship.CreatedAt)
		if err != nil {
			return err
		}
		if !maps.Equal(relationship.Dimensions, expectedRelationship.Dimensions) || strings.TrimSpace(relationship.Summary) != expectedRelationship.Summary {
			return fmt.Errorf("%w: initial owner relationship must project its personalization seed", domain.ErrInvalidArgument)
		}
	}
	tx, err := repositories.Agents.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin create agent", err)
	}
	defer tx.Rollback()
	var existing string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM agent_profiles WHERE id = ?`, string(profile.ID)).Scan(&existing); err == nil {
		return domain.ErrConflict
	} else if !isNoRows(err) {
		return wrappedSQLError("check agent profile", err)
	}
	createdAt, err := timeValue(profile.CreatedAt)
	if err != nil {
		return err
	}
	updatedAt, err := timeValue(profile.UpdatedAt)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_profiles(id, name, age, gender, preferences, backstory, provider_id, model,
			fallback_enabled, fallback_provider_id, fallback_model, execution_budget, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, string(profile.ID), strings.TrimSpace(profile.Name), profile.Age,
		strings.TrimSpace(profile.Gender), strings.TrimSpace(profile.Preferences), strings.TrimSpace(profile.Backstory),
		strings.TrimSpace(profile.ProviderID), strings.TrimSpace(profile.Model), profile.FallbackEnabled,
		strings.TrimSpace(profile.FallbackProviderID), strings.TrimSpace(profile.FallbackModel), profile.ExecutionBudget.Normalized(), createdAt, updatedAt); err != nil {
		return wrappedSQLError("create agent profile", err)
	}
	if err := repositories.Persona.appendPersonaTx(ctx, tx, persona, 0, nil, false); err != nil {
		return err
	}
	if err := repositories.Relationship.appendRelationshipTx(ctx, tx, relationship, 0, nil); err != nil {
		return err
	}
	if err := repositories.Affect.appendAffectStateTx(ctx, tx, affect, 0, nil); err != nil {
		return err
	}
	if personalization != nil {
		if err := repositories.Personalization.appendTx(ctx, tx, *personalization); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit create agent", err)
	}
	return nil
}

func (r *AgentRepository) Get(ctx context.Context, id domain.ID) (domain.AgentProfile, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.AgentProfile{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.AgentProfile{}, err
	}
	if id.Empty() {
		return domain.AgentProfile{}, fmt.Errorf("%w: agent profile id is required", domain.ErrInvalidArgument)
	}
	return scanAgentProfile(r.db.QueryRowContext(ctx, `
		SELECT id, name, age, gender, preferences, backstory, provider_id, model,
		       fallback_enabled, fallback_provider_id, fallback_model, execution_budget, created_at, updated_at
		FROM agent_profiles WHERE id = ?`, string(id)))
}

func (r *AgentRepository) List(ctx context.Context) ([]domain.AgentProfile, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, age, gender, preferences, backstory, provider_id, model,
		       fallback_enabled, fallback_provider_id, fallback_model, execution_budget, created_at, updated_at
		FROM agent_profiles ORDER BY created_at, id`)
	if err != nil {
		return nil, wrappedSQLError("list agent profiles", err)
	}
	defer rows.Close()
	profiles := make([]domain.AgentProfile, 0)
	for rows.Next() {
		profile, scanErr := scanAgentProfile(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate agent profiles", err)
	}
	return profiles, nil
}

type agentProfileScanner interface {
	Scan(dest ...any) error
}

func scanAgentProfile(scanner agentProfileScanner) (domain.AgentProfile, error) {
	var profile domain.AgentProfile
	var id, createdAt, updatedAt string
	if err := scanner.Scan(&id, &profile.Name, &profile.Age, &profile.Gender, &profile.Preferences, &profile.Backstory,
		&profile.ProviderID, &profile.Model, &profile.FallbackEnabled, &profile.FallbackProviderID, &profile.FallbackModel,
		&profile.ExecutionBudget, &createdAt, &updatedAt); err != nil {
		return domain.AgentProfile{}, wrappedSQLError("scan agent profile", err)
	}
	profile.ID = domain.ID(id)
	var err error
	if profile.CreatedAt, err = scanTime(createdAt); err != nil {
		return domain.AgentProfile{}, err
	}
	if profile.UpdatedAt, err = scanTime(updatedAt); err != nil {
		return domain.AgentProfile{}, err
	}
	if err := profile.Validate(); err != nil {
		return domain.AgentProfile{}, err
	}
	return profile, nil
}

// UpdateExecutionBudget changes only the owner-controlled resource preset.
// Existing runs retain their already-resolved durable budgets.
func (r *AgentRepository) UpdateExecutionBudget(ctx context.Context, id domain.ID, preset domain.ExecutionBudgetPreset, updatedAt time.Time) (domain.AgentProfile, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.AgentProfile{}, err
	}
	if preset == "" || !preset.Valid() {
		return domain.AgentProfile{}, fmt.Errorf("%w: execution budget preset is required", domain.ErrInvalidArgument)
	}
	profile, err := r.Get(ctx, id)
	if err != nil {
		return domain.AgentProfile{}, err
	}
	profile.ExecutionBudget = preset
	profile.UpdatedAt = updatedAt.UTC()
	if !profile.UpdatedAt.After(profile.CreatedAt) {
		profile.UpdatedAt = profile.CreatedAt.Add(time.Nanosecond)
	}
	if err := profile.Validate(); err != nil {
		return domain.AgentProfile{}, err
	}
	encodedTime, err := timeValue(profile.UpdatedAt)
	if err != nil {
		return domain.AgentProfile{}, err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE agent_profiles SET execution_budget = ?, updated_at = ? WHERE id = ?`, preset, encodedTime, string(id))
	if err != nil {
		return domain.AgentProfile{}, wrappedSQLError("update agent execution budget", err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
		return domain.AgentProfile{}, affectedErr
	} else if affected != 1 {
		return domain.AgentProfile{}, domain.ErrNotFound
	}
	return profile, nil
}

// UpdateModelRoute changes only the non-secret provider/model preference of an
// existing named agent. Personality and memory revisions are independent.
func (r *AgentRepository) UpdateModelRoute(ctx context.Context, id domain.ID, providerID, model string, updatedAt time.Time) (domain.AgentProfile, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.AgentProfile{}, err
	}
	profile, err := r.Get(ctx, id)
	if err != nil {
		return domain.AgentProfile{}, err
	}
	profile.ProviderID = strings.TrimSpace(providerID)
	profile.Model = strings.TrimSpace(model)
	profile.UpdatedAt = updatedAt.UTC()
	if !profile.UpdatedAt.After(profile.CreatedAt) {
		profile.UpdatedAt = profile.CreatedAt.Add(time.Nanosecond)
	}
	if err := profile.Validate(); err != nil {
		return domain.AgentProfile{}, err
	}
	encodedTime, err := timeValue(profile.UpdatedAt)
	if err != nil {
		return domain.AgentProfile{}, err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE agent_profiles SET provider_id = ?, model = ?, updated_at = ? WHERE id = ?`, profile.ProviderID, profile.Model, encodedTime, string(id))
	if err != nil {
		return domain.AgentProfile{}, wrappedSQLError("update agent model route", err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
		return domain.AgentProfile{}, affectedErr
	} else if affected != 1 {
		return domain.AgentProfile{}, domain.ErrNotFound
	}
	return profile, nil
}

// UpdateFallbackRoute changes the owner-controlled fallback route. The route
// is persisted independently of the primary route; an enabled fallback must
// contain both provider and model, and remains inert until the orchestrator
// explicitly selects it after a safe pre-output failure.
func (r *AgentRepository) UpdateFallbackRoute(ctx context.Context, id domain.ID, enabled bool, providerID, model string, updatedAt time.Time) (domain.AgentProfile, error) {
	if err := requireDatabase(r.db); err != nil {
		return domain.AgentProfile{}, err
	}
	profile, err := r.Get(ctx, id)
	if err != nil {
		return domain.AgentProfile{}, err
	}
	profile.FallbackEnabled = enabled
	profile.FallbackProviderID = strings.TrimSpace(providerID)
	profile.FallbackModel = strings.TrimSpace(model)
	profile.UpdatedAt = updatedAt.UTC()
	if !profile.UpdatedAt.After(profile.CreatedAt) {
		profile.UpdatedAt = profile.CreatedAt.Add(time.Nanosecond)
	}
	if err := profile.Validate(); err != nil {
		return domain.AgentProfile{}, err
	}
	encodedTime, err := timeValue(profile.UpdatedAt)
	if err != nil {
		return domain.AgentProfile{}, err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE agent_profiles
		SET fallback_enabled = ?, fallback_provider_id = ?, fallback_model = ?, updated_at = ?
		WHERE id = ?`, profile.FallbackEnabled, profile.FallbackProviderID, profile.FallbackModel, encodedTime, string(id))
	if err != nil {
		return domain.AgentProfile{}, wrappedSQLError("update agent fallback route", err)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
		return domain.AgentProfile{}, affectedErr
	} else if affected != 1 {
		return domain.AgentProfile{}, domain.ErrNotFound
	}
	return profile, nil
}
