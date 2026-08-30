package reflection

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

const (
	minimumAffectHalfLife = 2 * time.Hour
	maximumAffectHalfLife = 14 * 24 * time.Hour
)

// AffectAppraisalPolicy is the immutable, per-agent policy used to turn a
// model's evidence-linked appraisal into a bounded affective event. The model
// can identify what happened, but it cannot choose the final intensity or
// persistence of the state transition.
type AffectAppraisalPolicy struct {
	Enabled             bool                `json:"enabled"`
	Reactivity          float64             `json:"reactivity"`
	ResponseIntensity   float64             `json:"response_intensity"`
	RecoverySpeed       float64             `json:"recovery_speed"`
	PositivePersistence float64             `json:"positive_persistence"`
	NegativePersistence float64             `json:"negative_persistence"`
	Expression          float64             `json:"expression"`
	Masking             float64             `json:"masking"`
	ConflictStyle       string              `json:"conflict_style,omitempty"`
	Predispositions     map[string]float64  `json:"predispositions,omitempty"`
	Triggers            map[string][]string `json:"triggers,omitempty"`
	SoothingStrategies  []string            `json:"soothing_strategies,omitempty"`
	AllowedEmotions     []string            `json:"allowed_emotions,omitempty"`
}

// NewAffectAppraisalPolicy compiles owner-authored emotional dynamics and
// stable temperament into a provider-independent policy. allowedEmotions is
// normally the current state's vocabulary, keeping model output closed over
// dimensions the application actually knows how to persist and display.
func NewAffectAppraisalPolicy(dynamics domain.EmotionalDynamics, temperament domain.Temperament, allowedEmotions []string) AffectAppraisalPolicy {
	policy := AffectAppraisalPolicy{
		Enabled: true, Reactivity: dynamics.Reactivity, ResponseIntensity: dynamics.ResponseIntensity,
		RecoverySpeed: dynamics.RecoverySpeed, PositivePersistence: dynamics.PositivePersistence,
		NegativePersistence: dynamics.NegativePersistence, Expression: dynamics.Expression,
		Masking: dynamics.Masking, ConflictStyle: strings.TrimSpace(dynamics.ConflictStyle),
		Predispositions: map[string]float64{
			domain.EmotionSympathy:      mean(temperament.Warmth, temperament.Empathy),
			domain.EmotionTenderness:    mean(temperament.Warmth, temperament.Attachment, temperament.RomanticTone),
			domain.EmotionJoy:           mean(temperament.Optimism, temperament.Emotionality),
			domain.EmotionGratitude:     mean(temperament.Empathy, temperament.Warmth),
			domain.EmotionLonging:       mean(temperament.Attachment, temperament.RomanticTone),
			domain.EmotionAnger:         mean(temperament.Irritability, temperament.Impulsivity, 1-temperament.EmotionalStability),
			domain.EmotionIrritation:    mean(temperament.Irritability, temperament.Sensitivity),
			domain.EmotionJealousy:      mean(temperament.Jealousy, temperament.Possessiveness, temperament.Attachment),
			domain.EmotionResentment:    mean(temperament.Stubbornness, temperament.Sensitivity, 1-temperament.EmotionalStability),
			domain.EmotionAnxiety:       mean(temperament.Anxiety, temperament.Fearfulness, temperament.Sensitivity, 1-temperament.EmotionalStability),
			domain.EmotionFear:          mean(temperament.Fearfulness, temperament.Anxiety, temperament.Sensitivity, 1-temperament.EmotionalStability),
			domain.EmotionEmbarrassment: mean(temperament.Shyness, temperament.Sensitivity, temperament.Emotionality),
			domain.EmotionBoredom:       mean(1-temperament.Curiosity, 1-temperament.Playfulness),
		},
		Triggers: cloneStringLists(dynamics.Triggers), SoothingStrategies: append([]string(nil), dynamics.SoothingStrategies...),
		AllowedEmotions: canonicalNames(allowedEmotions),
	}
	return policy.normalize()
}

func (policy AffectAppraisalPolicy) Valid() bool {
	if !policy.Enabled {
		return true
	}
	for _, value := range []float64{
		policy.Reactivity, policy.ResponseIntensity, policy.RecoverySpeed,
		policy.PositivePersistence, policy.NegativePersistence, policy.Expression, policy.Masking,
	} {
		if !finite(value) || value < 0 || value > 1 {
			return false
		}
	}
	if len(policy.Predispositions) > 64 || len(policy.Triggers) > 64 || len(policy.AllowedEmotions) > 64 || len(policy.SoothingStrategies) > 64 {
		return false
	}
	for name, value := range policy.Predispositions {
		if validateName(name) != nil || !finite(value) || value < 0 || value > 1 {
			return false
		}
	}
	for name, values := range policy.Triggers {
		if validateName(name) != nil || len(values) > 64 {
			return false
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || len([]rune(value)) > 256 {
				return false
			}
		}
	}
	for _, name := range policy.AllowedEmotions {
		if validateName(name) != nil {
			return false
		}
	}
	for _, value := range policy.SoothingStrategies {
		if strings.TrimSpace(value) == "" || len([]rune(value)) > 256 {
			return false
		}
	}
	return true
}

