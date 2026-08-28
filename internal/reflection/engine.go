package reflection

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

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// Engine validates and bounds one reflection proposal. It has no storage
// dependency: callers receive a next-state projection and decide how to
// append the corresponding relationship/affect/persona versions atomically.
type Engine struct {
	analyzer    Analyzer
	coordinator *Coordinator
	clock       domain.Clock
	config      Config
}

// New constructs a reflection engine and normalizes zero-valued optional
// settings to conservative defaults.
func New(config Config) (*Engine, error) {
	if config.Analyzer == nil {
		return nil, ErrNoAnalyzer
	}
	if config.Clock == nil {
		config.Clock = domain.SystemClock{}
	}
	if !config.Budget.Valid() {
		return nil, fmt.Errorf("%w: invalid reflection budget", ErrInvalidSnapshot)
	}
	config.Budget = config.Budget.normalize()
	if config.MaxDelta < 0 || !finite(config.MaxDelta) {
		return nil, fmt.Errorf("%w: max delta must be non-negative and finite", ErrInvalidSnapshot)
	}
	if config.MaxDelta == 0 {
		config.MaxDelta = DefaultConfig().MaxDelta
	}
	if config.MinimumEvidence < 0 || config.MinimumEvidenceWeight < 0 || !finite(config.MinimumEvidenceWeight) {
		return nil, fmt.Errorf("%w: evidence thresholds must be non-negative", ErrInvalidSnapshot)
	}
	if config.MinimumEvidence == 0 {
		config.MinimumEvidence = 1
	}
	if config.Cooldown < 0 {
		return nil, fmt.Errorf("%w: cooldown cannot be negative", ErrInvalidSnapshot)
	}
	if config.AffectDecay.HalfLife == 0 && len(config.AffectDecay.DimensionHalfLives) == 0 {
		config.AffectDecay = DefaultDecayPolicy()
	}
	if !config.AffectDecay.Valid() {
		return nil, fmt.Errorf("%w: invalid affect decay policy", ErrInvalidSnapshot)
	}
	config.AffectDecay = config.AffectDecay.normalize()
	if config.MaxPromptBytes <= 0 {
		config.MaxPromptBytes = DefaultConfig().MaxPromptBytes
	}
	if config.MaxOpinions <= 0 {
		config.MaxOpinions = DefaultConfig().MaxOpinions
	}
	if config.MaxOpinions > maxSubjectiveOpinions {
		return nil, fmt.Errorf("%w: max opinions cannot exceed %d", ErrInvalidSnapshot, maxSubjectiveOpinions)
	}
	if config.MaxOpinionContentBytes <= 0 {
		config.MaxOpinionContentBytes = DefaultConfig().MaxOpinionContentBytes
	}
	if config.MaxOpinionContentBytes > maxSubjectiveOpinionContentBytes {
		return nil, fmt.Errorf("%w: max opinion content cannot exceed %d bytes", ErrInvalidSnapshot, maxSubjectiveOpinionContentBytes)
	}
	if err := validateRanges(config.TraitRanges, "trait ranges"); err != nil {
		return nil, err
	}
	if err := validateRanges(config.RelationshipRanges, "relationship ranges"); err != nil {
		return nil, err
	}
	if err := validateRanges(config.AffectRanges, "affect ranges"); err != nil {
		return nil, err
	}
	for name, limit := range config.MaxDeltaByTrait {
		if validateName(name) != nil || !finite(limit) || limit < 0 {
			return nil, fmt.Errorf("%w: invalid max delta override for %q", ErrInvalidSnapshot, name)
		}
	}
	for name, pinned := range config.PinnedTraits {
		if pinned && validateName(name) != nil {
			return nil, fmt.Errorf("%w: invalid pinned trait %q", ErrInvalidSnapshot, name)
		}
	}
	config.TraitRanges = cloneRanges(config.TraitRanges)
	config.RelationshipRanges = cloneRanges(config.RelationshipRanges)
	config.AffectRanges = cloneRanges(config.AffectRanges)
	config.MaxDeltaByTrait = cloneLowerFloatMap(config.MaxDeltaByTrait)
	config.PinnedTraits = cloneBoolMap(config.PinnedTraits)
	if config.Coordinator == nil {
		config.Coordinator = NewCoordinator()
	}
	return &Engine{analyzer: config.Analyzer, coordinator: config.Coordinator, clock: config.Clock, config: config}, nil
}

