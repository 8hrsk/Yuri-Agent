package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/executionbudget"
	"github.com/OrdoAI/yuri-agent/internal/memory"
	"github.com/OrdoAI/yuri-agent/internal/reflection"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func (b *Bridge) reflectOnTurn(ctx context.Context, backend agent.ModelBackend, model string, turn memory.Turn, agentIDs ...domain.ID) {
	agentID := firstNonEmptyID(agentIDs...)
	if agentID.Empty() {
		agentID = firstNonEmptyID(turn.AgentID, b.personaProfileID())
	}
	b.mu.Lock()
	enabled := b.config.Persona.AutoEvolution
	defaultCooldown := time.Duration(b.config.Persona.ReflectionCooldownMinutes) * time.Minute
	coordinator := b.reflectionRuns
	gate := b.reflectionGate
	if gate == nil {
		gate = make(chan struct{}, 1)
		b.reflectionGate = gate
	}
	b.mu.Unlock()
	if !enabled || backend == nil || strings.TrimSpace(model) == "" || len(turn.Messages) == 0 {
		return
	}
	// Reflection currently has one process-wide worker. Skip overlapping reviews
	// rather than queueing stale snapshots and spending model budget only to lose
	// the optimistic version race at persistence time. The captured agent ID still
	// keeps the accepted review inside its profile boundary.
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	default:
		return
	}
	state, domainState, evidence, err := b.reflectionSnapshot(ctx, turn, agentID)
	if err != nil {
		b.logReflectionFailure(ctx, turn.RunID, err)
		return
	}
	if !domainState.personalization.EvolutionPolicy.ReflectionEnabled(enabled) {
		return
	}
	cooldown := domainState.personalization.EvolutionPolicy.ReflectionCooldown(defaultCooldown)
	modelAnalyzer, err := reflection.NewModelAnalyzer(modelReflectionBackend{backend: backend, model: model})
	if err != nil {
		b.logReflectionFailure(ctx, turn.RunID, err)
		return
	}
	config := reflection.DefaultConfig()
	config.Analyzer = modelAnalyzer
	config.Coordinator = coordinator
	config.DurableStateCooldown = cooldown
	config.MaxDelta = .10
	config.MinimumEvidence = 1
	config.MinimumEvidenceWeight = .5
	config.Budget = reflectionBudgetForPolicy(domainState.personalization.EvolutionPolicy, reflection.ReflectionBudget{
		MaxDuration: 60 * time.Second, MaxTokens: 2_500, MaxInputBytes: 64 * 1024, MaxOutputBytes: 16 * 1024, MaxEvidence: 8,
	})
	config.Budget.MaxTokens = executionbudget.BoundTotalTokens(config.Budget.MaxTokens, modelExecutionLimits(backend, model))
	config.TraitRanges = rangesFor(state.Persona.Traits, 0, 1)
	config.RelationshipRanges = rangesFor(state.Relationship.Dimensions, 0, 1)
	config.AffectRanges = rangesFor(state.Affect.Dimensions, 0, 1)
	for name := range defaultAffectDimensions() {
		if _, exists := config.AffectRanges[name]; !exists {
			config.AffectRanges[name] = reflection.ValueRange{Min: 0, Max: 1}
		}
	}
	appraisalPolicy := reflection.NewAffectAppraisalPolicy(
		domainState.personalization.EmotionalDynamics,
		domainState.personalization.Temperament,
		sortedRangeKeys(config.AffectRanges),
	)
	config.AffectAppraisal = appraisalPolicy
	config.PinnedTraits = make(map[string]bool, len(state.Persona.PinnedTraits))
	for _, name := range state.Persona.PinnedTraits {
		config.PinnedTraits[name] = true
	}
	for _, field := range domainState.personalization.EvolutionPolicy.LockedFields {
		if name, ok := strings.CutPrefix(strings.TrimSpace(field), "temperament."); ok && name != "" {
			config.PinnedTraits[name] = true
		}
	}
	engine, err := reflection.New(config)
	if err != nil {
		b.logReflectionFailure(ctx, turn.RunID, err)
		return
	}
	profileID := agentID
	profile, err := b.repositories.Agents.Get(ctx, profileID)
	if err != nil {
		b.logReflectionFailure(ctx, turn.RunID, err)
		return
	}
	roster, err := b.repositories.Agents.ListIncludingDeleted(ctx)
	if err != nil {
		b.logReflectionFailure(ctx, turn.RunID, err)
		return
	}
	result, err := engine.Run(ctx, reflection.InputSnapshot{
		ProfileID: profileID, RunID: turn.RunID, Trigger: reflection.TriggerPostTurn,
		CapturedAt: turn.Now, ImmutablePolicy: immutablePolicySystemPrompt, IdentitySeed: agentIdentitySeed(profile, roster, domainState.personalization.Identity.PreferredLanguage),
		AffectPolicy: appraisalPolicy, State: state, Evidence: evidence,
	})
	if err != nil {
		// Guard rejections are expected safe no-ops. They are logged without
		// retrying the model or persisting a partial projection.
		b.logReflectionFailure(ctx, turn.RunID, err)
		return
	}
	if !result.Changed() && !result.CanPersistAffectDecay() {
		return
	}
	mutation, err := reflectionMutation(result, domainState, evidence, domainState.personalization.EvolutionPolicy)
	if err != nil {
		if errors.Is(err, domain.ErrNotPermitted) {
			return
		}
		b.logReflectionFailure(ctx, turn.RunID, err)
		return
	}
	if err := b.repositories.ApplyReflectionState(ctx, mutation); err != nil {
		b.logReflectionFailure(ctx, turn.RunID, err)
		return
	}
	if _, err := b.emitPersonalitySnapshot(ctx); err != nil && b.logger != nil && ctx.Err() == nil {
		b.logger.WarnContext(ctx, "emit personality snapshot", "run_id", turn.RunID, "error", safeError(err.Error()))
	}
	if b.logger != nil {
		b.logger.InfoContext(ctx, "post-turn reflection applied", "run_id", turn.RunID, "decision", result.Decision)
	}
}

