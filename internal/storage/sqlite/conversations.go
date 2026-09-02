package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

const (
	// ConversationTitleSource values are persisted in conversations.title_source
	// and intentionally kept small so policy can make a single atomic decision
	// about whether an automatic title may still replace the placeholder.
	ConversationTitleSourceDefault   = "default"
	ConversationTitleSourceGenerated = "generated"
	ConversationTitleSourceUser      = "user"
	// DefaultConversationTitle is the neutral placeholder shown until the first
	// successful interactive turn gives the title worker something to summarize.
	DefaultConversationTitle  = "Новый диалог"
	maxConversationTitleRunes = 80
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
	title := normalizeConversationTitle(conversation.Title)
	if conversation.ID.Empty() || conversation.AgentID.Empty() || title == "" {
		return fmt.Errorf("%w: conversation id, agent id and title are required", domain.ErrInvalidArgument)
	}
	titleSource, err := normalizeConversationTitleSource(title, conversation.TitleSource)
	if err != nil {
		return err
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
		INSERT INTO conversations(id, agent_id, title, title_source, created_at, updated_at, archived_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(conversation.ID), string(conversation.AgentID), title, titleSource, createdAt,
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
	return scanConversation(r.db.QueryRowContext(ctx, conversationSelect+` WHERE id = ?`, string(id)))
}

const conversationSelect = `
	SELECT id, agent_id, title, title_source, created_at, updated_at, archived_at
	FROM conversations`

func scanConversation(row rowScanner) (Conversation, error) {
	var (
		result                Conversation
		createdAt, updated    string
		idValue, agentIDValue string
		titleSource           string
		archived              sql.NullString
	)
	if err := row.Scan(&idValue, &agentIDValue, &result.Title, &titleSource, &createdAt, &updated, &archived); err != nil {
		return Conversation{}, wrappedSQLError("get conversation", err)
	}
	result.ID = domain.ID(idValue)
	result.AgentID = domain.ID(agentIDValue)
	result.TitleSource = titleSource
	var err error
	if result.CreatedAt, err = scanTime(createdAt); err != nil {
		return Conversation{}, err
	}
	if result.UpdatedAt, err = scanTime(updated); err != nil {
		return Conversation{}, err
	}
	if archived.Valid {
		parsed, parseErr := scanTime(archived.String)
		if parseErr != nil {
			return Conversation{}, parseErr
		}
		result.ArchivedAt = &parsed
	}
	return result, nil
}

// ConversationListOptions bounds a conversation listing. A zero Limit is
// replaced by defaultListLimit: the desktop bridge expands every returned
// conversation into its messages, runs and tool calls, so an unbounded listing
// is the head of a much larger read.
type ConversationListOptions struct {
	AgentID         domain.ID
	IncludeArchived bool
	Limit           int
	Offset          int
}

// List returns conversations ordered by most recently updated. Archived
// conversations are excluded unless includeArchived is true. The result is
// capped at defaultListLimit; use ListPage to page beyond that.
func (r *ConversationRepository) List(ctx context.Context, includeArchived ...bool) ([]Conversation, error) {
	return r.ListPage(ctx, ConversationListOptions{IncludeArchived: len(includeArchived) > 0 && includeArchived[0]})
}

// ListByAgent returns only conversations owned by one named agent, capped like
// List.
func (r *ConversationRepository) ListByAgent(ctx context.Context, agentID domain.ID, includeArchived ...bool) ([]Conversation, error) {
	if agentID.Empty() {
		return nil, fmt.Errorf("%w: agent id is required", domain.ErrInvalidArgument)
	}
	return r.ListPage(ctx, ConversationListOptions{
		AgentID:         agentID,
		IncludeArchived: len(includeArchived) > 0 && includeArchived[0],
	})
}

// ListPage is the paging form of List. Ordering is identical, so a caller can
// walk the whole set with a stable offset.
func (r *ConversationRepository) ListPage(ctx context.Context, options ConversationListOptions) ([]Conversation, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	limit, offset, err := boundedListWindow("conversation", []int{options.Limit, options.Offset})
	if err != nil {
		return nil, err
	}
	query := conversationSelect
	where := make([]string, 0, 2)
	args := make([]any, 0, 3)
	if !options.AgentID.Empty() {
		where = append(where, "agent_id = ?")
		args = append(args, string(options.AgentID))
	}
	if !options.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY updated_at DESC, id DESC"
	query, args = appendWindow(query, args, limit, offset)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list conversations", err)
	}
	defer rows.Close()
	result := make([]Conversation, 0)
	for rows.Next() {
		item, scanErr := scanConversation(rows)
		if scanErr != nil {
			return nil, scanErr
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
	title := normalizeConversationTitle(conversation.Title)
	if conversation.ID.Empty() || conversation.AgentID.Empty() || title == "" {
		return fmt.Errorf("%w: conversation id, agent id and title are required", domain.ErrInvalidArgument)
	}
	titleSource, err := normalizeConversationTitleSource(title, conversation.TitleSource)
	if err != nil {
		return err
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
		SET title = ?, title_source = ?, updated_at = ?, archived_at = ?
		WHERE id = ?`, title, titleSource, updatedAtValue,
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

// Delete hides one owner-scoped transcript from the active conversation list.
// The name is kept for desktop API compatibility, but this is deliberately a
// soft delete: messages, runs, attachments and memory provenance remain durable.
func (r *ConversationRepository) Delete(ctx context.Context, id, agentID domain.ID) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if id.Empty() || agentID.Empty() {
		return fmt.Errorf("%w: conversation id and agent id are required", domain.ErrInvalidArgument)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin delete conversation", err)
	}
	defer tx.Rollback()
	var live int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM agent_runs
			WHERE conversation_id = ? AND state NOT IN ('completed', 'failed', 'cancelled')
		)`, string(id)).Scan(&live); err != nil {
		return wrappedSQLError("check conversation runs before delete", err)
	}
	if live == 1 {
		return fmt.Errorf("%w: finish or cancel the active conversation run before deletion", domain.ErrConflict)
	}
	archivedAt, err := timeValue(time.Now().UTC())
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE conversations
		SET archived_at = ?, updated_at = ?
		WHERE id = ? AND agent_id = ? AND archived_at IS NULL`,
		archivedAt, archivedAt, string(id), string(agentID))
	if err != nil {
		return wrappedSQLError("delete conversation", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return wrappedSQLError("count deleted conversation", err)
	}
	if changed != 1 {
		return domain.ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit delete conversation", err)
	}
	return nil
}

// UpdateTitleIfDefault atomically replaces the placeholder title for the
// named agent. The compare-and-set predicate is the boundary between the
// asynchronous title worker and an owner rename: once either wins, a later
// worker cannot overwrite the result.
func (r *ConversationRepository) UpdateTitleIfDefault(ctx context.Context, id, agentID domain.ID, title string, at time.Time) (bool, error) {
	if err := requireDatabase(r.db); err != nil {
		return false, err
	}
	if err := contextErr(ctx); err != nil {
		return false, err
	}
	if id.Empty() || agentID.Empty() {
		return false, fmt.Errorf("%w: conversation id and agent id are required", domain.ErrInvalidArgument)
	}
	title = normalizeConversationTitle(title)
	if title == "" {
		return false, fmt.Errorf("%w: conversation title is required", domain.ErrInvalidArgument)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	updatedAt, err := timeValue(at)
	if err != nil {
		return false, err
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE conversations
		SET title = ?, title_source = ?, updated_at = ?
		WHERE id = ? AND agent_id = ? AND title_source = ?`,
		title, ConversationTitleSourceGenerated, updatedAt, string(id), string(agentID), ConversationTitleSourceDefault)
	if err != nil {
		return false, wrappedSQLError("generate conversation title", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, wrappedSQLError("count generated conversation title", err)
	}
	return changed == 1, nil
}

// Rename marks a title as owner-authored in one scoped write. It deliberately
// does not reuse Save: a read/modify/write sequence would allow an in-flight
// title worker to win between the read and the write.
func (r *ConversationRepository) Rename(ctx context.Context, id, agentID domain.ID, title string, at time.Time) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if id.Empty() || agentID.Empty() {
		return fmt.Errorf("%w: conversation id and agent id are required", domain.ErrInvalidArgument)
	}
	title = normalizeConversationTitle(title)
	if title == "" {
		return fmt.Errorf("%w: conversation title is required", domain.ErrInvalidArgument)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	updatedAt, err := timeValue(at)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE conversations
		SET title = ?, title_source = ?, updated_at = ?
		WHERE id = ? AND agent_id = ?`,
		title, ConversationTitleSourceUser, updatedAt, string(id), string(agentID))
	if err != nil {
		return wrappedSQLError("rename conversation", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return wrappedSQLError("count renamed conversation", err)
	}
	if changed == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func normalizeConversationTitle(value string) string {
	title := strings.TrimSpace(value)
	if utf8.RuneCountInString(title) <= maxConversationTitleRunes {
		return title
	}
	runes := []rune(title)
	// Keep the persisted value within the schema/application bound while still
	// signalling that a long owner-provided title was shortened.
	return string(runes[:maxConversationTitleRunes-1]) + "…"
}

func normalizeConversationTitleSource(title, source string) (string, error) {
	switch strings.TrimSpace(source) {
	case "":
		// Legacy callers supplied only a title. A custom title is necessarily an
		// owner choice; the neutral placeholder remains eligible for generation.
		if title == DefaultConversationTitle {
			return ConversationTitleSourceDefault, nil
		}
		return ConversationTitleSourceUser, nil
	case ConversationTitleSourceDefault, ConversationTitleSourceGenerated, ConversationTitleSourceUser:
		return strings.TrimSpace(source), nil
	default:
		return "", fmt.Errorf("%w: unsupported conversation title source %q", domain.ErrInvalidArgument, source)
	}
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
