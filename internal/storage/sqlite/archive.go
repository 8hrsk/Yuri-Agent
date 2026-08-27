package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// ArchiveSearchOptions bounds deliberate cross-session lexical search. The
// archive always contains the original transcript, including conversations
// hidden from the active UI list. Callers can explicitly exclude such sessions
// for a list-only view. Dormant memory filtering belongs to
// MemorySearchOptions, not the transcript archive.
type ArchiveSearchOptions struct {
	ConversationID  domain.ID
	IncludeArchived bool
	ExcludeArchived bool
	Limit           int
	Offset          int
	MaxTokens       int
}

// ArchiveSearchHit contains a bounded transcript hit and stable provenance.
// ConversationTitle is denormalized for list rendering only; the message
// repository remains authoritative for the full record.
type ArchiveSearchHit struct {
	Message           Message
	ConversationID    domain.ID
	ConversationTitle string
	Snippet           string
	Score             float64
}

// ArchiveRepository owns the FTS5 projection over the immutable transcript.
// A lost or stale index is recoverable through Rebuild from messages.
type ArchiveRepository struct {
	db *sql.DB
}

func NewArchiveRepository(database *sql.DB) *ArchiveRepository {
	return &ArchiveRepository{db: database}
}

// Search performs safe lexical search across all conversations. It is an
// explicit on-demand operation: no complete archive is loaded into context.
func (r *ArchiveRepository) Search(ctx context.Context, query string, options ...ArchiveSearchOptions) ([]ArchiveSearchHit, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	ftsQuery, err := safeFTSQuery(query)
	if err != nil {
		return nil, err
	}
	opts := ArchiveSearchOptions{Limit: 20}
	if len(options) > 0 {
		opts = options[0]
		if opts.Limit == 0 {
			opts.Limit = 20
		}
	}
	if opts.Limit < 0 || opts.Offset < 0 || opts.MaxTokens < 0 {
		return nil, fmt.Errorf("%w: archive search bounds cannot be negative", domain.ErrInvalidArgument)
	}
	where := []string{"messages_fts MATCH ?"}
	args := []any{ftsQuery}
	if !opts.ConversationID.Empty() {
		where = append(where, "m.conversation_id = ?")
		args = append(args, string(opts.ConversationID))
	}
	if opts.ExcludeArchived && !opts.IncludeArchived {
		where = append(where, "(c.archived_at IS NULL OR c.id IS NULL)")
	}
	querySQL := `
		SELECT m.id, m.conversation_id, m.role, m.content, m.status,
		       m.provider_meta_json, m.created_at,
		       c.title,
		       snippet(messages_fts, 3, '[', ']', '…', 18) AS hit_snippet,
		       bm25(messages_fts) AS hit_rank
		FROM messages_fts
		JOIN messages AS m ON m.id = messages_fts.message_id
		LEFT JOIN conversations AS c ON c.id = m.conversation_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY hit_rank ASC, m.created_at DESC, m.id DESC`
	if opts.Limit > 0 {
		querySQL += " LIMIT ?"
		args = append(args, opts.Limit)
	}
	if opts.Offset > 0 {
		if opts.Limit <= 0 {
			querySQL += " LIMIT -1"
		}
		querySQL += " OFFSET ?"
		args = append(args, opts.Offset)
	}
	rows, err := r.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, wrappedSQLError("search archive", err)
	}
	defer rows.Close()
	result := make([]ArchiveSearchHit, 0)
	usedTokens := 0
	for rows.Next() {
		var (
			message                        Message
			messageID, conversationID      string
			providerMeta, createdAt, title string
			snippet                        string
			rank                           float64
		)
		if err := rows.Scan(&messageID, &conversationID, &message.Role, &message.Content, &message.Status,
			&providerMeta, &createdAt, &title, &snippet, &rank); err != nil {
			return nil, wrappedSQLError("scan archive search hit", err)
		}
		message.ID = domain.ID(messageID)
		message.ConversationID = domain.ID(conversationID)
		message.ProviderMeta = providerMeta
		if message.CreatedAt, err = scanTime(createdAt); err != nil {
			return nil, err
		}
		itemTokens := approximateTokens(message.Content)
		if opts.MaxTokens > 0 && usedTokens+itemTokens > opts.MaxTokens {
			continue
		}
		result = append(result, ArchiveSearchHit{
			Message: message, ConversationID: message.ConversationID,
			ConversationTitle: title, Snippet: snippet, Score: -rank,
		})
		usedTokens += itemTokens
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate archive search", err)
	}
	return result, nil
}

// SearchMessages is a descriptive alias for generic archive consumers.
func (r *ArchiveRepository) SearchMessages(ctx context.Context, query string, options ...ArchiveSearchOptions) ([]ArchiveSearchHit, error) {
	return r.Search(ctx, query, options...)
}

// SearchLexical is the explicit name used by hybrid retrieval services.
func (r *ArchiveRepository) SearchLexical(ctx context.Context, query string, options ...ArchiveSearchOptions) ([]ArchiveSearchHit, error) {
	return r.Search(ctx, query, options...)
}