func reflectionBudgetForPolicy(policy domain.PersonalizationEvolutionPolicy, fallback reflection.ReflectionBudget) reflection.ReflectionBudget {
	budget := fallback
	if policy.ReflectionMaxTokens > 0 {
		budget.MaxTokens = policy.ReflectionMaxTokens
	}
	if policy.ReflectionMaxDurationSecs > 0 {
		budget.MaxDuration = time.Duration(policy.ReflectionMaxDurationSecs) * time.Second
	}
	if policy.ReflectionMaxEvidence > 0 {
		budget.MaxEvidence = policy.ReflectionMaxEvidence
	}
	return budget
}

func sortedRangeKeys(values map[string]reflection.ValueRange) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type reflectionDomainState struct {
	persona         domain.MutablePersona
	relationship    domain.RelationshipState
	affect          domain.AffectiveState
	personalization domain.PersonalizationSeed
}

func (b *Bridge) reflectionSnapshot(ctx context.Context, turn memory.Turn, agentIDs ...domain.ID) (reflection.ReflectionState, reflectionDomainState, []reflection.Evidence, error) {
	profileID := firstNonEmptyID(agentIDs...)
	if profileID.Empty() {
		profileID = firstNonEmptyID(turn.AgentID, b.personaProfileID())
	}
	persona, err := b.repositories.Persona.Get(ctx, profileID)
	if err != nil {
		return reflection.ReflectionState{}, reflectionDomainState{}, nil, err
	}
	relationship, err := b.repositories.Relationship.Get(ctx, profileID)
	if err != nil {
		return reflection.ReflectionState{}, reflectionDomainState{}, nil, err
	}
	affect, err := b.repositories.Affect.Get(ctx, profileID)
	if err != nil {
		return reflection.ReflectionState{}, reflectionDomainState{}, nil, err
	}
	personalization, err := b.repositories.Personalization.Get(ctx, profileID)
	if err != nil {
		return reflection.ReflectionState{}, reflectionDomainState{}, nil, err
	}
	evidence := make([]reflection.Evidence, 0, len(turn.Messages))
	for index, message := range turn.Messages {
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		source := reflection.EvidenceSourceAssistant
		trust := reflection.EvidenceUntrusted
		weight := .5
		if message.Role == string(agent.RoleUser) {
			source, trust, weight = reflection.EvidenceSourceUser, reflection.EvidenceTrusted, 1
		}
		id := message.ID
		if id.Empty() {
			id = domain.ID(fmt.Sprintf("%s:evidence:%d", turn.RunID, index+1))
		}
		evidence = append(evidence, reflection.Evidence{
			ID: id, Source: source, SourceID: id, ConversationID: turn.ConversationID, MessageID: message.ID,
			Content: truncateRunes(message.Content, 4_000), Trust: trust, Weight: weight, Confidence: weight,
			OccurredAt: message.CreatedAt,
		})
	}
	state := reflection.ReflectionState{
		Version: maxUint64(persona.Version, relationship.Version, affect.Version),
		Persona: reflection.MutablePersona{Version: persona.Version, Traits: copyFloatMap(persona.Traits), Prompt: persona.Prompt(), PinnedTraits: append([]string(nil), persona.PinnedTraits...), UpdatedAt: persona.UpdatedAt},
		Relationship: reflection.RelationshipState{
			Version: relationship.Version, Dimensions: copyFloatMap(relationship.Dimensions), Summary: relationship.Summary,
			Opinions: reflectionOpinions(relationship.Opinions), UpdatedAt: relationship.UpdatedAt,
		},
		Affect:    reflection.AffectiveState{Version: affect.Version, Dimensions: copyFloatMap(affectValues(affect)), DimensionUpdated: dimensionTimes(affectValues(affect), affect.UpdatedAt), UpdatedAt: affect.UpdatedAt},
		UpdatedAt: latestTime(persona.UpdatedAt, relationship.UpdatedAt, affect.UpdatedAt),
	}
	for _, candidate := range []struct {
		version uint64
		runID   domain.ID
		at      time.Time
	}{{persona.Version, persona.AuthorRunID, persona.UpdatedAt}, {relationship.Version, relationship.AuthorRunID, relationship.UpdatedAt}, {affect.Version, affect.AuthorRunID, affect.UpdatedAt}} {
		if candidate.version > 1 && !candidate.runID.Empty() && candidate.at.After(state.LastReflectionAt) {
			state.LastReflectionAt = candidate.at
		}
	}
	for _, candidate := range []struct {
		version uint64
		runID   domain.ID
		at      time.Time
	}{{persona.Version, persona.AuthorRunID, persona.UpdatedAt}, {relationship.Version, relationship.AuthorRunID, relationship.UpdatedAt}} {
		if candidate.version > 1 && !candidate.runID.Empty() && candidate.at.After(state.LastDurableUpdateAt) {
			state.LastDurableUpdateAt = candidate.at
		}
	}
	return state, reflectionDomainState{persona: persona, relationship: relationship, affect: affect, personalization: personalization}, evidence, nil
}

