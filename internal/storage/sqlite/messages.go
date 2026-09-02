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
	return scanMessage(r.db.QueryRowContext(ctx, messageSelect+` WHERE id = ?`, string(id)))
}

const messageColumns = `id, conversation_id, role, content, status, provider_meta_json, created_at`

const messageSelect = `
	SELECT ` + messageColumns + `
	FROM messages`

// ListByConversation returns transcript entries in stable chronological order.
// The optional window is (limit, offset) and describes a tail of the
// transcript: the limit selects the most recent N entries and the offset skips
// that many entries back from the newest, so a caller that wants the live
// context never has to read the whole conversation to find its end. A caller
// that supplies no limit gets the newest defaultListLimit entries rather than
// every message with its full content.
func (r *MessageRepository) ListByConversation(ctx context.Context, conversationID domain.ID, window ...int) ([]Message, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if conversationID.Empty() {
		return nil, fmt.Errorf("%w: conversation id is required", domain.ErrInvalidArgument)
	}
	limit, offset, err := boundedListWindow("message", window)
	if err != nil {
		return nil, err
	}
	tail := messageSelect + ` WHERE conversation_id = ?
		ORDER BY created_at DESC, id DESC`
	args := []any{string(conversationID)}
	tail, args = appendWindow(tail, args, limit, offset)
	query := `SELECT ` + messageColumns + `
		FROM (` + tail + `) ORDER BY created_at ASC, id ASC`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list messages", err)
	}
	defer rows.Close()
	result := make([]Message, 0)
	for rows.Next() {
		message, scanErr := scanMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, message)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate messages", err)
	}
	return result, nil
}

// List is a concise alias useful to generic transcript consumers.
func (r *MessageRepository) List(ctx context.Context, conversationID domain.ID, window ...int) ([]Message, error) {
	return r.ListByConversation(ctx, conversationID, window...)
}

func (r *MessageRepository) AttachmentMetadataByConversation(ctx context.Context, conversationID domain.ID) ([]string, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if conversationID.Empty() {
		return nil, fmt.Errorf("%w: conversation id is required", domain.ErrInvalidArgument)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT provider_meta_json FROM messages
		WHERE conversation_id = ? AND json_array_length(provider_meta_json, '$.attachments') > 0`, string(conversationID))
	if err != nil {
		return nil, wrappedSQLError("list conversation attachment metadata", err)
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, wrappedSQLError("scan conversation attachment metadata", err)
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (r *MessageRepository) AttachmentBlobReferenced(ctx context.Context, blobKey string) (bool, error) {
	if err := requireDatabase(r.db); err != nil {
		return false, err
	}
	if strings.TrimSpace(blobKey) == "" {
		return false, fmt.Errorf("%w: attachment blob key is required", domain.ErrInvalidArgument)
	}
	var exists int
	err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM messages AS m, json_each(m.provider_meta_json, '$.attachments') AS attachment
			WHERE json_extract(attachment.value, '$.blob_key') = ?
		)`, blobKey).Scan(&exists)
	if err != nil {
		return false, wrappedSQLError("check attachment blob reference", err)
	}
	return exists == 1, nil
}

func scanMessage(row rowScanner) (Message, error) {
	var (
		message                         Message
		idValue, rowConversationID      string
		createdAt, providerMeta, status string
	)
	if err := row.Scan(&idValue, &rowConversationID, &message.Role, &message.Content, &status, &providerMeta, &createdAt); err != nil {
		return Message{}, wrappedSQLError("scan message", err)
	}
	message.ID = domain.ID(idValue)
	message.ConversationID = domain.ID(rowConversationID)
	message.Status = status
	message.ProviderMeta = providerMeta
	var err error
	if message.CreatedAt, err = scanTime(createdAt); err != nil {
		return Message{}, err
	}
	return message, nil
}

// ListSince reads messages at or after the supplied timestamp. It is useful
// for handoff and context-flush jobs without changing the original transcript.
func (r *MessageRepository) ListSince(ctx context.Context, conversationID domain.ID, since time.Time, window ...int) ([]Message, error) {
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
	limit, offset, err := listWindow("message", window)
	if err != nil {
		return nil, err
	}
	query := messageSelect + ` WHERE conversation_id = ? AND created_at >= ?
		ORDER BY created_at ASC, id ASC`
	args := []any{string(conversationID), formatTime(since)}
	query, args = appendWindow(query, args, limit, offset)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list messages since timestamp", err)
	}
	defer rows.Close()
	result := make([]Message, 0)
	for rows.Next() {
		message, scanErr := scanMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, message)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate messages since timestamp", err)
	}
	return result, nil
}