// NewEngine is an explicit constructor alias matching the other internal
// services in this repository.
func NewEngine(config Config) (*Engine, error) { return New(config) }

// Config returns an immutable copy of the normalized engine configuration.
// Maps are cloned so callers cannot race with a running reflection.
func (e *Engine) Config() Config {
	if e == nil {
		return Config{}
	}
	config := e.config
	config.TraitRanges = cloneRanges(config.TraitRanges)
	config.RelationshipRanges = cloneRanges(config.RelationshipRanges)
	config.AffectRanges = cloneRanges(config.AffectRanges)
	config.MaxDeltaByTrait = cloneLowerFloatMap(config.MaxDeltaByTrait)
	config.PinnedTraits = cloneBoolMap(config.PinnedTraits)
	return config
}

// Run performs one bounded reflection analysis. The profile coordinator is
// acquired before the analyzer call and released on every path, including
// cancellation and analyzer failure.
func (e *Engine) Run(ctx context.Context, snapshot InputSnapshot) (ReflectionResult, error) {
	return e.run(ctx, snapshot, false)
}

// TryRun is the non-blocking Engine variant. It is useful for idle/cron
// dispatchers that would rather coalesce a trigger than queue stale input.
func (e *Engine) TryRun(ctx context.Context, snapshot InputSnapshot) (ReflectionResult, error) {
	return e.run(ctx, snapshot, true)
}