func reflectionMutation(result reflection.ReflectionResult, current reflectionDomainState, evidence []reflection.Evidence, policies ...domain.PersonalizationEvolutionPolicy) (storage.ReflectionStateMutation, error) {
	policy := current.personalization.EvolutionPolicy
	if len(policies) > 0 {
		policy = policies[0]
	}
	if result.Proposal.Persona != nil && (looksLikeSecret(result.Proposal.Persona.Prompt) || looksLikeSecret(result.Proposal.Persona.PromptDelta)) {
		return storage.ReflectionStateMutation{}, errors.New("reflection persona proposal contains secret-like material")
	}
	if result.Proposal.Relationship != nil {
		for _, opinion := range result.Proposal.Relationship.Opinions {
			if looksLikeSecret(opinion.Claim) {
				return storage.ReflectionStateMutation{}, errors.New("reflection opinion contains secret-like material")
			}
		}
	}
	links := reflectionEvidenceLinks(evidence, result.RunID, result.FinishedAt)
	for _, value := range current.relationship.Evidence {
		addEvidenceLink(links, value)
	}
	for _, opinion := range current.relationship.Opinions {
		for _, value := range opinion.Evidence {
			addEvidenceLink(links, value)
		}
	}
	mutation := storage.ReflectionStateMutation{}
	if result.Proposal.Persona != nil && !policy.FieldLocked("mutable_persona") {
		next := current.persona
		next.Version = result.State.Persona.Version
		next.RevisionID = ""
		next.ParentID = current.persona.RevisionID
		next.ParentVersion = current.persona.Version
		next.Operation = domain.PersonaOperationUpdate
		next.Traits = copyFloatMap(result.State.Persona.Traits)
		next.IdentityPrompt, next.PromptText = strings.TrimSpace(result.State.Persona.Prompt), ""
		next.PinnedTraits = append([]string(nil), result.State.Persona.PinnedTraits...)
		next.Diff = next.DeltaFrom(current.persona)
		next.Reason = firstNonEmpty(result.Proposal.Persona.Reason, result.Proposal.Reason)
		next.Evidence = selectedEvidenceLinks(links, proposalPersonaEvidenceIDs(result.Proposal))
		next.AuthorRunID, next.UpdatedAt = result.RunID, result.FinishedAt
		mutation.Persona, mutation.ExpectedPersona = &next, current.persona.Version
	}
	if result.Proposal.Relationship != nil && !policy.FieldLocked("relationship") {
		next := current.relationship
		next.Version = result.State.Relationship.Version
		next.RevisionID = ""
		next.ParentID = current.relationship.RevisionID
		next.ParentVersion = current.relationship.Version
		next.Operation = domain.RelationshipOperationUpdate
		next.Dimensions = copyFloatMap(result.State.Relationship.Dimensions)
		next.Opinions = domainOpinions(result.State.Relationship.Opinions, links, result.RunID)
		next.Reason = firstNonEmpty(result.Proposal.Relationship.Reason, result.Proposal.Reason)
		next.Evidence = selectedEvidenceLinks(links, proposalRelationshipEvidenceIDs(result.Proposal))
		next.AuthorRunID, next.UpdatedAt = result.RunID, result.FinishedAt
		mutation.Relationship, mutation.ExpectedRelationship = &next, current.relationship.Version
	}
	if (result.Proposal.Affect != nil || result.CanPersistAffectDecay()) && !policy.FieldLocked("affect") {
		next := current.affect
		next.Version = current.affect.Version + 1
		next.RevisionID = ""
		next.ParentID = current.affect.RevisionID
		next.ParentVersion = current.affect.Version
		next.Operation = domain.AffectOperationUpdate
		next.Emotions, next.Dimensions, next.Values = copyFloatMap(result.State.Affect.Dimensions), nil, nil
		next.Reason = "Детерминированное затухание эмоционального состояния"
		if result.Proposal.Affect != nil {
			next.Reason = firstNonEmpty(result.Proposal.Affect.Reason, result.Proposal.Reason, next.Reason)
		}
		next.AuthorRunID, next.AsOf, next.UpdatedAt = result.RunID, result.FinishedAt, result.FinishedAt
		mutation.Affect, mutation.ExpectedAffect = &next, current.affect.Version
		if result.Proposal.Affect != nil {
			affectEvidence := selectedEvidenceLinks(links, proposalAffectEvidenceIDs(result.Proposal))
			applied := result.AppliedAffectDeltas
			if len(applied) == 0 {
				applied = result.Proposal.Affect.Dimensions
			}
			for _, name := range sortedFloatKeys(applied) {
				delta := applied[name]
				if delta == 0 {
					continue
				}
				sum := sha256.Sum256([]byte(string(result.RunID) + "\x00" + name))
				valence := 1.0
				if delta < 0 {
					valence = -1
				}
				halfLifeSeconds := result.AffectHalfLifeSeconds[name]
				if halfLifeSeconds <= 0 {
					halfLifeSeconds = int64((7 * 24 * time.Hour).Seconds())
				}
				metadata, _ := json.Marshal(map[string]any{
					"appraisal": "bounded_v1", "raw_delta": result.Proposal.Affect.Dimensions[name],
					"applied_delta": delta, "half_life_seconds": halfLifeSeconds,
				})
				mutation.AffectEvents = append(mutation.AffectEvents, domain.AffectiveEvent{
					ID: domain.ID("affect-event-" + hex.EncodeToString(sum[:8])), AffectID: current.affect.ID,
					SourceID: result.RunID, SourceType: "post_turn_reflection", RunID: result.RunID,
					Emotion: name, Intensity: math.Abs(delta), Valence: valence,
					DecayPolicy: domain.AffectiveDecayExponential, HalfLifeSeconds: halfLifeSeconds,
					Provenance: "reflection_appraisal", Evidence: affectEvidence, MetadataJSON: string(metadata), CreatedAt: result.FinishedAt,
				})
			}
		}
	}
	if mutation.Persona == nil && mutation.Relationship == nil && mutation.Affect == nil {
		return storage.ReflectionStateMutation{}, domain.ErrNotPermitted
	}
	return mutation, nil
}

