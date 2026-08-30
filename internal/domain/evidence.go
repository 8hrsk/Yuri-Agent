package domain

import (
	"fmt"
	"strings"
	"time"
)

// EvidenceLink points at durable evidence without copying potentially
// sensitive source text into persona/relationship state. ExcerptHash is an
// integrity hint, not the excerpt itself.
type EvidenceLink struct {
	ID             ID        `json:"id,omitempty"`
	SourceType     string    `json:"source_type"`
	SourceID       ID        `json:"source_id,omitempty"`
	RunID          ID        `json:"run_id,omitempty"`
	ConversationID ID        `json:"conversation_id,omitempty"`
	MessageID      ID        `json:"message_id,omitempty"`
	ExcerptHash    string    `json:"excerpt_hash,omitempty"`
	Provenance     string    `json:"provenance,omitempty"`
	UserConfirmed  bool      `json:"user_confirmed,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Evidence and PersonaEvidence are aliases used by callers that prefer a
// shorter or more specific name. They intentionally share one validation
// contract across persona, relationship, and affect writes.
type Evidence = EvidenceLink
type PersonaEvidence = EvidenceLink
type RelationshipEvidence = EvidenceLink

func (e EvidenceLink) Validate() error {
	if strings.TrimSpace(e.SourceType) == "" {
		return fmt.Errorf("%w: evidence source type is required", ErrInvalidArgument)
	}
	if e.SourceID.Empty() && e.RunID.Empty() && e.ConversationID.Empty() && e.MessageID.Empty() && strings.TrimSpace(e.ExcerptHash) == "" {
		return fmt.Errorf("%w: evidence must reference a durable source", ErrInvalidArgument)
	}
	// Zero evidence timestamps are accepted for candidates and filled from the
	// parent revision by repositories. A non-zero value is retained verbatim.
	return nil
}

func (e EvidenceLink) Valid() bool { return e.Validate() == nil }
