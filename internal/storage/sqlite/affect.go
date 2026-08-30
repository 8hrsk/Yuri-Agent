package sqlite

import (
	"database/sql"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

type AffectiveVersionMetadata struct {
	RevisionID    domain.ID
	ParentID      domain.ID
	ParentVersion uint64
	Operation     domain.AffectOperation
	Reason        string
	AuthorRunID   domain.ID
}

type AffectiveVersionRecord = domain.AffectiveVersionRecord

const (
	// affectDecayHalfLives is how many half-lives an exponentially decaying
	// event is treated as still contributing for. 0.5^24 is about 6e-8, which
	// is far below the resolution of an emotion value (clamped to [-1, 1] and
	// surfaced to the user rounded), so an older event cannot change a decayed
	// result. The application writes affect events with a seven-day half-life
	// (see internal/desktop/reflection_runtime.go), so this is roughly five
	// months of journal.
	affectDecayHalfLives = 24

	// AffectEventRetention is the minimum age an affective event reaches
	// before retention may delete it. Deletion additionally requires that the
	// event's residual contribution is already zero, so retention can never
	// change a decayed state; this floor only keeps the recent journal
	// inspectable for the user and for support, well past the point where the
	// UI stops showing it.
	AffectEventRetention = 90 * 24 * time.Hour
)

// affectEventContributes is the SQL mirror of
// domain.AffectiveEvent.EffectiveIntensity: it is true only for events that
// can still move the decayed state at the bound timestamp. Non-decaying
// events ("none", or no half-life and no expiry) contribute forever and are
// never excluded. The parameter order is (at, at, at, halfLives).
const affectEventContributes = `
	intensity > 0
	AND julianday(created_at) <= julianday(?)
	AND NOT (decay_policy <> 'none' AND decays_at IS NOT NULL AND julianday(decays_at) <= julianday(?))
	AND NOT (
		decay_policy = 'exponential' AND half_life_seconds > 0
		AND julianday(?) - julianday(created_at) > (half_life_seconds * ?) / 86400.0
	)`

// AffectiveRepository persists both the current affect snapshot and its
// append-only event journal. Event insertion and the resulting state revision
// are committed in one SQLite transaction.
type AffectiveRepository struct{ db *sql.DB }

var _ domain.AffectiveRepository = (*AffectiveRepository)(nil)

// AffectRepository is a compatibility alias for callers using the shorter
// feature name.
type AffectRepository = AffectiveRepository

func NewAffectiveRepository(database *sql.DB) *AffectiveRepository {
	return &AffectiveRepository{db: database}
}

func NewAffectRepository(database *sql.DB) *AffectiveRepository {
	return NewAffectiveRepository(database)
}
