package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestApplyReflectionStateCommitsAllTargetsAtomically(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 28, 16, 0, 0, 0, time.UTC)
	persona, _ := domain.NewMutablePersona("owner", map[string]float64{"warmth": .5}, "warm", now)
	relationship, _ := domain.NewRelationshipState("owner", map[string]float64{"trust": .5}, "forming", now)
	affect, _ := domain.NewAffectiveState("owner", map[string]float64{"joy": .1}, "calm", now)
	if err := repositories.Persona.Create(ctx, persona); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Relationship.Create(ctx, relationship); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Affect.Create(ctx, affect); err != nil {
		t.Fatal(err)
	}
	evidence := stage5Evidence()
	persona.Version, persona.Traits, persona.Reason, persona.Evidence, persona.UpdatedAt = 2, map[string]float64{"warmth": .55}, "reflection", evidence, now.Add(time.Minute)
	relationship.Version, relationship.Dimensions, relationship.Reason, relationship.Evidence, relationship.UpdatedAt = 2, map[string]float64{"trust": .55}, "reflection", evidence, now.Add(time.Minute)
	affect.Version, affect.Emotions, affect.Reason, affect.UpdatedAt = 2, map[string]float64{"joy": .2}, "reflection", now.Add(time.Minute)
	affectEvent := domain.AffectiveEvent{ID: "affect-event", Emotion: "joy", Intensity: .1, Valence: 1, SourceType: "message", SourceID: "message-1", Evidence: stage5Evidence(), CreatedAt: now.Add(time.Minute)}
	if err := repositories.ApplyReflectionState(ctx, ReflectionStateMutation{Persona: &persona, ExpectedPersona: 1, Relationship: &relationship, ExpectedRelationship: 1, Affect: &affect, ExpectedAffect: 1, AffectEvents: []domain.AffectiveEvent{affectEvent}}); err != nil {
		t.Fatal(err)
	}
	gotPersona, _ := repositories.Persona.Get(ctx, "owner")
	gotRelationship, _ := repositories.Relationship.Get(ctx, "owner")
	gotAffect, _ := repositories.Affect.Get(ctx, "owner")
	if gotPersona.Version != 2 || gotRelationship.Version != 2 || gotAffect.Version != 2 {
		t.Fatalf("versions = %d/%d/%d", gotPersona.Version, gotRelationship.Version, gotAffect.Version)
	}
	events, err := repositories.Affect.ListEvents(ctx, "owner")
	if err != nil || len(events) != 1 || events[0].StateVersion != 2 {
		t.Fatalf("affect events = %#v, %v", events, err)
	}
}

func TestApplyReflectionStateConflictRollsBackEveryTarget(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, _ := NewRepositories(database)
	now := time.Now().UTC()
	persona, _ := domain.NewMutablePersona("owner", map[string]float64{"warmth": .5}, "warm", now)
	relationship, _ := domain.NewRelationshipState("owner", map[string]float64{"trust": .5}, "forming", now)
	if err := repositories.Persona.Create(ctx, persona); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Relationship.Create(ctx, relationship); err != nil {
		t.Fatal(err)
	}
	persona.Version, persona.Traits, persona.Reason, persona.Evidence, persona.UpdatedAt = 2, map[string]float64{"warmth": .55}, "reflection", stage5Evidence(), now.Add(time.Minute)
	relationship.Version, relationship.Dimensions, relationship.Reason, relationship.Evidence, relationship.UpdatedAt = 2, map[string]float64{"trust": .55}, "reflection", stage5Evidence(), now.Add(time.Minute)
	err := repositories.ApplyReflectionState(context.Background(), ReflectionStateMutation{Persona: &persona, ExpectedPersona: 1, Relationship: &relationship, ExpectedRelationship: 99})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want conflict", err)
	}
	current, _ := repositories.Persona.Get(ctx, "owner")
	if current.Version != 1 {
		t.Fatalf("persona partially committed at version %d", current.Version)
	}
}