// SearchSession narrows deliberate search to one conversation while retaining
// the same bounded result contract.
func (r *ArchiveRepository) SearchSession(ctx context.Context, conversationID domain.ID, query string, options ...ArchiveSearchOptions) ([]ArchiveSearchHit, error) {
	if conversationID.Empty() {
		return nil, fmt.Errorf("%w: conversation id is required", domain.ErrInvalidArgument)
	}
	opts := ArchiveSearchOptions{ConversationID: conversationID}
	if len(options) > 0 {
		opts = options[0]
		opts.ConversationID = conversationID
	}
	return r.Search(ctx, query, opts)
}

// Window returns the original transcript around a deliberate search hit. It
// uses chronological message ordering and never substitutes an FTS snippet for
// source data.
func (r *ArchiveRepository) Window(ctx context.Context, messageID domain.ID, before, after int) ([]Message, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if messageID.Empty() || before < 0 || after < 0 {
		return nil, fmt.Errorf("%w: message id and non-negative window bounds are required", domain.ErrInvalidArgument)
	}
	var conversationID, anchorCreatedAt string
	if err := r.db.QueryRowContext(ctx, `SELECT conversation_id, created_at FROM messages WHERE id = ?`, string(messageID)).Scan(&conversationID, &anchorCreatedAt); err != nil {
		return nil, wrappedSQLError("get archive window anchor", err)
	}
	if _, err := scanTime(anchorCreatedAt); err != nil {
		return nil, err
	}
	// Fetch only the requested neighborhood. Ordering by timestamp and ID
	// makes equal-timestamp messages deterministic without loading a complete
	// conversation into memory.
	beforeMessages := make([]Message, 0, before)
	if before > 0 {
		rows, err := r.db.QueryContext(ctx, `
			SELECT id, conversation_id, role, content, status, provider_meta_json, created_at
			FROM messages
			WHERE conversation_id = ? AND (created_at < ? OR (created_at = ? AND id < ?))
			ORDER BY created_at DESC, id DESC LIMIT ?`, conversationID, anchorCreatedAt, anchorCreatedAt, string(messageID), before)
		if err != nil {
			return nil, wrappedSQLError("query archive preceding window", err)
		}
		beforeMessages, err = scanArchiveMessages(rows, "preceding archive window")
		if err != nil {
			return nil, err
		}
		for left, right := 0, len(beforeMessages)-1; left < right; left, right = left+1, right-1 {
			beforeMessages[left], beforeMessages[right] = beforeMessages[right], beforeMessages[left]
		}
	}
	afterLimit := after + 1 // include the anchor itself
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, conversation_id, role, content, status, provider_meta_json, created_at
		FROM messages
		WHERE conversation_id = ? AND (created_at > ? OR (created_at = ? AND id >= ?))
		ORDER BY created_at ASC, id ASC LIMIT ?`, conversationID, anchorCreatedAt, anchorCreatedAt, string(messageID), afterLimit)
	if err != nil {
		return nil, wrappedSQLError("query archive following window", err)
	}
	afterMessages, err := scanArchiveMessages(rows, "following archive window")
	if err != nil {
		return nil, err
	}
	if len(afterMessages) == 0 || afterMessages[0].ID != messageID {
		return nil, domain.ErrNotFound
	}
	return append(beforeMessages, afterMessages...), nil
}

func scanArchiveMessages(rows *sql.Rows, label string) ([]Message, error) {
	defer rows.Close()
	result := make([]Message, 0)
	for rows.Next() {
		var message Message
		var idValue, conversationIDValue, status, providerMeta, createdAt string
		if err := rows.Scan(&idValue, &conversationIDValue, &message.Role, &message.Content, &status, &providerMeta, &createdAt); err != nil {
			return nil, wrappedSQLError("scan "+label, err)
		}
		message.ID = domain.ID(idValue)
		message.ConversationID = domain.ID(conversationIDValue)
		message.Status = status
		message.ProviderMeta = providerMeta
		parsed, err := scanTime(createdAt)
		if err != nil {
			return nil, err
		}
		message.CreatedAt = parsed
		result = append(result, message)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate "+label, err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrappedSQLError("close "+label, err)
	}
	return result, nil
}

// Rebuild reconstructs the message FTS projection from the authoritative
// messages table. It is idempotent and safe to call during startup repair.
func (r *ArchiveRepository) Rebuild(ctx context.Context) error {
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin rebuild archive", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM messages_fts;
		INSERT INTO messages_fts(message_id, conversation_id, role, content, created_at)
		SELECT id, conversation_id, role, content, created_at FROM messages;`); err != nil {
		return wrappedSQLError("rebuild archive index", err)
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit rebuild archive", err)
	}
	return nil
}

// RebuildIndex is a descriptive alias for startup repair code.
func (r *ArchiveRepository) RebuildIndex(ctx context.Context) error {
	return r.Rebuild(ctx)
}

// IndexStats provides a small diagnostics surface without exposing FTS
// internals to application code.
func (r *ArchiveRepository) IndexStats(ctx context.Context) (messages, indexed int64, err error) {
	if err = requireDatabase(r.db); err != nil {
		return 0, 0, err
	}
	if err = contextErr(ctx); err != nil {
		return 0, 0, err
	}
	if err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&messages); err != nil {
		return 0, 0, wrappedSQLError("count messages", err)
	}
	if err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages_fts`).Scan(&indexed); err != nil {
		return 0, 0, wrappedSQLError("count archive index", err)
	}
	return messages, indexed, nil
}
