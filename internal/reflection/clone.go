package reflection

import (
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// Clone helpers below are intentionally internal to prevent analyzer code
// from mutating snapshots or state held by the caller.
func cloneSnapshot(input InputSnapshot) InputSnapshot {
	output := input
	output.ImmutablePolicy = string([]byte(input.ImmutablePolicy))
	output.IdentitySeed = string([]byte(input.IdentitySeed))
	output.AffectPolicy = input.AffectPolicy.clone()
	output.State = cloneState(input.State)
	output.Evidence = append([]Evidence(nil), input.Evidence...)
	for index := range output.Evidence {
		output.Evidence[index].Content = string([]byte(output.Evidence[index].Content))
		output.Evidence[index].Text = string([]byte(output.Evidence[index].Text))
		output.Evidence[index].Excerpt = string([]byte(output.Evidence[index].Excerpt))
	}
	return output
}

func cloneState(input ReflectionState) ReflectionState {
	output := input
	output.Persona = clonePersona(input.Persona)
	output.Relationship = cloneRelationship(input.Relationship)
	output.Affect = cloneAffect(input.Affect)
	return output
}

func clonePersona(input MutablePersona) MutablePersona {
	output := input
	output.Traits = cloneFloatMap(input.Traits)
	output.PinnedTraits = append([]string(nil), input.PinnedTraits...)
	return output
}

func cloneRelationship(input RelationshipState) RelationshipState {
	output := input
	output.Dimensions = cloneFloatMap(input.Dimensions)
	output.Evidence = append([]domain.ID(nil), input.Evidence...)
	if input.Opinions != nil {
		output.Opinions = make([]SubjectiveOpinion, len(input.Opinions))
		for index, opinion := range input.Opinions {
			output.Opinions[index] = cloneOpinion(opinion)
		}
	}
	return output
}

func cloneOpinion(input SubjectiveOpinion) SubjectiveOpinion {
	output := input
	output.EvidenceIDs = append([]domain.ID(nil), input.EvidenceIDs...)
	output.Evidence = append([]domain.ID(nil), input.Evidence...)
	return output
}

func cloneAffect(input AffectiveState) AffectiveState {
	output := input
	output.Dimensions = cloneFloatMap(input.Dimensions)
	if input.DimensionUpdated != nil {
		output.DimensionUpdated = make(map[string]time.Time, len(input.DimensionUpdated))
		for key, value := range input.DimensionUpdated {
			output.DimensionUpdated[key] = value
		}
	}
	return output
}

func cloneFloatMap(input map[string]float64) map[string]float64 {
	if input == nil {
		return nil
	}
	output := make(map[string]float64, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
