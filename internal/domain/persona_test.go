package domain

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestMutablePersonaValidationEnforcesBoundsPinsAndEvidence(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	base := MutablePersona{ID: "persona", Version: 1, Traits: map[string]float64{"warmth": 0.5},
		PinnedTraits: []string{"warmth"}, CreatedAt: now, UpdatedAt: now}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid persona rejected: %v", err)
	}
	if err := (MutablePersona{ID: "persona", Version: 1, Traits: map[string]float64{"policy_mode": 0.1}, CreatedAt: now, UpdatedAt: now}).Validate(); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("immutable policy trait error = %v, want invalid argument", err)
	}
	if err := (MutablePersona{ID: "persona", Version: 1, Traits: map[string]float64{"warmth": math.NaN()}, CreatedAt: now, UpdatedAt: now}).Validate(); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("NaN trait error = %v, want invalid argument", err)
	}
	next := base
	next.Version = 2
	next.Traits = map[string]float64{"warmth": 0.5}
	next.PinnedTraits = []string{"warmth"}
	next.Reason = "owner preference"
	next.Evidence = []EvidenceLink{{SourceType: "message", MessageID: "m1"}}
	if err := ValidatePersonaEvolution(base, next, DefaultPersonaLimits); err != nil {
		t.Fatalf("unchanged pinned evolution rejected: %v", err)
	}
	next.Traits["warmth"] = 0.6
	if err := ValidatePersonaEvolution(base, next, DefaultPersonaLimits); !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("pinned trait change error = %v, want not permitted", err)
	}
}

func TestAffectiveEventDecayAndApplication(t *testing.T) {
	created := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	event := AffectiveEvent{ID: "event", Emotion: EmotionJoy, Intensity: 1, Valence: 1,
		DecayPolicy: AffectiveDecayLinear, DecaysAt: created.Add(10 * time.Minute), CreatedAt: created}
	if got := event.EffectiveIntensity(created.Add(5 * time.Minute)); got < 0.49 || got > 0.51 {
		t.Fatalf("half-life linear intensity = %v, want about .5", got)
	}
	if got := event.EffectiveIntensity(created.Add(11 * time.Minute)); got != 0 {
		t.Fatalf("expired intensity = %v, want zero", got)
	}
	state, err := NewAffectiveState("affect", nil, "", created)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := state.ApplyEvent(event, created)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Emotions[EmotionJoy] != 1 {
		t.Fatalf("applied affect = %#v", updated)
	}
}
