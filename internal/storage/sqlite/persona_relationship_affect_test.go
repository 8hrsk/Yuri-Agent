package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func stage5Evidence() []domain.EvidenceLink {
	return []domain.EvidenceLink{{SourceType: "message", MessageID: "message-1", ExcerptHash: "sha256:excerpt"}}
}

func TestPersonaRepositoryVersionHistoryRollbackResetAndPins(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewPersonaRepository(database)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	persona := domain.MutablePersona{ID: "persona", Traits: map[string]float64{"warmth": 0.5, "trust": 0.5},
		IdentityPrompt: "gentle", CreatedAt: now, UpdatedAt: now}
	if err := repository.Create(ctx, persona); err != nil {
		t.Fatalf("create persona: %v", err)
	}
	current, err := repository.Get(ctx, "persona")
	if err != nil || current.Version != 1 || current.RevisionID == "" {
		t.Fatalf("initial persona = %#v, %v", current, err)
	}
	next := current
	next.Version = 2
	next.Traits = map[string]float64{"warmth": 0.6, "trust": 0.5}
	next.Reason = "positive interaction"
	next.Evidence = stage5Evidence()
	next.UpdatedAt = now.Add(time.Minute)
	appended, err := repository.AppendVersion(ctx, next, 1)
	if err != nil {
		t.Fatalf("append persona: %v", err)
	}
	if appended.Version != 2 || appended.RevisionID == "" || appended.ParentVersion != 1 {
		t.Fatalf("appended persona = %#v", appended)
	}
	if _, err := repository.AppendVersion(ctx, next, 1); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale append error = %v, want conflict", err)
	}
	if _, err := repository.PinTrait(ctx, "persona", 2, "warmth", true, now.Add(2*time.Minute), "user pin"); err != nil {
		t.Fatalf("pin trait: %v", err)
	}
	current, err = repository.Get(ctx, "persona")
	if err != nil || current.Version != 3 || len(current.PinnedTraits) != 1 {
		t.Fatalf("pinned persona = %#v, %v", current, err)
	}
	blocked := current
	blocked.Version++
	blocked.Traits = map[string]float64{"warmth": 0.7, "trust": 0.5}
	blocked.Reason = "drift"
	blocked.Evidence = stage5Evidence()
	blocked.UpdatedAt = now.Add(3 * time.Minute)
	if _, err := repository.AppendVersion(ctx, blocked, 3); !errors.Is(err, domain.ErrNotPermitted) {
		t.Fatalf("pinned append error = %v, want not permitted", err)
	}
	rolled, err := repository.Rollback(ctx, "persona", uint64(1), "undo drift", now.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("rollback persona: %v", err)
	}
	if rolled.Version != 4 || rolled.Operation != domain.PersonaOperationRollback || rolled.Traits["warmth"] != 0.5 {
		t.Fatalf("rolled persona = %#v", rolled)
	}
	reset, err := repository.Reset(ctx, "persona", uint64(4), "reset seed", now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("reset persona: %v", err)
	}
	if reset.Version != 5 || reset.Operation != domain.PersonaOperationReset || reset.Traits["warmth"] != 0.5 {
		t.Fatalf("reset persona = %#v", reset)
	}
	history, err := repository.ListHistory(ctx, "persona")
	if err != nil || len(history) != 5 || history[0].Persona.Version != 5 || history[4].Persona.Version != 1 {
		t.Fatalf("persona history = %#v, %v", history, err)
	}
	var auditRows int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE action LIKE 'persona.%' AND target = 'persona'`).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if auditRows != len(history) {
		t.Fatalf("persona versions=%d atomic audit rows=%d", len(history), auditRows)
	}
}

func TestPersonaRepositoryRejectsInvalidPinnedTraitSets(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewPersonaRepository(database)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	persona := domain.MutablePersona{ID: "persona", Traits: map[string]float64{"warmth": .5}, IdentityPrompt: "gentle", CreatedAt: now, UpdatedAt: now}
	if err := repository.Create(ctx, persona); err != nil {
		t.Fatal(err)
	}
	for _, pinned := range [][]string{{"missing"}, {"warmth", "warmth"}, {"INVALID"}} {
		if _, err := repository.SetPinnedTraits(ctx, persona.ID, 1, pinned, now.Add(time.Second), "invalid"); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("SetPinnedTraits(%v) error = %v", pinned, err)
		}
	}
	var auditRows int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE action LIKE 'persona.%' AND target = 'persona'`).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if auditRows != 1 {
		t.Fatalf("rejected pin writes produced audit/state rows: %d", auditRows)
	}
}