func (e *Engine) run(ctx context.Context, snapshot InputSnapshot, try bool) (ReflectionResult, error) {
	if e == nil || e.analyzer == nil {
		return ReflectionResult{}, ErrNoAnalyzer
	}
	if err := ContextError(ctx); err != nil {
		return ReflectionResult{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return ReflectionResult{}, err
	}
	inputSize, err := snapshotSize(snapshot)
	if err != nil {
		return ReflectionResult{}, fmt.Errorf("%w: snapshot size: %v", ErrInvalidSnapshot, err)
	}
	if inputSize > e.config.Budget.MaxInputBytes {
		return ReflectionResult{}, fmt.Errorf("%w: input size %d exceeds %d bytes", ErrBudgetExceeded, inputSize, e.config.Budget.MaxInputBytes)
	}
	if len(snapshot.Evidence) > e.config.Budget.MaxEvidence {
		return ReflectionResult{}, fmt.Errorf("%w: evidence count %d exceeds %d", ErrBudgetExceeded, len(snapshot.Evidence), e.config.Budget.MaxEvidence)
	}
	started := e.now()
	if started.IsZero() {
		started = snapshot.CapturedAt.UTC()
	}
	runCtx, cancel := context.WithTimeout(ctx, e.config.Budget.MaxDuration)
	defer cancel()
	callback := func(owned context.Context) (ReflectionResult, error) {
		return e.runOwned(owned, snapshot, started)
	}
	if try {
		return e.coordinator.TryDo(runCtx, snapshot.ProfileID, callback)
	}
	return e.coordinator.Do(runCtx, snapshot.ProfileID, callback)
}

func (e *Engine) runOwned(ctx context.Context, input InputSnapshot, started time.Time) (ReflectionResult, error) {
	if err := ContextError(ctx); err != nil {
		return ReflectionResult{}, err
	}
	now := e.now()
	if now.IsZero() {
		now = input.CapturedAt
	}
	now = now.UTC()
	base := cloneState(input.State)
	decayed, err := DecayAffect(base.Affect, now, e.config.AffectDecay)
	if err != nil {
		return ReflectionResult{}, err
	}
	affectDecayChanged := affectStateChanged(base.Affect, decayed)
	base.Affect = decayed
	if e.cooldownActive(base.LastReflectionAt, now) {
		return e.noChangeResult(input, base, started, now, DecisionCooldown, "reflection cooldown is active", affectDecayChanged), nil
	}
	request := AnalysisRequest{
		Snapshot:     cloneSnapshot(input),
		Budget:       e.config.Budget,
		OutputSchema: ProposalSchema(),
	}
	// The decayed state is the state the analyzer should reason about, but it
	// remains a copy and is never written back through the input snapshot. The
	// result carries an explicit change bit so an adapter can persist it even
	// when the analyzer returns no_change or changes another target only.
	request.Snapshot.State = cloneState(base)
	response, err := e.analyzer.Analyze(ctx, request)
	if err != nil {
		if contextErr := ContextError(ctx); contextErr != nil {
			return ReflectionResult{}, contextErr
		}
		return ReflectionResult{}, err
	}
	if err := ContextError(ctx); err != nil {
		return ReflectionResult{}, err
	}
	if !response.Usage.Valid() {
		return ReflectionResult{}, fmt.Errorf("%w: invalid usage accounting", ErrInvalidProposal)
	}
	proposal, rawSize, err := responseProposal(response)
	if err != nil {
		return ReflectionResult{}, err
	}
	if rawSize > e.config.Budget.MaxOutputBytes {
		return ReflectionResult{}, fmt.Errorf("%w: output size %d exceeds %d bytes", ErrBudgetExceeded, rawSize, e.config.Budget.MaxOutputBytes)
	}
	usage := response.Usage
	if usage.OutputBytes == 0 {
		usage.OutputBytes = rawSize
	}
	if usage.OutputBytes > e.config.Budget.MaxOutputBytes {
		return ReflectionResult{}, fmt.Errorf("%w: reported output size %d exceeds %d bytes", ErrBudgetExceeded, usage.OutputBytes, e.config.Budget.MaxOutputBytes)
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	if usage.TotalTokens > e.config.Budget.MaxTokens {
		return ReflectionResult{}, fmt.Errorf("%w: token usage %d exceeds %d", ErrBudgetExceeded, usage.TotalTokens, e.config.Budget.MaxTokens)
	}
	if proposal.Outcome == OutcomeNoChange {
		return ReflectionResult{
			ProfileID: input.ProfileID, RunID: input.RunID, Outcome: OutcomeNoChange,
			Decision: DecisionNoChange, Proposal: proposal, State: base,
			AffectDecayChanged: affectDecayChanged, Usage: usage,
			StartedAt: started, FinishedAt: e.finished(started),
		}, nil
	}
	next, decision, guardErr := e.applyProposal(input, base, proposal, now)
	if guardErr != nil {
		return ReflectionResult{
			ProfileID: input.ProfileID, RunID: input.RunID, Outcome: OutcomeNoChange,
			Decision: decision, Proposal: proposal, State: base,
			AffectDecayChanged: affectDecayChanged, Usage: usage,
			StartedAt: started, FinishedAt: e.finished(started),
		}, guardErr
	}
	return ReflectionResult{
		ProfileID: input.ProfileID, RunID: input.RunID, Outcome: OutcomeChanged,
		Decision: DecisionApplied, Proposal: proposal, State: next,
		AffectDecayChanged: affectDecayChanged, Usage: usage,
		StartedAt: started, FinishedAt: e.finished(started),
	}, nil
}

func (e *Engine) noChangeResult(input InputSnapshot, state ReflectionState, started, finished time.Time, decision Decision, reason string, affectDecayChanged bool) ReflectionResult {
	return ReflectionResult{
		ProfileID: input.ProfileID, RunID: input.RunID, Outcome: OutcomeNoChange,
		Decision: decision,
		Proposal: ReflectionProposal{Outcome: OutcomeNoChange, Reason: reason},
		State:    state, AffectDecayChanged: affectDecayChanged,
		StartedAt: started, FinishedAt: e.finished(started, finished),
	}
}

func (e *Engine) cooldownActive(last, now time.Time) bool {
	return e.config.Cooldown > 0 && !last.IsZero() && now.Before(last.Add(e.config.Cooldown))
}

func (e *Engine) now() time.Time {
	if e == nil || e.clock == nil {
		return time.Now().UTC()
	}
	return e.clock.Now().UTC()
}

func (e *Engine) finished(started time.Time, values ...time.Time) time.Time {
	finished := e.now()
	for _, candidate := range values {
		if !candidate.IsZero() {
			finished = candidate.UTC()
			break
		}
	}
	if finished.Before(started) {
		return started
	}
	return finished
}

func snapshotSize(snapshot InputSnapshot) (int, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return 0, err
	}
	return len(encoded), nil
}

func responseProposal(response AnalysisResponse) (ReflectionProposal, int, error) {
	if len(strings.TrimSpace(string(response.Raw))) > 0 {
		proposal, err := DecodeProposal(response.Raw)
		if err != nil {
			return ReflectionProposal{}, len(response.Raw), err
		}
		return proposal, len(response.Raw), nil
	}
	proposal := response.Proposal
	if err := proposal.Validate(); err != nil {
		return ReflectionProposal{}, 0, err
	}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		return ReflectionProposal{}, 0, fmt.Errorf("%w: encode proposal: %v", ErrInvalidProposal, err)
	}
	return proposal, len(encoded), nil
}

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

