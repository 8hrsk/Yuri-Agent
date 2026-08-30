package reflection

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func appraisalDynamics(reactivity, intensity, recovery, positivePersistence, negativePersistence float64) domain.EmotionalDynamics {
	return domain.EmotionalDynamics{
		Reactivity: reactivity, ResponseIntensity: intensity, RecoverySpeed: recovery,
		PositivePersistence: positivePersistence, NegativePersistence: negativePersistence,
		Expression: .5, Masking: .5, ConflictStyle: "adaptive",
		Triggers: map[string][]string{"fear": {"потеря связи"}}, SoothingStrategies: []string{"спокойный разговор"},
	}
}

func TestAffectAppraisalPolicyMakesContrastingProfilesReactDifferently(t *testing.T) {
	lowTemperament := domain.DefaultTemperament()
	lowTemperament.Optimism, lowTemperament.Emotionality = 0, 0
	highTemperament := lowTemperament
	highTemperament.Optimism, highTemperament.Emotionality = 1, 1
	low := NewAffectAppraisalPolicy(appraisalDynamics(0, 0, 0, .5, .5), lowTemperament, []string{domain.EmotionJoy})
	high := NewAffectAppraisalPolicy(appraisalDynamics(1, 1, 1, .5, .5), highTemperament, []string{domain.EmotionJoy})

	lowDelta, lowAllowed := low.AdjustDelta(domain.EmotionJoy, .1)
	highDelta, highAllowed := high.AdjustDelta(domain.EmotionJoy, .1)
	if !lowAllowed || !highAllowed || math.Abs(lowDelta-.02) > 1e-12 || math.Abs(highDelta-.1) > 1e-12 {
		t.Fatalf("contrasting activation = low %.4f high %.4f", lowDelta, highDelta)
	}
	lowRecovery, _ := low.AdjustDelta(domain.EmotionJoy, -.1)
	highRecovery, _ := high.AdjustDelta(domain.EmotionJoy, -.1)
	if math.Abs(lowRecovery+.025) > 1e-12 || math.Abs(highRecovery+.1) > 1e-12 {
		t.Fatalf("contrasting recovery = low %.4f high %.4f", lowRecovery, highRecovery)
	}
	if _, allowed := high.AdjustDelta("invented_emotion", .1); allowed {
		t.Fatal("appraisal accepted an emotion outside the profile vocabulary")
	}
}

func TestAffectAppraisalPolicyDerivesPerEmotionPersistence(t *testing.T) {
	policy := NewAffectAppraisalPolicy(
		appraisalDynamics(.7, .7, .4, .9, .1), domain.DefaultTemperament(),
		[]string{domain.EmotionJoy, domain.EmotionIrritation},
	)
	if policy.HalfLife(domain.EmotionJoy) <= policy.HalfLife(domain.EmotionIrritation) {
		t.Fatalf("positive half-life %s should exceed negative half-life %s", policy.HalfLife(domain.EmotionJoy), policy.HalfLife(domain.EmotionIrritation))
	}
	fast := NewAffectAppraisalPolicy(appraisalDynamics(.7, .7, 1, .9, .1), domain.DefaultTemperament(), []string{domain.EmotionJoy})
	if fast.HalfLife(domain.EmotionJoy) >= policy.HalfLife(domain.EmotionJoy) {
		t.Fatalf("fast recovery half-life %s should be below %s", fast.HalfLife(domain.EmotionJoy), policy.HalfLife(domain.EmotionJoy))
	}
}

