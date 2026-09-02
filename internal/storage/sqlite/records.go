package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// Conversation is the durable transcript container.  The transcript remains
// authoritative even when a later context snapshot summarizes it.
type Conversation struct {
	ID      domain.ID `json:"id"`
	AgentID domain.ID `json:"agent_id"`
	Title   string    `json:"title"`
	// TitleSource records who owns the current title. The repository treats an
	// empty value as a legacy row and normalizes it before writing. A generated
	// title is replaceable only while the source is "default"; an owner rename
	// is an explicit "user" source and is therefore never overwritten by the
	// asynchronous title worker.
	TitleSource string     `json:"title_source"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
}

// Message is an immutable transcript entry. ProviderMeta is redacted provider
// metadata, not a place for credentials or the full provider response.
type Message struct {
	ID             domain.ID `json:"id"`
	ConversationID domain.ID `json:"conversation_id"`
	Role           string    `json:"role"`
	Content        string    `json:"content"`
	Status         string    `json:"status"`
	ProviderMeta   string    `json:"provider_meta,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// ToolCall is the durable, redacted record of a tool intent and its outcome.
// ArgsRedacted and ResultRef must never contain credentials or raw sensitive
// content. Version is used for optimistic updates by the execution runtime.
type ToolCall struct {
	ID             domain.ID        `json:"id"`
	RunID          domain.ID        `json:"run_id"`
	ToolID         string           `json:"tool_id"`
	ArgsRedacted   string           `json:"args_redacted"`
	Risk           domain.RiskLevel `json:"risk"`
	ApprovalID     domain.ID        `json:"approval_id,omitempty"`
	Status         string           `json:"status"`
	ResultRef      string           `json:"result_ref,omitempty"`
	IdempotencyKey string           `json:"idempotency_key,omitempty"`
	Version        uint64           `json:"version"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// AuditEvent is append-only metadata for decisions and side effects. Payload
// is expected to be redacted before it reaches this adapter.
type AuditEvent struct {
	ID              domain.ID                 `json:"id"`
	RunID           domain.ID                 `json:"run_id,omitempty"`
	ToolCallID      domain.ID                 `json:"tool_call_id,omitempty"`
	ApprovalID      domain.ID                 `json:"approval_id,omitempty"`
	Actor           domain.Actor              `json:"actor"`
	Action          string                    `json:"action"`
	Target          string                    `json:"target,omitempty"`
	Decision        domain.PermissionDecision `json:"decision,omitempty"`
	PayloadRedacted string                    `json:"payload_redacted,omitempty"`
	Duration        time.Duration             `json:"duration"`
	CreatedAt       time.Time                 `json:"created_at"`
}

// Repositories groups all SQLite adapters. Each field is safe to pass
// to application services that depend on the corresponding domain port.
type Repositories struct {
	Agents          *AgentRepository
	Personalization *PersonalizationRepository
	Conversations   *ConversationRepository
	Messages        *MessageRepository
	Memories        *MemoryRepository
	Archive         *ArchiveRepository
	Runs            *RunRepository
	Delegations     *DelegationRepository
	PeerDialogues   *PeerDialogueRepository
	// PeerDialogueMessages is kept separate from the dialogue aggregate so
	// callers can read bounded turns without gaining an unscoped write path.
	PeerDialogueMessages *PeerDialogueMessageRepository
	Approvals            *ApprovalRepository
	ToolCalls            *ToolCallRepository
	Audit                *AuditRepository
	Plugins              *PluginRepository
	Scheduler            *SchedulerRepository
	Persona              *PersonaRepository
	Personas             *PersonaRepository
	Relationship         *RelationshipRepository
	Relationships        *RelationshipRepository
	Affect               *AffectiveRepository
	Affective            *AffectiveRepository
	PeerSocial           *PeerSocialRepository
}

// NewRepositories constructs all repositories over one authoritative SQLite
// connection. The caller remains responsible for closing the database.
func NewRepositories(database *sql.DB) (*Repositories, error) {
	if database == nil {
		return nil, fmt.Errorf("%w: database is required", domain.ErrInvalidArgument)
	}
	return &Repositories{
		Agents:               NewAgentRepository(database),
		Personalization:      NewPersonalizationRepository(database),
		Conversations:        NewConversationRepository(database),
		Messages:             NewMessageRepository(database),
		Memories:             NewMemoryRepository(database),
		Archive:              NewArchiveRepository(database),
		Runs:                 NewRunRepository(database),
		Delegations:          NewDelegationRepository(database),
		PeerDialogues:        NewPeerDialogueRepository(database),
		PeerDialogueMessages: NewPeerDialogueMessageRepository(database),
		Approvals:            NewApprovalRepository(database),
		ToolCalls:            NewToolCallRepository(database),
		Audit:                NewAuditRepository(database),
		Plugins:              NewPluginRepository(database),
		Scheduler:            NewSchedulerRepository(database),
		Persona:              NewPersonaRepository(database),
		Personas:             NewPersonaRepository(database),
		Relationship:         NewRelationshipRepository(database),
		Relationships:        NewRelationshipRepository(database),
		Affect:               NewAffectiveRepository(database),
		Affective:            NewAffectiveRepository(database),
		PeerSocial:           NewPeerSocialRepository(database),
	}, nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", domain.ErrInvalidArgument)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func requireDatabase(database *sql.DB) error {
	if database == nil {
		return fmt.Errorf("%w: database is required", domain.ErrInvalidArgument)
	}
	return nil
}

// sqliteTimeLayout is the canonical on-disk timestamp encoding. SQLite compares
// and orders TEXT timestamps byte by byte, so the encoding has to be
// order-preserving: every field fixed width, always UTC, always the same
// trailing 'Z'. time.RFC3339Nano is not — it drops trailing zeros from the
// fractional part, so "12:00:00Z" sorts after "12:00:00.5Z" ('Z' > '.') and
// ".5Z" sorts after ".55Z". A whole-second next_run_at was therefore invisible
// to "next_run_at <= ?" for the entire remainder of that second, and
// ORDER BY created_at inverted any run of prefix fractions.
//
// The layout stays a superset-compatible RFC 3339 string, so scanTime's
// time.RFC3339Nano parse still reads it, SQLite's julianday()/strftime() still
// understand it, and every existing TEXT index stays usable and correctly
// ordered. Migration 000013 normalizes rows written before this change.
const sqliteTimeLayout = "2006-01-02T15:04:05.000000000Z"

// formatTime encodes an instant in the canonical on-disk layout. Every write of
// a timestamp column in this package must go through it: a single writer left
// on the old encoding would make comparisons straddle two formats, which is
// worse than leaving both on the old one.
func formatTime(value time.Time) string {
	return value.UTC().Format(sqliteTimeLayout)
}

func timeValue(value time.Time) (any, error) {
	if value.IsZero() {
		return nil, fmt.Errorf("%w: timestamp is required", domain.ErrInvalidArgument)
	}
	return formatTime(value), nil
}

func nullableTimeValue(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatTime(value)
}

func scanTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return parsed.UTC(), nil
	}
	// SQLite's CURRENT_TIMESTAMP is used by the foundation migration. Keep the
	// parser tolerant of that format for metadata and future migrations.
	parsed, legacyErr := time.Parse("2006-01-02 15:04:05", value)
	if legacyErr == nil {
		return parsed.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("parse timestamp %q: %w", value, err)
}

func scanNullableTime(value sql.NullString) (time.Time, error) {
	if !value.Valid {
		return time.Time{}, nil
	}
	return scanTime(value.String)
}

func marshalJSON(value any, fallback string) (string, error) {
	if value == nil {
		return fallback, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode json: %w", err)
	}
	return string(encoded), nil
}

func validJSON(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", domain.ErrInvalidArgument, field)
	}
	if !json.Valid([]byte(value)) {
		return fmt.Errorf("%w: %s must be valid JSON", domain.ErrInvalidArgument, field)
	}
	return nil
}

func translateSQLError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "run agent is deleted") {
		return domain.ErrNotPermitted
	}
	if strings.Contains(message, "peer dialogue pair is in cooldown") {
		return domain.ErrConflict
	}
	if strings.Contains(message, "unique constraint") ||
		strings.Contains(message, "constraint failed") && strings.Contains(message, "unique") {
		return domain.ErrConflict
	}
	return err
}

func wrappedSQLError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("sqlite %s: %w", operation, translateSQLError(err))
}
