package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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
		INSERT INTO agent_profiles(id, name, age, gender, preferences, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, string(profile.ID), strings.TrimSpace(profile.Name), profile.Age,
		strings.TrimSpace(profile.Gender), strings.TrimSpace(profile.Preferences), createdAt, updatedAt)
	return wrappedSQLError("create agent profile", err)
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
		SELECT id, name, age, gender, preferences, created_at, updated_at
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
		SELECT id, name, age, gender, preferences, created_at, updated_at
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
	if err := scanner.Scan(&id, &profile.Name, &profile.Age, &profile.Gender, &profile.Preferences, &createdAt, &updatedAt); err != nil {
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
