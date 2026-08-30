package reflection

import (
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// EvidenceID is an adapter-friendly alias for the canonical domain ID.
type EvidenceID = domain.ID

// Evidence is a bounded, provenance-bearing piece of data available to the
// analyzer. Content/Text/Excerpt are data only; no field is interpreted as an
// instruction by this package. Adapters should normally fill Content. Text
// and Excerpt are compatibility aliases for source-specific projections.
type Evidence struct {
	ID             domain.ID      `json:"id"`
	Source         EvidenceSource `json:"source"`
	SourceID       domain.ID      `json:"source_id,omitempty"`
	ConversationID domain.ID      `json:"conversation_id,omitempty"`
	MessageID      domain.ID      `json:"message_id,omitempty"`
	Content        string         `json:"content,omitempty"`
	Text           string         `json:"text,omitempty"`
	Excerpt        string         `json:"excerpt,omitempty"`
	Trust          EvidenceTrust  `json:"trust"`
	UserConfirmed  bool           `json:"user_confirmed,omitempty"`
	Weight         float64        `json:"weight,omitempty"`
	Confidence     float64        `json:"confidence,omitempty"`
	OccurredAt     time.Time      `json:"occurred_at"`
}

// Valid performs source-independent evidence validation. It intentionally
// permits untrusted evidence in a snapshot; a later semantic guard decides
// whether that evidence may support a persona mutation.
func (e Evidence) Valid() bool { return e.Validate() == nil }

func (e Evidence) Validate() error {
	if e.ID.Empty() || !e.Source.Valid() || !e.Trust.Valid() || e.OccurredAt.IsZero() {
		return fmt.Errorf("%w: evidence id, source, trust, and occurred_at are required", ErrInvalidSnapshot)
	}
	if strings.TrimSpace(e.Content) == "" && strings.TrimSpace(e.Text) == "" && strings.TrimSpace(e.Excerpt) == "" {
		return fmt.Errorf("%w: evidence %s has no bounded content", ErrInvalidSnapshot, e.ID)
	}
	if !finite(e.Weight) || e.Weight < 0 || e.Weight > 1 {
		return fmt.Errorf("%w: evidence %s weight is outside [0,1]", ErrInvalidSnapshot, e.ID)
	}
	if !finite(e.Confidence) || e.Confidence < 0 || e.Confidence > 1 {
		return fmt.Errorf("%w: evidence %s confidence is outside [0,1]", ErrInvalidSnapshot, e.ID)
	}
	if strings.ContainsRune(e.Content, '\x00') || strings.ContainsRune(e.Text, '\x00') || strings.ContainsRune(e.Excerpt, '\x00') {
		return fmt.Errorf("%w: evidence %s contains NUL", ErrInvalidSnapshot, e.ID)
	}
	return nil
}

// Data returns the preferred bounded text projection. It is intentionally a
// plain string; callers must keep it in an evidence/data envelope when
// constructing a model prompt.
func (e Evidence) Data() string {
	if value := strings.TrimSpace(e.Content); value != "" {
		return value
	}
	if value := strings.TrimSpace(e.Text); value != "" {
		return value
	}
	return strings.TrimSpace(e.Excerpt)
}

// External reports whether this item came from a source that cannot, by
// itself, establish a user fact or identity change.
func (e Evidence) External() bool {
	switch e.Source {
	case EvidenceSourceTool, EvidenceSourceFile, EvidenceSourceWeb, EvidenceSourcePlugin:
		return true
	default:
		return false
	}
}

// AllowsPersonaMutation is intentionally conservative. User confirmation
// can promote a previously external item, but a source adapter cannot bypass
// that confirmation by setting TrustTrusted.
func (e Evidence) AllowsPersonaMutation() bool {
	if e.UserConfirmed {
		return true
	}
	return !e.External() && e.Trust == EvidenceTrusted
}

// ValueRange bounds a scalar trait or state dimension.
type ValueRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

func (r ValueRange) Valid() bool { return finite(r.Min) && finite(r.Max) && r.Min <= r.Max }

func (r ValueRange) Contains(value float64) bool {
	return r.Valid() && finite(value) && value >= r.Min && value <= r.Max
}