func (e *Engine) applyOpinionDeltas(existing []SubjectiveOpinion, deltas []OpinionDelta, now time.Time) ([]SubjectiveOpinion, error) {
	if len(deltas) == 0 {
		return append([]SubjectiveOpinion(nil), existing...), nil
	}
	result := make([]SubjectiveOpinion, len(existing))
	for index, opinion := range existing {
		result[index] = cloneOpinion(opinion)
	}
	for _, delta := range deltas {
		key := opinionKey(delta.Subject, delta.Topic, delta.Label)
		index := -1
		for candidate := range result {
			if result[candidate].Key() == key {
				index = candidate
				break
			}
		}
		if index < 0 && !delta.ID.Empty() {
			for candidate := range result {
				if result[candidate].ID == delta.ID {
					index = candidate
					break
				}
			}
		}
		var next SubjectiveOpinion
		if index >= 0 {
			next = result[index]
		} else {
			next.ID = delta.ID
			if next.ID.Empty() {
				next.ID = deterministicOpinionID(key)
			}
			next.CreatedAt = now
		}
		next.Subject = strings.TrimSpace(delta.Subject)
		next.Topic = strings.TrimSpace(delta.Topic)
		next.Claim = strings.TrimSpace(delta.Claim)
		next.Label = delta.Label
		next.Confidence = delta.Confidence
		next.Reason = strings.TrimSpace(delta.Reason)
		next.EvidenceIDs = sortedOpinionEvidence(delta.EvidenceIDs, delta.Evidence)
		next.Evidence = nil
		next.UpdatedAt = now
		if next.CreatedAt.IsZero() {
			next.CreatedAt = now
		}
		if index >= 0 {
			result[index] = next
		} else {
			result = append(result, next)
		}
		result = deduplicateOpinions(result)
	}
	if len(result) > e.config.MaxOpinions {
		return nil, fmt.Errorf("%w: relationship has %d opinions, maximum is %d", ErrOpinionLimit, len(result), e.config.MaxOpinions)
	}
	sortOpinions(result)
	return result, nil
}

func deterministicOpinionID(key string) domain.ID {
	digest := sha256.Sum256([]byte(key))
	return domain.ID("opinion-" + hex.EncodeToString(digest[:12]))
}