func TestTemperamentPredispositionsShapeScenarioAppraisals(t *testing.T) {
	dynamics := appraisalDynamics(.75, .75, .5, .5, .5)
	tests := []struct {
		name      string
		emotion   string
		configure func(low, high *domain.Temperament)
	}{
		{name: "conflict", emotion: domain.EmotionIrritation, configure: func(low, high *domain.Temperament) {
			low.Irritability, low.Sensitivity = 0, 0
			high.Irritability, high.Sensitivity = 1, 1
		}},
		{name: "reconciliation", emotion: domain.EmotionGratitude, configure: func(low, high *domain.Temperament) {
			low.Empathy, low.Warmth = 0, 0
			high.Empathy, high.Warmth = 1, 1
		}},
		{name: "embarrassment", emotion: domain.EmotionEmbarrassment, configure: func(low, high *domain.Temperament) {
			low.Shyness, low.Sensitivity, low.Emotionality = 0, 0, 0
			high.Shyness, high.Sensitivity, high.Emotionality = 1, 1, 1
		}},
		{name: "fear", emotion: domain.EmotionFear, configure: func(low, high *domain.Temperament) {
			low.Fearfulness, low.Anxiety, low.Sensitivity, low.EmotionalStability = 0, 0, 0, 1
			high.Fearfulness, high.Anxiety, high.Sensitivity, high.EmotionalStability = 1, 1, 1, 0
		}},
		{name: "jealousy", emotion: domain.EmotionJealousy, configure: func(low, high *domain.Temperament) {
			low.Jealousy, low.Possessiveness, low.Attachment = 0, 0, 0
			high.Jealousy, high.Possessiveness, high.Attachment = 1, 1, 1
		}},
		{name: "praise", emotion: domain.EmotionJoy, configure: func(low, high *domain.Temperament) {
			low.Optimism, low.Emotionality = 0, 0
			high.Optimism, high.Emotionality = 1, 1
		}},
		{name: "boredom", emotion: domain.EmotionBoredom, configure: func(low, high *domain.Temperament) {
			low.Curiosity, low.Playfulness = 1, 1
			high.Curiosity, high.Playfulness = 0, 0
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			low, high := domain.DefaultTemperament(), domain.DefaultTemperament()
			test.configure(&low, &high)
			lowPolicy := NewAffectAppraisalPolicy(dynamics, low, []string{test.emotion})
			highPolicy := NewAffectAppraisalPolicy(dynamics, high, []string{test.emotion})
			lowDelta, lowAllowed := lowPolicy.AdjustDelta(test.emotion, .1)
			highDelta, highAllowed := highPolicy.AdjustDelta(test.emotion, .1)
			if !lowAllowed || !highAllowed || highDelta <= lowDelta {
				t.Fatalf("scenario %s did not reflect temperament: low %.4f high %.4f", test.name, lowDelta, highDelta)
			}
		})
	}
}