func reflectionOpinions(values []domain.RelationshipOpinion) []reflection.SubjectiveOpinion {
	result := make([]reflection.SubjectiveOpinion, 0, len(values))
	for _, value := range values {
		label := reflection.OpinionLabelOpinion
		if value.Label == "inference" || strings.Contains(strings.ToLower(value.Provenance), "inference") {
			label = reflection.OpinionLabelInference
		}
		ids := evidenceLinkIDs(value.Evidence)
		if len(ids) == 0 {
			continue
		}
		result = append(result, reflection.SubjectiveOpinion{ID: value.ID, Subject: value.Subject, Topic: value.Topic, Claim: value.Text(), Label: label, Confidence: value.Confidence, Reason: firstNonEmpty(value.Topic, "Субъективный вывод Yuri"), EvidenceIDs: ids, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt})
	}
	return result
}

func domainOpinions(values []reflection.SubjectiveOpinion, links map[domain.ID]domain.EvidenceLink, runID domain.ID) []domain.RelationshipOpinion {
	result := make([]domain.RelationshipOpinion, 0, len(values))
	for _, value := range values {
		evidence := selectedEvidenceLinks(links, append(append([]domain.ID(nil), value.EvidenceIDs...), value.Evidence...))
		if len(evidence) == 0 {
			continue
		}
		id := value.ID
		if id.Empty() {
			sum := sha256.Sum256([]byte(strings.ToLower(value.Subject + "\x00" + value.Topic + "\x00" + string(value.Label))))
			id = domain.ID("opinion-" + hex.EncodeToString(sum[:8]))
		}
		createdAt := value.CreatedAt
		if createdAt.IsZero() {
			createdAt = value.UpdatedAt
		}
		result = append(result, domain.RelationshipOpinion{ID: id, Subject: value.Subject, Topic: value.Topic, Label: string(value.Label), Claim: value.Claim, Confidence: value.Confidence, Evidence: evidence, Provenance: "reflection:run:" + string(runID), CreatedAt: createdAt, UpdatedAt: value.UpdatedAt})
	}
	return result
}

