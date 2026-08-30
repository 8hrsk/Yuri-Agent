package domain

import (
	"strings"
	"testing"
	"time"
)

func TestNewPersonalizationSeedPreservesLegacyTraitsAndIdentity(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	profile, err := NewAgentProfileWithBackstory("agent-yuri", "Yuri", 21, "female", "Любит лаконичность.", "Выросла в библиотеке.", now)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := NewPersonalizationSeed(profile, map[string]float64{"warmth": .91, "dry_humor": .44}, map[string]float64{"trust": .2}, now)
	if err != nil {
		t.Fatal(err)
	}
	if seed.SchemaVersion != PersonalizationSchemaVersion || seed.Version != 1 || seed.AgentID != profile.ID {
		t.Fatalf("seed identity = %#v", seed)
	}
	if seed.Identity.SelfDescription != profile.Preferences || seed.Backstory.Narrative != profile.Backstory {
		t.Fatalf("owner identity was not preserved: %#v", seed)
	}
	if seed.Temperament.Warmth != .91 || seed.Temperament.Custom["dry_humor"] != .44 {
		t.Fatalf("traits were not preserved: %#v", seed.Temperament)
	}
	if seed.CommunicationStyle.Softness != .91 || seed.RelationshipSeed.Dimensions["trust"] != .2 {
		t.Fatalf("derived seed mismatch: %#v", seed)
	}
	if _, ok := seed.EvolutionPolicy.TraitBounds["dry_humor"]; !ok {
		t.Fatal("custom trait has no evolution bound")
	}
	if !seed.EvolutionPolicy.ReflectionEnabled(false) || seed.EvolutionPolicy.ReflectionCooldown(5*time.Minute) != time.Hour {
		t.Fatalf("default per-agent reflection policy = %#v", seed.EvolutionPolicy)
	}
}

func TestTemperamentRoundTripsTraitMap(t *testing.T) {
	input := map[string]float64{"warmth": .12, "fearfulness": .88, "custom_habit": .33}
	got := TemperamentFromTraits(input).Traits()
	for name, value := range input {
		if got[name] != value {
			t.Fatalf("trait %s = %v, want %v", name, got[name], value)
		}
	}
	if len(StandardTemperamentTraitNames()) != 25 {
		t.Fatalf("standard traits = %d, want 25", len(StandardTemperamentTraitNames()))
	}
}

func TestPersonalizationSeedRejectsUnsafeOrInvalidState(t *testing.T) {
	now := time.Now().UTC()
	profile, err := NewAgentProfile("agent", "Yuri", 21, "female", "", now)
	if err != nil {
		t.Fatal(err)
	}
	base, err := NewPersonalizationSeed(profile, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*PersonalizationSeed)
	}{
		{name: "schema", mutate: func(seed *PersonalizationSeed) { seed.SchemaVersion = 3 }},
		{name: "style range", mutate: func(seed *PersonalizationSeed) { seed.CommunicationStyle.Humor = 1.1 }},
		{name: "trait range", mutate: func(seed *PersonalizationSeed) { seed.Temperament.Fearfulness = -0.1 }},
		{name: "conflict style", mutate: func(seed *PersonalizationSeed) { seed.EmotionalDynamics.ConflictStyle = "revenge" }},
		{name: "trigger instruction", mutate: func(seed *PersonalizationSeed) {
			seed.EmotionalDynamics.Triggers = map[string][]string{"anger": {strings.Repeat("x", 257)}}
		}},
		{name: "backstory nul", mutate: func(seed *PersonalizationSeed) { seed.Backstory.Narrative = "past\x00prompt" }},
		{name: "unknown relationship", mutate: func(seed *PersonalizationSeed) { seed.RelationshipSeed.Preset = "unknown" }},
		{name: "bound outside trait", mutate: func(seed *PersonalizationSeed) {
			seed.EvolutionPolicy.TraitBounds["missing"] = NumericRange{Min: 0, Max: 1}
		}},
		{name: "reflection mode", mutate: func(seed *PersonalizationSeed) { seed.EvolutionPolicy.ReflectionMode = "always" }},
		{name: "reflection cooldown", mutate: func(seed *PersonalizationSeed) { seed.EvolutionPolicy.ReflectionCooldownMinutes = 10081 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.Temperament.Custom = cloneFloatMap(base.Temperament.Custom)
			candidate.EvolutionPolicy.TraitBounds = make(map[string]NumericRange, len(base.EvolutionPolicy.TraitBounds))
			for name, valueRange := range base.EvolutionPolicy.TraitBounds {
				candidate.EvolutionPolicy.TraitBounds[name] = valueRange
			}
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("Validate() accepted %#v", candidate)
			}
		})
	}
}

func TestPersonalizationSeedRequiresLinearParentHistory(t *testing.T) {
	now := time.Now().UTC()
	profile, _ := NewAgentProfile("agent", "Yuri", 21, "female", "", now)
	seed, _ := NewPersonalizationSeed(profile, nil, nil, now)
	seed.Version = 2
	seed.RevisionID = "agent:personalization:v2"
	seed.ParentID = "agent:personalization:v1"
	seed.ParentVersion = 1
	seed.Operation = PersonalizationOperationUpdate
	seed.UpdatedAt = now.Add(time.Second)
	if err := seed.Validate(); err != nil {
		t.Fatalf("valid second revision rejected: %v", err)
	}
	seed.ParentVersion = 0
	if err := seed.Validate(); err == nil {
		t.Fatal("non-linear parent history accepted")
	}
}
