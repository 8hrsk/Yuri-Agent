package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func seedAffectState(t *testing.T, repository *AffectiveRepository, ctx context.Context, id domain.ID, at time.Time) {
	t.Helper()
	state := domain.AffectiveState{ID: id, Emotions: map[string]float64{domain.EmotionJoy: 0}, CreatedAt: at, UpdatedAt: at}
	if err := repository.CreateState(ctx, state); err != nil {
		t.Fatalf("create affect state: %v", err)
	}
}

func insertAffectEventRow(t *testing.T, repository *AffectiveRepository, ctx context.Context, event domain.AffectiveEvent) {
	t.Helper()
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertAffectiveEvent(ctx, transaction, event); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("insert affective event: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

func countAffectEvents(t *testing.T, repository *AffectiveRepository, ctx context.Context, id domain.ID) int {
	t.Helper()
	var count int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM affective_events WHERE affect_id = ?`, string(id)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestAffectRetentionDeletesOnlySpentEvents pins M-4's retention half:
// affective_events is finally pruned, but only for events whose contribution
// is already exactly zero, so a decayed state cannot change because of it.
func TestAffectRetentionDeletesOnlySpentEvents(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewAffectiveRepository(database)
	now := time.Now().UTC().Truncate(time.Second)
	seedAffectState(t, repository, ctx, "affect", now.Add(-2*AffectEventRetention))

	halfLife := int64((7 * 24 * time.Hour).Seconds())
	// Older than retention and past affectDecayHalfLives half-lives: spent.
	insertAffectEventRow(t, repository, ctx, domain.AffectiveEvent{
		ID: "spent_exponential", AffectID: "affect", Emotion: domain.EmotionJoy, Intensity: 1, Valence: 1,
		DecayPolicy: domain.AffectiveDecayExponential, HalfLifeSeconds: halfLife,
		CreatedAt: now.Add(-time.Duration(affectDecayHalfLives+4) * time.Duration(halfLife) * time.Second),
	})
	// Older than retention and already past its explicit expiry: spent.
	insertAffectEventRow(t, repository, ctx, domain.AffectiveEvent{
		ID: "spent_linear", AffectID: "affect", Emotion: domain.EmotionJoy, Intensity: 1, Valence: 1,
		DecayPolicy: domain.AffectiveDecayLinear, DecaysAt: now.Add(-AffectEventRetention - time.Hour),
		CreatedAt: now.Add(-2 * AffectEventRetention),
	})
	// Never decays: contributes forever, so retention must keep it however old.
	insertAffectEventRow(t, repository, ctx, domain.AffectiveEvent{
		ID: "permanent", AffectID: "affect", Emotion: domain.EmotionJoy, Intensity: 0.5, Valence: 1,
		DecayPolicy: domain.AffectiveDecayNone, CreatedAt: now.Add(-2 * AffectEventRetention),
	})
	// Spent, but younger than the retention floor: kept so the recent journal
	// stays inspectable.
	insertAffectEventRow(t, repository, ctx, domain.AffectiveEvent{
		ID: "recent_spent", AffectID: "affect", Emotion: domain.EmotionJoy, Intensity: 1, Valence: 1,
		DecayPolicy: domain.AffectiveDecayLinear, DecaysAt: now.Add(-time.Hour),
		CreatedAt: now.Add(-2 * time.Hour),
	})
	// Still contributing.
	insertAffectEventRow(t, repository, ctx, domain.AffectiveEvent{
		ID: "live", AffectID: "affect", Emotion: domain.EmotionJoy, Intensity: 0.4, Valence: 1,
		DecayPolicy: domain.AffectiveDecayExponential, HalfLifeSeconds: halfLife,
		CreatedAt: now.Add(-24 * time.Hour),
	})

	if got := countAffectEvents(t, repository, ctx, "affect"); got != 5 {
		t.Fatalf("seeded events = %d, want 5", got)
	}
	deleted, err := repository.PruneEvents(ctx, "affect", now)
	if err != nil {
		t.Fatalf("PruneEvents() error = %v", err)
	}
	if deleted != 2 {
		t.Fatalf("PruneEvents() deleted %d events, want 2", deleted)
	}
	remaining, err := repository.ListEvents(ctx, "affect")
	if err != nil {
		t.Fatal(err)
	}
	kept := make(map[domain.ID]bool, len(remaining))
	for _, event := range remaining {
		kept[event.ID] = true
	}
	for _, id := range []domain.ID{"permanent", "recent_spent", "live"} {
		if !kept[id] {
			t.Fatalf("retention deleted %q, which can still be needed", id)
		}
	}
	for _, id := range []domain.ID{"spent_exponential", "spent_linear"} {
		if kept[id] {
			t.Fatalf("retention kept spent event %q", id)
		}
	}

	// Retention is idempotent: a second pass has nothing left to remove.
	again, err := repository.PruneEvents(ctx, "affect", now)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("second PruneEvents() deleted %d events, want 0", again)
	}
}

// TestAffectDecayMatchesFullJournalRead pins M-4's read half: Decay no longer
// deserializes the whole append-only journal, and the snapshot it produces is
// identical to the one the unbounded read produced.
func TestAffectDecayMatchesFullJournalRead(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewAffectiveRepository(database)
	now := time.Now().UTC().Truncate(time.Second)
	seedAffectState(t, repository, ctx, "affect", now.Add(-3*AffectEventRetention))

	halfLife := int64((7 * 24 * time.Hour).Seconds())
	insertAffectEventRow(t, repository, ctx, domain.AffectiveEvent{
		ID: "live_joy", AffectID: "affect", Emotion: domain.EmotionJoy, Intensity: 0.8, Valence: 1,
		DecayPolicy: domain.AffectiveDecayExponential, HalfLifeSeconds: halfLife,
		CreatedAt: now.Add(-time.Duration(halfLife) * time.Second),
	})
	insertAffectEventRow(t, repository, ctx, domain.AffectiveEvent{
		ID: "permanent_joy", AffectID: "affect", Emotion: domain.EmotionJoy, Intensity: 0.1, Valence: 1,
		DecayPolicy: domain.AffectiveDecayNone, CreatedAt: now.Add(-3 * AffectEventRetention),
	})
	// A large tail of long-expired events. These are exactly what the old
	// unbounded read pulled off disk and JSON-decoded on every recomputation.
	for index := 0; index < 500; index++ {
		insertAffectEventRow(t, repository, ctx, domain.AffectiveEvent{
			ID: domain.ID(fmt.Sprintf("expired_%d", index)), AffectID: "affect",
			Emotion: domain.EmotionJoy, Intensity: 1, Valence: -1,
			DecayPolicy: domain.AffectiveDecayLinear,
			DecaysAt:    now.Add(-2*AffectEventRetention + time.Duration(index)*time.Second),
			CreatedAt:   now.Add(-3*AffectEventRetention + time.Duration(index)*time.Second),
		})
	}

	all, err := repository.ListEvents(ctx, "affect")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 502 {
		t.Fatalf("seeded events = %d, want 502", len(all))
	}
	contributors, err := repository.listDecayContributors(ctx, "affect", now)
	if err != nil {
		t.Fatalf("listDecayContributors() error = %v", err)
	}
	if len(contributors) != 2 {
		t.Fatalf("decay read %d events, want the 2 that still contribute", len(contributors))
	}

	current, err := repository.GetState(ctx, "affect")
	if err != nil {
		t.Fatal(err)
	}
	// The bounded read produces the same snapshot as reading the whole journal.
	want := current.Decay(all, now)
	got := current.Decay(contributors, now)
	if got.Emotions[domain.EmotionJoy] != want.Emotions[domain.EmotionJoy] {
		t.Fatalf("decayed joy = %v, want %v (full-journal result)", got.Emotions[domain.EmotionJoy], want.Emotions[domain.EmotionJoy])
	}

	decayed, err := repository.Decay(ctx, "affect", current.Version, now, "retention parity")
	if err != nil {
		t.Fatalf("Decay() error = %v", err)
	}
	if decayed.Emotions[domain.EmotionJoy] != want.Emotions[domain.EmotionJoy] {
		t.Fatalf("Decay() joy = %v, want %v", decayed.Emotions[domain.EmotionJoy], want.Emotions[domain.EmotionJoy])
	}
	// Decay applied retention on the way through, so the expired tail is gone.
	if remaining := countAffectEvents(t, repository, ctx, "affect"); remaining != 2 {
		t.Fatalf("events after Decay() = %d, want 2", remaining)
	}
}
