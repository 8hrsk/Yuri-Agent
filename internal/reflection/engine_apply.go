package reflection

import (
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func (e *Engine) applyProposal(input InputSnapshot, base ReflectionState, proposal ReflectionProposal, now time.Time) (ReflectionState, Decision, error) {
	if err := proposal.Validate(); err != nil {
		return base, "", err
	}
	next := cloneState(base)
	for _, target := range []struct {
		name string
		ids  []domain.ID
		ok   bool
	}{
		{name: "relationship", ids: proposalRelationshipEvidence(proposal), ok: proposal.Relationship != nil && len(proposal.Relationship.Dimensions) > 0},
		{name: "affect", ids: proposalAffectEvidence(proposal), ok: proposal.Affect != nil},
		{name: "persona", ids: proposalPersonaEvidence(proposal), ok: proposal.Persona != nil},
	} {
		if !target.ok {
			continue
		}
		if err := e.validateEvidence(input.Evidence, target.ids, target.name); err != nil {
			return base, decisionForError(err), err
		}
	}
	if proposal.Relationship != nil {
		for index, opinion := range proposal.Relationship.Opinions {
			ids := proposalOpinionEvidence(proposal, opinion)
			if err := e.validateEvidence(input.Evidence, ids, fmt.Sprintf("relationship opinion %d", index)); err != nil {
				return base, decisionForError(err), err
			}
			if len([]byte(strings.TrimSpace(opinion.Claim))) > e.config.MaxOpinionContentBytes {
				return base, decisionForError(ErrOpinionLimit), fmt.Errorf("%w: relationship opinion %d claim exceeds %d bytes", ErrOpinionLimit, index, e.config.MaxOpinionContentBytes)
			}
		}
	}
	if proposal.Relationship != nil {
		if err := e.validateScalarDelta(proposal.Relationship.Dimensions, next.Relationship.Dimensions, e.config.RelationshipRanges, "relationship", defaultDimensionRange()); err != nil {
			return base, decisionForError(err), err
		}
	}
	if proposal.Affect != nil {
		if err := e.validateScalarDelta(proposal.Affect.Dimensions, next.Affect.Dimensions, e.config.AffectRanges, "affect", defaultDimensionRange()); err != nil {
			return base, decisionForError(err), err
		}
	}
	if proposal.Persona != nil {
		if err := e.validatePersonaDelta(next.Persona, proposal.Persona); err != nil {
			return base, decisionForError(err), err
		}
	}
	if proposal.Relationship != nil {
		for _, name := range sortedKeys(proposal.Relationship.Dimensions) {
			next.Relationship.Dimensions = ensureFloatMap(next.Relationship.Dimensions)
			next.Relationship.Dimensions[name] += proposal.Relationship.Dimensions[name]
		}
		if len(proposal.Relationship.Opinions) > 0 {
			deltas := append([]OpinionDelta(nil), proposal.Relationship.Opinions...)
			for index := range deltas {
				// Store the resolved provenance on the state record even when the
				// proposal inherited it from relationship or top-level evidence.
				deltas[index].EvidenceIDs = proposalOpinionEvidence(proposal, deltas[index])
				deltas[index].Evidence = nil
			}
			opinions, err := e.applyOpinionDeltas(next.Relationship.Opinions, deltas, now)
			if err != nil {
				return base, decisionForError(err), err
			}
			next.Relationship.Opinions = opinions
		}
		next.Relationship.Version++
		if len(proposal.Relationship.Dimensions) > 0 {
			next.Relationship.Evidence = appendUniqueIDs(next.Relationship.Evidence, proposalRelationshipEvidence(proposal))
		}
		next.Relationship.UpdatedAt = now
	}
	if proposal.Affect != nil {
		for _, name := range sortedKeys(proposal.Affect.Dimensions) {
			next.Affect.Dimensions = ensureFloatMap(next.Affect.Dimensions)
			next.Affect.Dimensions[name] += proposal.Affect.Dimensions[name]
			if next.Affect.DimensionUpdated == nil {
				next.Affect.DimensionUpdated = make(map[string]time.Time)
			}
			next.Affect.DimensionUpdated[name] = now
		}
		next.Affect.Version++
		next.Affect.UpdatedAt = now
	}
	if proposal.Persona != nil {
		for _, name := range sortedKeys(proposal.Persona.Traits) {
			next.Persona.Traits = ensureFloatMap(next.Persona.Traits)
			next.Persona.Traits[name] += proposal.Persona.Traits[name]
		}
		if strings.TrimSpace(proposal.Persona.Prompt) != "" {
			next.Persona.Prompt = strings.TrimSpace(proposal.Persona.Prompt)
		} else if strings.TrimSpace(proposal.Persona.PromptDelta) != "" {
			patch := strings.TrimSpace(proposal.Persona.PromptDelta)
			if strings.TrimSpace(next.Persona.Prompt) == "" {
				next.Persona.Prompt = patch
			} else {
				next.Persona.Prompt = strings.TrimSpace(next.Persona.Prompt) + "\n" + patch
			}
		}
		next.Persona.Version++
		next.Persona.UpdatedAt = now
	}
	next.Version++
	next.LastReflectionAt = now
	next.UpdatedAt = now
	return next, DecisionApplied, nil
}

func (e *Engine) validateEvidence(snapshot []Evidence, ids []domain.ID, target string) error {
	if len(ids) < e.config.MinimumEvidence {
		return fmt.Errorf("%w: %s has %d evidence references, needs %d", ErrInsufficientEvidence, target, len(ids), e.config.MinimumEvidence)
	}
	byID := make(map[domain.ID]Evidence, len(snapshot))
	for _, evidence := range snapshot {
		byID[evidence.ID] = evidence
	}
	weight := 0.0
	for _, id := range ids {
		evidence, ok := byID[id]
		if !ok {
			return fmt.Errorf("%w: %s references unknown evidence %s", ErrInvalidProposal, target, id)
		}
		weight += evidence.Weight
		if target == "persona" && !evidence.AllowsPersonaMutation() {
			return fmt.Errorf("%w: %s evidence %s is external or unconfirmed", ErrUntrustedEvidence, target, id)
		}
	}
	if e.config.MinimumEvidenceWeight > 0 && weight < e.config.MinimumEvidenceWeight {
		return fmt.Errorf("%w: %s evidence weight %.3f is below %.3f", ErrInsufficientEvidence, target, weight, e.config.MinimumEvidenceWeight)
	}
	return nil
}