func sortedOpinionEvidence(first, second []domain.ID) []domain.ID {
	ids := make([]domain.ID, 0, len(first)+len(second))
	ids = append(ids, first...)
	ids = append(ids, second...)
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	return ids
}

func deduplicateOpinions(values []SubjectiveOpinion) []SubjectiveOpinion {
	seenKeys := make(map[string]struct{}, len(values))
	seenIDs := make(map[domain.ID]struct{}, len(values))
	// Walk backwards so the latest delta wins for both canonical key and
	// explicit ID, then restore the original order for deterministic sorting.
	reversed := make([]SubjectiveOpinion, 0, len(values))
	for index := len(values) - 1; index >= 0; index-- {
		value := values[index]
		if _, exists := seenKeys[value.Key()]; exists {
			continue
		}
		if !value.ID.Empty() {
			if _, exists := seenIDs[value.ID]; exists {
				continue
			}
			seenIDs[value.ID] = struct{}{}
		}
		seenKeys[value.Key()] = struct{}{}
		reversed = append(reversed, value)
	}
	result := make([]SubjectiveOpinion, len(reversed))
	for index := range reversed {
		result[len(reversed)-1-index] = reversed[index]
	}
	return result
}

func sortOpinions(values []SubjectiveOpinion) {
	sort.SliceStable(values, func(left, right int) bool {
		leftKey, rightKey := values[left].Key(), values[right].Key()
		if leftKey == rightKey {
			return values[left].ID < values[right].ID
		}
		return leftKey < rightKey
	})
}

func (e *Engine) validateScalarDelta(delta, current map[string]float64, ranges map[string]ValueRange, target string, fallback ValueRange) error {
	for _, name := range sortedKeys(delta) {
		value := delta[name]
		limit := e.config.MaxDelta
		if override, ok := e.config.MaxDeltaByTrait[strings.ToLower(name)]; ok {
			limit = override
		}
		if math.Abs(value) > limit {
			return fmt.Errorf("%w: %s %q delta %.6f exceeds %.6f", ErrDeltaExceeded, target, name, value, limit)
		}
		base := current[name]
		bounds := lookupRange(ranges, name, fallback)
		if target == "relationship" {
			if _, configured := ranges[strings.ToLower(name)]; !configured {
				bounds = defaultRelationshipRange(name)
			}
		}
		if !bounds.Contains(base + value) {
			return fmt.Errorf("%w: %s %q value %.6f is outside [%.6f,%.6f]", ErrOutOfRange, target, name, base+value, bounds.Min, bounds.Max)
		}
	}
	return nil
}

func (e *Engine) validatePersonaDelta(current MutablePersona, delta *PersonaDelta) error {
	if delta == nil {
		return nil
	}
	pinned := make(map[string]bool, len(current.PinnedTraits)+len(e.config.PinnedTraits))
	for _, name := range current.PinnedTraits {
		pinned[strings.ToLower(name)] = true
	}
	for name, value := range e.config.PinnedTraits {
		if value {
			pinned[strings.ToLower(name)] = true
		}
	}
	for _, name := range sortedKeys(delta.Traits) {
		if pinned[strings.ToLower(name)] {
			return fmt.Errorf("%w: persona trait %q cannot be changed", ErrPinnedTrait, name)
		}
	}
	if err := e.validateScalarDelta(delta.Traits, current.Traits, e.config.TraitRanges, "persona", ValueRange{}); err != nil {
		return err
	}
	prompt := firstNonEmpty(delta.Prompt, delta.PromptDelta)
	if prompt != "" {
		if forbiddenPromptMutation(prompt) {
			return fmt.Errorf("%w: persona prompt attempts to alter an immutable boundary", ErrForbiddenMutation)
		}
		if err := validatePromptText(prompt, e.config.MaxPromptBytes, ErrInvalidProposal); err != nil {
			return err
		}
		result := strings.TrimSpace(delta.Prompt)
		if result == "" {
			result = strings.TrimSpace(current.Prompt)
			if result == "" {
				result = strings.TrimSpace(delta.PromptDelta)
			} else {
				result += "\n" + strings.TrimSpace(delta.PromptDelta)
			}
		}
		if len([]byte(result)) > e.config.MaxPromptBytes {
			return fmt.Errorf("%w: resulting persona prompt exceeds %d bytes", ErrDeltaExceeded, e.config.MaxPromptBytes)
		}
	}
	return nil
}