func TestRelationshipRepositoryPersistsOpinionEvidenceAndRollback(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewRelationshipRepository(database)
	now := time.Date(2026, 8, 28, 13, 0, 0, 0, time.UTC)
	state := domain.RelationshipState{ID: "relationship", Dimensions: map[string]float64{"trust": 0.5}, CreatedAt: now, UpdatedAt: now}
	if err := repository.Create(ctx, state); err != nil {
		t.Fatalf("create relationship: %v", err)
	}
	opinion := domain.RelationshipOpinion{Subject: "owner", Claim: "usually reliable", Confidence: 0.8, Evidence: stage5Evidence(), CreatedAt: now}
	updated, err := repository.RecordOpinion(ctx, "relationship", 1, opinion, now.Add(time.Minute), "new evidence")
	if err != nil {
		t.Fatalf("record opinion: %v", err)
	}
	if updated.Version != 2 || len(updated.Opinions) != 1 || updated.Opinions[0].Confidence != 0.8 {
		t.Fatalf("relationship update = %#v", updated)
	}
	loaded, err := repository.Get(ctx, "relationship")
	if err != nil || len(loaded.Opinions) != 1 || len(loaded.Opinions[0].Evidence) != 1 {
		t.Fatalf("loaded relationship = %#v, %v", loaded, err)
	}
	rolled, err := repository.Rollback(ctx, "relationship", uint64(1), "remove opinion", now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("rollback relationship: %v", err)
	}
	if rolled.Version != 3 || len(rolled.Opinions) != 0 || rolled.Operation != domain.RelationshipOperationRollback {
		t.Fatalf("rolled relationship = %#v", rolled)
	}
	history, err := repository.ListVersions(ctx, "relationship")
	if err != nil || len(history) != 3 {
		t.Fatalf("relationship history = %#v, %v", history, err)
	}
}

func TestAffectiveRepositoryAtomicEventDecayAndReset(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewAffectiveRepository(database)
	now := time.Date(2026, 8, 28, 14, 0, 0, 0, time.UTC)
	state := domain.AffectiveState{ID: "affect", Emotions: map[string]float64{}, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateState(ctx, state); err != nil {
		t.Fatalf("create affect: %v", err)
	}
	event := domain.AffectiveEvent{ID: "event-1", Emotion: domain.EmotionJoy, Intensity: 0.8, Valence: 1,
		DecayPolicy: domain.AffectiveDecayLinear, DecaysAt: now.Add(time.Hour), CreatedAt: now.Add(time.Minute), SourceType: "message", SourceID: "message-1"}
	updated, err := repository.AppendEvent(ctx, "affect", uint64(1), event)
	if err != nil {
		t.Fatalf("append affect event: %v", err)
	}
	if updated.Version != 2 || updated.Emotions[domain.EmotionJoy] != 0.8 {
		t.Fatalf("updated affect = %#v", updated)
	}
	storedEvent, err := repository.GetEvent(ctx, "event-1")
	if err != nil || storedEvent.AffectID != "affect" || storedEvent.StateVersion != 2 {
		t.Fatalf("stored event = %#v, %v", storedEvent, err)
	}
	events, err := repository.ListEvents(ctx, "affect")
	if err != nil || len(events) != 1 {
		t.Fatalf("affect events = %#v, %v", events, err)
	}
	decayed, err := repository.Decay(ctx, "affect", uint64(2), now.Add(time.Hour+time.Minute), "time decay")
	if err != nil {
		t.Fatalf("decay affect: %v", err)
	}
	if decayed.Version != 3 || decayed.Emotions[domain.EmotionJoy] != 0 {
		t.Fatalf("decayed affect = %#v", decayed)
	}
	reset, err := repository.Reset(ctx, "affect", uint64(3), "reset affect", now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("reset affect: %v", err)
	}
	if reset.Version != 4 || reset.Emotions[domain.EmotionJoy] != 0 || reset.Operation != domain.AffectOperationReset {
		t.Fatalf("reset affect = %#v", reset)
	}
	var stateRows, eventRows int
	if err := database.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM affective_states WHERE affect_id = 'affect'`).Scan(&stateRows); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM affective_events WHERE affect_id = 'affect'`).Scan(&eventRows); err != nil {
		t.Fatal(err)
	}
	if stateRows != 4 || eventRows != 1 {
		t.Fatalf("durable affect rows states=%d events=%d", stateRows, eventRows)
	}
}

func TestAffectiveRepositoryRejectsInvalidAppendedState(t *testing.T) {
	database, ctx := testDatabase(t)
	repository := NewAffectiveRepository(database)
	now := time.Date(2026, 8, 28, 14, 0, 0, 0, time.UTC)
	seed := domain.AffectiveState{ID: "affect", Emotions: map[string]float64{"warmth": .2}, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateState(ctx, seed); err != nil {
		t.Fatal(err)
	}
	current, err := repository.GetState(ctx, seed.ID)
	if err != nil {
		t.Fatal(err)
	}
	invalid := current
	invalid.Version++
	invalid.UpdatedAt = now.Add(time.Second)
	invalid.Emotions = map[string]float64{"warmth": 2}
	if _, err = repository.AppendVersion(ctx, invalid, current.Version); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("AppendVersion invalid state error = %v", err)
	}
	loaded, err := repository.GetState(ctx, seed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != current.Version {
		t.Fatalf("invalid append advanced head to version %d", loaded.Version)
	}
	var auditRows int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE action LIKE 'affect.%' AND target = 'affect'`).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if auditRows != 1 {
		t.Fatalf("rejected affect append produced audit/state rows: %d", auditRows)
	}
}
