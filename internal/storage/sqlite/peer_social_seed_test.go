package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestDirectionalPeerRelationshipsUseObserverTemperamentAndResetIndependently(t *testing.T) {
	database, ctx := testDatabase(t)
	repositories, err := NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	firstProfile, _ := domain.NewAgentProfile("agent-reserved", "Рин", 21, "female", "", now)
	secondProfile, _ := domain.NewAgentProfile("agent-social", "Мира", 22, "female", "", now.Add(time.Second))
	if err := repositories.Agents.Create(context.Background(), firstProfile); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Agents.Create(context.Background(), secondProfile); err != nil {
		t.Fatal(err)
	}
	reserved, _ := domain.NewPersonalizationSeed(firstProfile, map[string]float64{"trust": .10, "attachment": .10, "sociability": .10, "empathy": .40, "curiosity": .50}, map[string]float64{"trust": .95, "closeness": .95}, now)
	social, _ := domain.NewPersonalizationSeed(secondProfile, map[string]float64{"trust": .90, "attachment": .80, "sociability": .90, "empathy": .90, "curiosity": .90}, map[string]float64{"trust": .05, "closeness": .05}, now)

	forward, err := repositories.PeerSocial.GetOrCreateRelationshipForProfile(ctx, firstProfile.ID, secondProfile.ID, reserved, now)
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := repositories.PeerSocial.GetOrCreateRelationshipForProfile(ctx, secondProfile.ID, firstProfile.ID, social, now)
	if err != nil {
		t.Fatal(err)
	}
	if forward.ID == reverse.ID || forward.Dimensions[domain.RelationshipDimensionTrust] >= reverse.Dimensions[domain.RelationshipDimensionTrust] || forward.Dimensions[domain.RelationshipDimensionCloseness] >= reverse.Dimensions[domain.RelationshipDimensionCloseness] {
		t.Fatalf("directional seeds were not independent: forward=%#v reverse=%#v", forward, reverse)
	}
	if forward.Dimensions[domain.RelationshipDimensionCloseness] >= .5 {
		t.Fatalf("owner relationship seed leaked into peer relation: %#v", forward.Dimensions)
	}

	changed := forward
	changed.Version++
	changed.RevisionID = ""
	changed.ParentID = forward.RevisionID
	changed.ParentVersion = forward.Version
	changed.Operation = domain.RelationshipOperationUpdate
	changed.Dimensions = make(map[string]float64, len(forward.Dimensions))
	for name, value := range forward.Dimensions {
		changed.Dimensions[name] = value
	}
	changed.Dimensions[domain.RelationshipDimensionTrust] += .1
	changed.Reason = "peer dialogue increased trust"
	changed.UpdatedAt = now.Add(time.Minute)
	if _, err := repositories.Relationship.AppendVersion(ctx, changed, forward.Version); err != nil {
		t.Fatal(err)
	}
	reset, err := repositories.Relationship.Reset(ctx, forward.ID, changed.Version, "owner reset peer relationship", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if reset.Dimensions[domain.RelationshipDimensionTrust] != forward.Dimensions[domain.RelationshipDimensionTrust] {
		t.Fatalf("peer reset did not use its own directional seed: %#v", reset)
	}
	reverseAfter, err := repositories.PeerSocial.GetRelationship(ctx, secondProfile.ID, firstProfile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reverseAfter.Version != reverse.Version || reverseAfter.Dimensions[domain.RelationshipDimensionTrust] != reverse.Dimensions[domain.RelationshipDimensionTrust] {
		t.Fatalf("reset changed reverse relation: before=%#v after=%#v", reverse, reverseAfter)
	}
}
