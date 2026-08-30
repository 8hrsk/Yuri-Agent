package reflection

import (
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func proposalRelationshipEvidence(proposal ReflectionProposal) []domain.ID {
	if proposal.Relationship != nil {
		if ids := deltaEvidence(proposal.Relationship.EvidenceIDs, proposal.Relationship.Evidence); len(ids) > 0 {
			return ids
		}
	}
	return proposalEvidence(proposal.EvidenceIDs, proposal.Evidence)
}

func proposalOpinionEvidence(proposal ReflectionProposal, opinion OpinionDelta) []domain.ID {
	if ids := deltaEvidence(opinion.EvidenceIDs, opinion.Evidence); len(ids) > 0 {
		return ids
	}
	return proposalRelationshipEvidence(proposal)
}

func proposalAffectEvidence(proposal ReflectionProposal) []domain.ID {
	if proposal.Affect != nil {
		if ids := deltaEvidence(proposal.Affect.EvidenceIDs, proposal.Affect.Evidence); len(ids) > 0 {
			return ids
		}
	}
	return proposalEvidence(proposal.EvidenceIDs, proposal.Evidence)
}

func proposalPersonaEvidence(proposal ReflectionProposal) []domain.ID {
	if proposal.Persona != nil {
		if ids := deltaEvidence(proposal.Persona.EvidenceIDs, proposal.Persona.Evidence); len(ids) > 0 {
			return ids
		}
	}
	return proposalEvidence(proposal.EvidenceIDs, proposal.Evidence)
}

func deltaEvidence(first, second []domain.ID) []domain.ID { return proposalEvidence(first, second) }

func proposalEvidence(first, second []domain.ID) []domain.ID {
	ids := make([]domain.ID, 0, len(first)+len(second))
	ids = append(ids, first...)
	ids = append(ids, second...)
	return ids
}

func appendUniqueIDs(existing, additions []domain.ID) []domain.ID {
	seen := make(map[domain.ID]struct{}, len(existing)+len(additions))
	for _, id := range existing {
		seen[id] = struct{}{}
	}
	for _, id := range additions {
		if _, ok := seen[id]; ok {
			continue
		}
		existing = append(existing, id)
		seen[id] = struct{}{}
	}
	return existing
}
