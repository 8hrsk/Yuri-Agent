package domain

import (
	"strings"
	"testing"
	"time"
)

func relationshipTestSeed(t *testing.T, id ID, traits, dimensions map[string]float64, preset RelationshipSeedPreset) PersonalizationSeed {
	t.Helper()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	profile, err := NewAgentProfileWithBackstory(id, "Yuri", 21, "female", "", "Вымышленное прошлое", now)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := NewPersonalizationSeed(profile, traits, dimensions, now)
	if err != nil {
		t.Fatal(err)
	}
	seed.RelationshipSeed.Preset = preset
	seed.RelationshipSeed.Dimensions = cloneFloatMap(dimensions)
	seed.RelationshipSeed.Summary = "Субъективная исходная история связи."
	if err := seed.Validate(); err != nil {
		t.Fatal(err)
	}
	return seed
}

func TestOwnerRelationshipProjectsSeedAndMarksCustomStoryFictional(t *testing.T) {
	now := time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
	seed := relationshipTestSeed(t, "agent-custom", nil, map[string]float64{"trust": .81, "attachment": .72}, RelationshipSeedCustom)
	state, err := NewOwnerRelationshipState(seed, now)
	if err != nil {
		t.Fatal(err)
	}
	if state.Dimensions["trust"] != .81 || state.Summary != seed.RelationshipSeed.Summary {
		t.Fatalf("owner relationship did not project seed: %#v", state)
	}
	if !strings.Contains(state.Reason, string(RelationshipSeedCustom)) || len(state.Evidence) != 1 || state.Evidence[0].Provenance != "fictional_owner_relationship_seed" {
		t.Fatalf("custom story boundary missing: %#v", state)
	}
}

func TestPeerRelationshipUsesObserverPredispositionWithoutOwnerScenario(t *testing.T) {
	now := time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
	reserved := relationshipTestSeed(t, "agent-reserved", map[string]float64{"trust": .15, "attachment": .12, "sociability": .10, "empathy": .45, "curiosity": .70}, map[string]float64{"trust": .95, "attachment": .95, "closeness": .95}, RelationshipSeedRomantic)
	social := relationshipTestSeed(t, "agent-social", map[string]float64{"trust": .85, "attachment": .75, "sociability": .90, "empathy": .85, "curiosity": .90}, map[string]float64{"trust": .10, "attachment": .05, "closeness": .05}, RelationshipSeedProfessional)

	first, err := NewPeerRelationshipState("peer-reserved", reserved, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPeerRelationshipState("peer-social", social, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Dimensions[RelationshipDimensionTrust] >= second.Dimensions[RelationshipDimensionTrust] || first.Dimensions[RelationshipDimensionCloseness] >= second.Dimensions[RelationshipDimensionCloseness] {
		t.Fatalf("observer temperament was not projected independently: first=%#v second=%#v", first.Dimensions, second.Dimensions)
	}
	if first.Dimensions[RelationshipDimensionAttachment] >= .5 || first.Dimensions[RelationshipDimensionCloseness] >= .5 {
		t.Fatalf("owner romantic scenario leaked into peer seed: %#v", first.Dimensions)
	}
	if len(first.Evidence) != 1 || first.Evidence[0].Provenance != "observer_temperament_seed" {
		t.Fatalf("peer provenance = %#v", first.Evidence)
	}
}