func reflectionEvidenceLinks(evidence []reflection.Evidence, runID domain.ID, at time.Time) map[domain.ID]domain.EvidenceLink {
	result := make(map[domain.ID]domain.EvidenceLink, len(evidence))
	for _, item := range evidence {
		sum := sha256.Sum256([]byte(item.Data()))
		result[item.ID] = domain.EvidenceLink{ID: item.ID, SourceType: string(item.Source), SourceID: item.SourceID, RunID: runID, ConversationID: item.ConversationID, MessageID: item.MessageID, ExcerptHash: "sha256:" + hex.EncodeToString(sum[:]), Provenance: "post_turn_reflection", UserConfirmed: item.UserConfirmed, CreatedAt: at}
	}
	return result
}

func addEvidenceLink(target map[domain.ID]domain.EvidenceLink, value domain.EvidenceLink) {
	id := firstDomainID(value.ID, value.MessageID, value.SourceID)
	if id.Empty() {
		return
	}
	if value.ID.Empty() {
		value.ID = id
	}
	target[id] = value
}

func selectedEvidenceLinks(links map[domain.ID]domain.EvidenceLink, ids []domain.ID) []domain.EvidenceLink {
	result := make([]domain.EvidenceLink, 0, len(ids))
	seen := make(map[domain.ID]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		if link, ok := links[id]; ok {
			result = append(result, link)
			seen[id] = true
		}
	}
	return result
}

