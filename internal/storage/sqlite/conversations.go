package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// ConversationRepository persists local conversations.
type ConversationRepository struct {
	db *sql.DB
}

func NewConversationRepository(database *sql.DB) *ConversationRepository {
	return &ConversationRepository{db: database}
}

func (r *ConversationRepository) Create(ctx context.Context, conversation Conversation) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if conversation.ID.Empty() || conversation.AgentID.Empty() || strings.TrimSpace(conversation.Title) == "" {
		return fmt.Errorf("%w: conversation id, agent id and title are required", domain.ErrInvalidArgument)
	}
	createdAt, err := timeValue(conversation.CreatedAt)
	if err != nil {
		return err
	}
	updatedAt := conversation.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = conversation.CreatedAt
	}
	updatedAtValue, err := timeValue(updatedAt)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO conversations(id, agent_id, title, created_at, updated_at, archived_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		string(conversation.ID), string(conversation.AgentID), strings.TrimSpace(conversation.Title), createdAt,
		updatedAtValue, nullableTimeValue(pointerTime(conversation.ArchivedAt)))
	return wrappedSQLError("create conversation", err)
}

func (r *ConversationRepository) Get(ctx context.Context, id domain.ID) (Conversation, error) {
	if err := requireDatabase(r.db); err != nil {
		return Conversation{}, err
	}
	if err := contextErr(ctx); err != nil {
		return Conversation{}, err
	}
	if id.Empty() {
		return Conversation{}, fmt.Errorf("%w: conversation id is required", domain.ErrInvalidArgument)
	}
	var (
		result                         Conversation
		archivedAt, createdAt, updated string
		idValue, agentIDValue          string
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT id, agent_id, title, created_at, updated_at, archived_at
		FROM conversations WHERE id = ?`, string(id)).Scan(
		&idValue, &agentIDValue, &result.Title, &createdAt, &updated, &nullableString{Value: &archivedAt})
	if err != nil {
		return Conversation{}, wrappedSQLError("get conversation", err)
	}
	result.ID = domain.ID(idValue)
	result.AgentID = domain.ID(agentIDValue)
	if result.CreatedAt, err = scanTime(createdAt); err != nil {
		return Conversation{}, err
	}
	if result.UpdatedAt, err = scanTime(updated); err != nil {
		return Conversation{}, err
	}
	if archivedAt != "" {
		parsed, parseErr := scanTime(archivedAt)
		if parseErr != nil {
			return Conversation{}, parseErr
		}
		result.ArchivedAt = &parsed
	}
	return result, nil
}

// List returns conversations ordered by most recently updated. Archived
// conversations are excluded unless includeArchived is true.
func (r *ConversationRepository) List(ctx context.Context, includeArchived ...bool) ([]Conversation, error) {
	return r.list(ctx, "", includeArchived...)
}

// ListByAgent returns only conversations owned by one named agent.
func (r *ConversationRepository) ListByAgent(ctx context.Context, agentID domain.ID, includeArchived ...bool) ([]Conversation, error) {
	if agentID.Empty() {
		return nil, fmt.Errorf("%w: agent id is required", domain.ErrInvalidArgument)
	}
	return r.list(ctx, agentID, includeArchived...)
}

func (r *ConversationRepository) list(ctx context.Context, agentID domain.ID, includeArchived ...bool) ([]Conversation, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	include := len(includeArchived) > 0 && includeArchived[0]
	query := `
		SELECT id, agent_id, title, created_at, updated_at, archived_at
		FROM conversations`
	where := make([]string, 0, 2)
	args := make([]any, 0, 1)
	if !agentID.Empty() {
		where = append(where, "agent_id = ?")
		args = append(args, string(agentID))
	}
	if !include {
		where = append(where, "archived_at IS NULL")
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY updated_at DESC, id DESC"
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list conversations", err)
	}
	defer rows.Close()
	result := make([]Conversation, 0)
	for rows.Next() {
		var (
			item                                      Conversation
			idValue, agentIDValue, createdAt, updated string
			archived                                  sql.NullString
		)
		if err := rows.Scan(&idValue, &agentIDValue, &item.Title, &createdAt, &updated, &archived); err != nil {
			return nil, wrappedSQLError("scan conversation", err)
		}
		item.ID = domain.ID(idValue)
		item.AgentID = domain.ID(agentIDValue)
		if item.CreatedAt, err = scanTime(createdAt); err != nil {
			return nil, err
		}
		if item.UpdatedAt, err = scanTime(updated); err != nil {
			return nil, err
		}
		if archived.Valid {
			parsed, parseErr := scanTime(archived.String)
			if parseErr != nil {
				return nil, parseErr
			}
			item.ArchivedAt = &parsed
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate conversations", err)
	}
	return result, nil
}

// Save updates mutable conversation metadata. Transcripts are immutable; a
// caller should use MessageRepository to append new messages.
func (r *ConversationRepository) Save(ctx context.Context, conversation Conversation) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if conversation.ID.Empty() || conversation.AgentID.Empty() || strings.TrimSpace(conversation.Title) == "" {
		return fmt.Errorf("%w: conversation id, agent id and title are required", domain.ErrInvalidArgument)
	}
	updatedAt := conversation.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	updatedAtValue, err := timeValue(updatedAt)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE conversations
		SET title = ?, updated_at = ?, archived_at = ?
		WHERE id = ?`, strings.TrimSpace(conversation.Title), updatedAtValue,
		nullableTimeValue(pointerTime(conversation.ArchivedAt)), string(conversation.ID))
	if err != nil {
		return wrappedSQLError("save conversation", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrappedSQLError("count saved conversation", err)
	}
	if count == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *ConversationRepository) Archive(ctx context.Context, id domain.ID, at time.Time) error {
	conversation, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	conversation.ArchivedAt = &at
	conversation.UpdatedAt = at
	return r.Save(ctx, conversation)
}

func (r *ConversationRepository) Unarchive(ctx context.Context, id domain.ID, at time.Time) error {
	conversation, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	conversation.ArchivedAt = nil
	conversation.UpdatedAt = at
	return r.Save(ctx, conversation)
}

func pointerTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

// nullableString is a tiny Scan helper that treats SQL NULL as an empty
// string. sql.NullString is used in list queries where retaining validity is
// useful; Get only needs the optional value.
type nullableString struct {
	Value *string
}

func (s *nullableString) Scan(value any) error {
	if value == nil {
		*s.Value = ""
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("expected string timestamp, got %T", value)
	}
	*s.Value = text
	return nil
}