func (policy AffectAppraisalPolicy) normalize() AffectAppraisalPolicy {
	if !policy.Enabled {
		return AffectAppraisalPolicy{}
	}
	policy.ConflictStyle = strings.TrimSpace(policy.ConflictStyle)
	policy.Predispositions = cloneFloatMap(policy.Predispositions)
	policy.Triggers = cloneStringLists(policy.Triggers)
	policy.SoothingStrategies = canonicalTextList(policy.SoothingStrategies)
	policy.AllowedEmotions = canonicalNames(policy.AllowedEmotions)
	return policy
}

func (policy AffectAppraisalPolicy) clone() AffectAppraisalPolicy {
	result := policy
	result.Predispositions = cloneFloatMap(policy.Predispositions)
	result.Triggers = cloneStringLists(policy.Triggers)
	result.SoothingStrategies = append([]string(nil), policy.SoothingStrategies...)
	result.AllowedEmotions = append([]string(nil), policy.AllowedEmotions...)
	return result
}

// AdjustDelta applies local intensity and recovery constraints. A positive
// delta activates an emotion and is scaled by reactivity, response intensity,
// and the emotion's temperament predisposition. A negative delta represents
// recovery and is scaled by recovery speed. Zero-value/disabled policies keep
// the previous engine semantics for compatibility.
func (policy AffectAppraisalPolicy) AdjustDelta(emotion string, delta float64) (float64, bool) {
	emotion = strings.ToLower(strings.TrimSpace(emotion))
	if !policy.Enabled {
		return delta, true
	}
	if !policy.allows(emotion) {
		return 0, false
	}
	if delta == 0 {
		return 0, true
	}
	if delta < 0 {
		return delta * (.25 + .75*policy.RecoverySpeed), true
	}
	predisposition := policy.Predispositions[emotion]
	gain := .20 + .80*mean(policy.Reactivity, policy.ResponseIntensity, predisposition)
	return delta * gain, true
}

func (policy AffectAppraisalPolicy) allows(emotion string) bool {
	if len(policy.AllowedEmotions) == 0 {
		return true
	}
	index := sort.SearchStrings(policy.AllowedEmotions, emotion)
	return index < len(policy.AllowedEmotions) && policy.AllowedEmotions[index] == emotion
}

// HalfLife returns the deterministic persistence for one emotion. Positive
// and negative emotion families use independent owner settings; faster
// recovery shortens both while never producing a zero/unstable duration.
func (policy AffectAppraisalPolicy) HalfLife(emotion string) time.Duration {
	if !policy.Enabled {
		return DefaultDecayPolicy().HalfLife
	}
	persistence := policy.NegativePersistence
	if positiveEmotion(emotion) {
		persistence = policy.PositivePersistence
	}
	hours := (6 + 162*persistence) * (1.25 - .75*policy.RecoverySpeed)
	value := time.Duration(hours * float64(time.Hour))
	if value < minimumAffectHalfLife {
		return minimumAffectHalfLife
	}
	if value > maximumAffectHalfLife {
		return maximumAffectHalfLife
	}
	return value
}

func (policy AffectAppraisalPolicy) DecayPolicy() DecayPolicy {
	if !policy.Enabled {
		return DefaultDecayPolicy()
	}
	overrides := make(map[string]time.Duration, len(policy.AllowedEmotions))
	for _, emotion := range policy.AllowedEmotions {
		overrides[emotion] = policy.HalfLife(emotion)
	}
	return DecayPolicy{HalfLife: policy.HalfLife(domain.EmotionIrritation), DimensionHalfLives: overrides}
}

func positiveEmotion(emotion string) bool {
	switch strings.ToLower(strings.TrimSpace(emotion)) {
	case domain.EmotionSympathy, domain.EmotionTenderness, domain.EmotionJoy, domain.EmotionGratitude, domain.EmotionLonging:
		return true
	default:
		return false
	}
}

func mean(values ...float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += math.Max(0, math.Min(1, value))
	}
	return total / float64(len(values))
}

func canonicalNames(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || seen[value] || validateName(value) != nil {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func canonicalTextList(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func cloneStringLists(values map[string][]string) map[string][]string {
	if values == nil {
		return nil
	}
	result := make(map[string][]string, len(values))
	for name, items := range values {
		result[strings.ToLower(strings.TrimSpace(name))] = append([]string(nil), items...)
	}
	return result
}