func proposalPersonaEvidenceIDs(proposal reflection.ReflectionProposal) []domain.ID {
	if proposal.Persona != nil && len(proposal.Persona.EvidenceIDs)+len(proposal.Persona.Evidence) > 0 {
		return append(append([]domain.ID(nil), proposal.Persona.EvidenceIDs...), proposal.Persona.Evidence...)
	}
	return append(append([]domain.ID(nil), proposal.EvidenceIDs...), proposal.Evidence...)
}

func proposalRelationshipEvidenceIDs(proposal reflection.ReflectionProposal) []domain.ID {
	if proposal.Relationship != nil && len(proposal.Relationship.EvidenceIDs)+len(proposal.Relationship.Evidence) > 0 {
		return append(append([]domain.ID(nil), proposal.Relationship.EvidenceIDs...), proposal.Relationship.Evidence...)
	}
	return append(append([]domain.ID(nil), proposal.EvidenceIDs...), proposal.Evidence...)
}

func proposalAffectEvidenceIDs(proposal reflection.ReflectionProposal) []domain.ID {
	if proposal.Affect != nil && len(proposal.Affect.EvidenceIDs)+len(proposal.Affect.Evidence) > 0 {
		return append(append([]domain.ID(nil), proposal.Affect.EvidenceIDs...), proposal.Affect.Evidence...)
	}
	return append(append([]domain.ID(nil), proposal.EvidenceIDs...), proposal.Evidence...)
}

func rangesFor(values map[string]float64, minimum, maximum float64) map[string]reflection.ValueRange {
	result := make(map[string]reflection.ValueRange, len(values))
	for name := range values {
		result[name] = reflection.ValueRange{Min: minimum, Max: maximum}
	}
	return result
}

func affectValues(value domain.AffectiveState) map[string]float64 {
	if value.Emotions != nil {
		return value.Emotions
	}
	if value.Dimensions != nil {
		return value.Dimensions
	}
	return value.Values
}

func evidenceLinkIDs(values []domain.EvidenceLink) []domain.ID {
	result := make([]domain.ID, 0, len(values))
	for _, value := range values {
		id := firstDomainID(value.ID, value.MessageID, value.SourceID)
		if !id.Empty() {
			result = append(result, id)
		}
	}
	return result
}

func dimensionTimes(values map[string]float64, at time.Time) map[string]time.Time {
	result := make(map[string]time.Time, len(values))
	for name := range values {
		result[name] = at
	}
	return result
}

func copyFloatMap(values map[string]float64) map[string]float64 {
	result := make(map[string]float64, len(values))
	for name, value := range values {
		result[name] = value
	}
	return result
}

func latestTime(values ...time.Time) time.Time {
	var result time.Time
	for _, value := range values {
		if value.After(result) {
			result = value
		}
	}
	return result
}

func maxUint64(values ...uint64) uint64 {
	var result uint64
	for _, value := range values {
		if value > result {
			result = value
		}
	}
	return result
}

func (b *Bridge) logReflectionFailure(ctx context.Context, runID domain.ID, err error) {
	if b.logger != nil && ctx.Err() == nil {
		b.logger.WarnContext(ctx, "post-turn reflection skipped", "run_id", runID, "error", safeError(err.Error()))
	}
}