func proposalHasTarget(proposal ReflectionProposal, target string) bool {
	switch target {
	case "relationship":
		return proposal.Relationship != nil
	case "affect":
		return proposal.Affect != nil
	case "persona":
		return proposal.Persona != nil
	default:
		return false
	}
}

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

func decisionForError(err error) Decision {
	switch {
	case err == nil:
		return DecisionApplied
	case isError(err, ErrInsufficientEvidence):
		return DecisionNoEvidence
	case isError(err, ErrPinnedTrait):
		return DecisionPinnedTrait
	case isError(err, ErrDeltaExceeded), isError(err, ErrOutOfRange):
		return DecisionDeltaLimit
	case isError(err, ErrOpinionLimit):
		return DecisionDeltaLimit
	case isError(err, ErrUntrustedEvidence), isError(err, ErrForbiddenMutation):
		return DecisionUntrusted
	case isError(err, ErrBudgetExceeded):
		return DecisionBudget
	default:
		return ""
	}
}

func isError(err, target error) bool {
	return errors.Is(err, target)
}

func ensureFloatMap(values map[string]float64) map[string]float64 {
	if values == nil {
		return make(map[string]float64)
	}
	return values
}

func defaultDimensionRange() ValueRange { return ValueRange{Min: -1, Max: 1} }

func defaultRelationshipRange(name string) ValueRange {
	switch strings.ToLower(name) {
	case "trust", "attachment", "respect", "irritation", "irritability", "jealousy",
		"resentment", "gratitude", "closeness", "reliability", "warmth":
		return ValueRange{Min: 0, Max: 1}
	default:
		return defaultDimensionRange()
	}
}

func defaultTraitRange(name string) ValueRange {
	// Most configurable persona intensities are naturally represented in
	// [0,1]. Unknown adapter-defined traits use the signed generic range.
	switch strings.ToLower(name) {
	case "warmth", "trust", "attachment", "jealousy", "irritability", "romantic_tone",
		"romanticity", "emotionality", "directness", "playfulness", "formality",
		"reliability", "closeness", "respect":
		return ValueRange{Min: 0, Max: 1}
	default:
		return defaultDimensionRange()
	}
}

func lookupRange(ranges map[string]ValueRange, name string, fallback ValueRange) ValueRange {
	if value, ok := ranges[strings.ToLower(name)]; ok {
		return value
	}
	if fallback == (ValueRange{}) {
		return defaultTraitRange(name)
	}
	return fallback
}

func validateRanges(ranges map[string]ValueRange, label string) error {
	for name, value := range ranges {
		if err := validateName(name); err != nil || !value.Valid() {
			return fmt.Errorf("%w: invalid %s entry %q", ErrInvalidSnapshot, label, name)
		}
	}
	return nil
}

func cloneRanges(input map[string]ValueRange) map[string]ValueRange {
	if input == nil {
		return nil
	}
	output := make(map[string]ValueRange, len(input))
	for name, value := range input {
		output[strings.ToLower(name)] = value
	}
	return output
}

func cloneLowerFloatMap(input map[string]float64) map[string]float64 {
	if input == nil {
		return nil
	}
	output := make(map[string]float64, len(input))
	for name, value := range input {
		output[strings.ToLower(name)] = value
	}
	return output
}

func cloneBoolMap(input map[string]bool) map[string]bool {
	if input == nil {
		return nil
	}
	output := make(map[string]bool, len(input))
	for name, value := range input {
		output[strings.ToLower(name)] = value
	}
	return output
}
