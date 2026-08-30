package reflection

import (
	"context"
	"encoding/json"
	"fmt"
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
	if config.DurableStateCooldown < 0 {
		return nil, fmt.Errorf("%w: durable state cooldown cannot be negative", ErrInvalidSnapshot)
	}
	if !config.AffectAppraisal.Valid() {
		return nil, fmt.Errorf("%w: invalid affect appraisal policy", ErrInvalidSnapshot)
	}
	config.AffectAppraisal = config.AffectAppraisal.normalize()
	if config.AffectAppraisal.Enabled {
		config.AffectDecay = config.AffectAppraisal.DecayPolicy()
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
	config.AffectAppraisal = config.AffectAppraisal.clone()
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
	config.AffectAppraisal = config.AffectAppraisal.clone()
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
	durableUpdatesPaused := e.config.DurableStateCooldown > 0 && !base.LastDurableUpdateAt.IsZero() && now.Before(base.LastDurableUpdateAt.Add(e.config.DurableStateCooldown))
	// The decayed state is the state the analyzer should reason about, but it
	// remains a copy and is never written back through the input snapshot. The
	// result carries an explicit change bit so an adapter can persist it even
	// when the analyzer returns no_change or changes another target only.
	request.Snapshot.State = cloneState(base)
	request.Snapshot.DurableUpdatesPaused = durableUpdatesPaused
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
	if durableUpdatesPaused && proposal.Outcome == OutcomeChanged && (proposal.Persona != nil || proposal.Relationship != nil) {
		proposal.Persona = nil
		proposal.Relationship = nil
		if proposal.Affect == nil {
			proposal = ReflectionProposal{Outcome: OutcomeNoChange, Reason: "durable persona and relationship cooldown is active"}
		}
	}
	if proposal.Outcome == OutcomeNoChange {
		return ReflectionResult{
			ProfileID: input.ProfileID, RunID: input.RunID, Outcome: OutcomeNoChange,
			Decision: DecisionNoChange, Proposal: proposal, State: base,
			AffectDecayChanged: affectDecayChanged, Usage: usage,
			StartedAt: started, FinishedAt: e.finished(started),
		}, nil
	}
	next, appliedAffect, decision, guardErr := e.applyProposal(input, base, proposal, now)
	if guardErr != nil {
		return ReflectionResult{
			ProfileID: input.ProfileID, RunID: input.RunID, Outcome: OutcomeNoChange,
			Decision: decision, Proposal: proposal, State: base,
			AffectDecayChanged: affectDecayChanged, Usage: usage,
			StartedAt: started, FinishedAt: e.finished(started),
		}, guardErr
	}
	halfLives := make(map[string]int64, len(appliedAffect))
	for emotion := range appliedAffect {
		halfLives[emotion] = int64(e.config.AffectAppraisal.HalfLife(emotion).Seconds())
	}
	return ReflectionResult{
		ProfileID: input.ProfileID, RunID: input.RunID, Outcome: OutcomeChanged,
		Decision: DecisionApplied, Proposal: proposal, State: next,
		AffectDecayChanged: affectDecayChanged, AppliedAffectDeltas: appliedAffect,
		AffectHalfLifeSeconds: halfLives, Usage: usage,
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
