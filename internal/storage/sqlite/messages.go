package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// MessageRepository appends and reads immutable conversation messages.
type MessageRepository struct {
	db *sql.DB
}

func NewMessageRepository(database *sql.DB) *MessageRepository {
	return &MessageRepository{db: database}
}

func (r *MessageRepository) Create(ctx context.Context, message Message) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if message.ID.Empty() || message.ConversationID.Empty() {
		return fmt.Errorf("%w: message and conversation ids are required", domain.ErrInvalidArgument)
	}
	if strings.TrimSpace(message.Role) == "" || strings.TrimSpace(message.Status) == "" {
		return fmt.Errorf("%w: message role and status are required", domain.ErrInvalidArgument)
	}
	createdAt, err := timeValue(message.CreatedAt)
	if err != nil {
		return err
	}
	providerMeta := strings.TrimSpace(message.ProviderMeta)
	if providerMeta == "" {
		providerMeta = "{}"
	}
	if err := validJSON(providerMeta, "provider_meta"); err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO messages(id, conversation_id, role, content, status, provider_meta_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(message.ID), string(message.ConversationID), strings.TrimSpace(message.Role),
		message.Content, strings.TrimSpace(message.Status), providerMeta, createdAt)
	return wrappedSQLError("create message", err)
}

func (r *MessageRepository) Get(ctx context.Context, id domain.ID) (Message, error) {
	if err := requireDatabase(r.db); err != nil {
		return Message{}, err
	}
	if err := contextErr(ctx); err != nil {
		return Message{}, err
	}
	if id.Empty() {
		return Message{}, fmt.Errorf("%w: message id is required", domain.ErrInvalidArgument)
	}
	var (
		message                         Message
		idValue, conversationID         string
		createdAt, providerMeta, status string
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT id, conversation_id, role, content, status, provider_meta_json, created_at
		FROM messages WHERE id = ?`, string(id)).Scan(
		&idValue, &conversationID, &message.Role, &message.Content, &status, &providerMeta, &createdAt)
	if err != nil {
		return Message{}, wrappedSQLError("get message", err)
	}
	message.ID = domain.ID(idValue)
	message.ConversationID = domain.ID(conversationID)
	message.Status = status
	message.ProviderMeta = providerMeta
	if message.CreatedAt, err = scanTime(createdAt); err != nil {
		return Message{}, err
	}
	return message, nil
}

// ListByConversation returns transcript entries in stable chronological
// order. A positive optional limit bounds the number of rows returned; zero or
// an omitted limit means no application-level limit.
func (r *MessageRepository) ListByConversation(ctx context.Context, conversationID domain.ID, limit ...int) ([]Message, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if conversationID.Empty() {
		return nil, fmt.Errorf("%w: conversation id is required", domain.ErrInvalidArgument)
	}
	query := `
		SELECT id, conversation_id, role, content, status, provider_meta_json, created_at
		FROM messages WHERE conversation_id = ?
		ORDER BY created_at ASC, id ASC`
	args := []any{string(conversationID)}
	if len(limit) > 0 && limit[0] > 0 {
		query += " LIMIT ?"
		args = append(args, limit[0])
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list messages", err)
	}
	defer rows.Close()
	result := make([]Message, 0)
	for rows.Next() {
		var (
			message                         Message
			idValue, rowConversationID      string
			createdAt, providerMeta, status string
		)
		if err := rows.Scan(&idValue, &rowConversationID, &message.Role, &message.Content, &status, &providerMeta, &createdAt); err != nil {
			return nil, wrappedSQLError("scan message", err)
		}
		message.ID = domain.ID(idValue)
		message.ConversationID = domain.ID(rowConversationID)
		message.Status = status
		message.ProviderMeta = providerMeta
		if message.CreatedAt, err = scanTime(createdAt); err != nil {
			return nil, err
		}
		result = append(result, message)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate messages", err)
	}
	return result, nil
}

// List is a concise alias useful to generic transcript consumers.
func (r *MessageRepository) List(ctx context.Context, conversationID domain.ID, limit ...int) ([]Message, error) {
	return r.ListByConversation(ctx, conversationID, limit...)
}

// ListSince reads messages at or after the supplied timestamp. It is useful
// for handoff and context-flush jobs without changing the original transcript.
func (r *MessageRepository) ListSince(ctx context.Context, conversationID domain.ID, since time.Time, limit ...int) ([]Message, error) {
	if since.IsZero() {
		return nil, fmt.Errorf("%w: since timestamp is required", domain.ErrInvalidArgument)
	}
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if conversationID.Empty() {
		return nil, fmt.Errorf("%w: conversation id is required", domain.ErrInvalidArgument)
	}
	query := `
		SELECT id, conversation_id, role, content, status, provider_meta_json, created_at
		FROM messages WHERE conversation_id = ? AND created_at >= ?
		ORDER BY created_at ASC, id ASC`
	args := []any{string(conversationID), since.UTC().Format(time.RFC3339Nano)}
	if len(limit) > 0 && limit[0] > 0 {
		query += " LIMIT ?"
		args = append(args, limit[0])
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list messages since timestamp", err)
	}
	defer rows.Close()
	result := make([]Message, 0)
	for rows.Next() {
		var (
			message                         Message
			idValue, rowConversationID      string
			createdAt, providerMeta, status string
		)
		if err := rows.Scan(&idValue, &rowConversationID, &message.Role, &message.Content, &status, &providerMeta, &createdAt); err != nil {
			return nil, wrappedSQLError("scan message since timestamp", err)
		}
		message.ID = domain.ID(idValue)
		message.ConversationID = domain.ID(rowConversationID)
		message.Status = status
		message.ProviderMeta = providerMeta
		if message.CreatedAt, err = scanTime(createdAt); err != nil {
			return nil, err
		}
		result = append(result, message)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate messages since timestamp", err)
	}
	return result, nil
}