func TestEngineAppliesLocalAppraisalAndRejectsUnknownEmotion(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	snapshot := testSnapshot(now)
	snapshot.State.Affect.Dimensions = map[string]float64{domain.EmotionJoy: .2}
	snapshot.State.Affect.DimensionUpdated = map[string]time.Time{domain.EmotionJoy: now}
	snapshot.State.Affect.UpdatedAt = now
	temperament := domain.DefaultTemperament()
	temperament.Optimism, temperament.Emotionality = 0, 0
	policy := NewAffectAppraisalPolicy(appraisalDynamics(0, 0, .5, .5, .5), temperament, []string{domain.EmotionJoy})
	snapshot.AffectPolicy = policy
	proposal := ReflectionProposal{
		Outcome: OutcomeChanged, Reason: "a small positive event", EvidenceIDs: []domain.ID{"e-user"},
		Affect: &AffectDelta{Dimensions: map[string]float64{domain.EmotionJoy: .1}},
	}
	engine, err := New(Config{
		Analyzer: typedAnalyzer(proposal), Clock: reflectionTestClock{at: now}, MaxDelta: .1,
		MinimumEvidence: 1, AffectAppraisal: policy,
		AffectRanges: map[string]ValueRange{domain.EmotionJoy: {Min: 0, Max: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.AppliedAffectDeltas[domain.EmotionJoy]-.02) > 1e-12 || math.Abs(result.State.Affect.Dimensions[domain.EmotionJoy]-.22) > 1e-12 {
		t.Fatalf("bounded appraisal result = %#v", result)
	}
	if result.AffectHalfLifeSeconds[domain.EmotionJoy] != int64(policy.HalfLife(domain.EmotionJoy).Seconds()) {
		t.Fatalf("half-life metadata = %#v", result.AffectHalfLifeSeconds)
	}

	unknown := proposal
	unknown.Affect = &AffectDelta{Dimensions: map[string]float64{"invented_emotion": .1}}
	engine, err = New(Config{Analyzer: typedAnalyzer(unknown), Clock: reflectionTestClock{at: now}, MaxDelta: .1, MinimumEvidence: 1, AffectAppraisal: policy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Run(context.Background(), snapshot); !errors.Is(err, ErrForbiddenMutation) {
		t.Fatalf("unknown emotion error = %v, want forbidden mutation", err)
	}
}

func TestZeroOnlyAffectAppraisalMustBeNoChange(t *testing.T) {
	proposal := ReflectionProposal{
		Outcome: OutcomeChanged, Reason: "neutral event", EvidenceIDs: []domain.ID{"e-user"},
		Affect: &AffectDelta{Dimensions: map[string]float64{domain.EmotionJoy: 0}},
	}
	if err := proposal.Validate(); !errors.Is(err, ErrInvalidProposal) {
		t.Fatalf("zero-only appraisal error = %v, want invalid proposal", err)
	}
}

func TestDurableCooldownStillAllowsShortLivedAffect(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	snapshot := testSnapshot(now)
	snapshot.State.Affect.Dimensions = map[string]float64{domain.EmotionJoy: .2}
	snapshot.State.Affect.DimensionUpdated = map[string]time.Time{domain.EmotionJoy: now}
	snapshot.State.Affect.UpdatedAt = now
	snapshot.State.LastDurableUpdateAt = now.Add(-10 * time.Minute)
	policy := NewAffectAppraisalPolicy(appraisalDynamics(.8, .8, .6, .5, .5), domain.DefaultTemperament(), []string{domain.EmotionJoy})
	snapshot.AffectPolicy = policy
	var analyzerSawPaused bool
	engine, err := New(Config{
		Analyzer: AnalyzerFunc(func(_ context.Context, request AnalysisRequest) (AnalysisResponse, error) {
			analyzerSawPaused = request.Snapshot.DurableUpdatesPaused
			return AnalysisResponse{Proposal: ReflectionProposal{
				Outcome: OutcomeChanged, Reason: "pleasant interaction", EvidenceIDs: []domain.ID{"e-user"},
				Persona:      &PersonaDelta{Traits: map[string]float64{"warmth": .05}},
				Relationship: &RelationshipDelta{Dimensions: map[string]float64{"trust": .05}},
				Affect:       &AffectDelta{Dimensions: map[string]float64{domain.EmotionJoy: .05}},
			}}, nil
		}),
		Clock: reflectionTestClock{at: now}, MaxDelta: .1, MinimumEvidence: 1,
		DurableStateCooldown: time.Hour, AffectAppraisal: policy,
		AffectRanges: map[string]ValueRange{domain.EmotionJoy: {Min: 0, Max: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !analyzerSawPaused || result.Proposal.Persona != nil || result.Proposal.Relationship != nil || result.Proposal.Affect == nil {
		t.Fatalf("durable cooldown filtering = paused:%v proposal:%#v", analyzerSawPaused, result.Proposal)
	}
	if result.State.Persona.Version != snapshot.State.Persona.Version || result.State.Relationship.Version != snapshot.State.Relationship.Version || result.State.Affect.Version != snapshot.State.Affect.Version+1 {
		t.Fatalf("cooldown changed wrong target: %#v", result.State)
	}
}

func TestPerAgentDecayIsRestartStable(t *testing.T) {
	start := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	middle, end := start.Add(12*time.Hour), start.Add(36*time.Hour)
	policy := NewAffectAppraisalPolicy(
		appraisalDynamics(.7, .7, .35, .85, .25), domain.DefaultTemperament(),
		[]string{domain.EmotionJoy, domain.EmotionIrritation},
	).DecayPolicy()
	state := AffectiveState{
		Dimensions:       map[string]float64{domain.EmotionJoy: .8, domain.EmotionIrritation: .6},
		DimensionUpdated: map[string]time.Time{domain.EmotionJoy: start, domain.EmotionIrritation: start}, UpdatedAt: start,
	}
	direct, err := DecayAffect(state, end, policy)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := DecayAffect(state, middle, policy)
	if err != nil {
		t.Fatal(err)
	}
	afterRestart, err := DecayAffect(checkpoint, end, policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, emotion := range []string{domain.EmotionJoy, domain.EmotionIrritation} {
		if math.Abs(direct.Dimensions[emotion]-afterRestart.Dimensions[emotion]) > 1e-12 {
			t.Fatalf("restart changed %s decay: direct %.15f checkpoint %.15f", emotion, direct.Dimensions[emotion], afterRestart.Dimensions[emotion])
		}
	}
	if direct.Dimensions[domain.EmotionJoy] <= direct.Dimensions[domain.EmotionIrritation] {
		t.Fatalf("per-emotion persistence was not applied: %#v", direct.Dimensions)
	}
}
