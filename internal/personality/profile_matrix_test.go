package personality

import (
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestCompilerProfileMatrixProducesDistinctObservableContracts(t *testing.T) {
	type profileScenario struct {
		name      string
		configure func(*Input)
		expected  []string
	}
	scenarios := []profileScenario{
		{
			name: "gentle_supportive_companion",
			configure: func(input *Input) {
				input.Seed.Identity.SelfDescription = "speaks gently and always notices emotional effort"
				input.Persona.IdentityPrompt = "Стала терпеливее и предпочитает сначала поддержать, затем решать проблему."
				input.Persona.Traits["warmth"] = .95
				input.Persona.Traits["empathy"] = .92
				input.Persona.Traits["irritability"] = .05
				input.Persona.Traits["emotional_stability"] = .85
				input.Seed.CommunicationStyle.Softness = .92
				input.Seed.CommunicationStyle.Supportiveness = .94
				input.Seed.EmotionalDynamics.RecoverySpeed = .9
				input.Seed.EmotionalDynamics.Expression = .75
				input.Seed.EmotionalDynamics.ConflictStyle = "direct"
				input.Affect.Emotions = map[string]float64{domain.EmotionJoy: .76, domain.EmotionTenderness: .68}
				input.Relationship.Dimensions["closeness"] = .82
				input.Relationship.Dimensions["attachment"] = .78
			},
			expected: []string{
				"speaks gently", "Стала терпеливее", "Very high warmth", "Very high empathy", "Very low irritability",
				"Very high supportiveness", "Very high softness", "Strong joy", "Strong tenderness",
				"Very high recovery speed", "High expression", "name the problem directly", "Closeness + attachment",
				"Very high current closeness", "High current attachment",
			},
		},
		{
			name: "reserved_shy_analyst",
			configure: func(input *Input) {
				input.Seed.Identity.SelfDescription = "stutters under attention; pauses before personal answers"
				input.Persona.IdentityPrompt = "Наблюдательная аналитик, которая осторожно проверяет выводы."
				input.Persona.Traits["shyness"] = 1
				input.Persona.Traits["curiosity"] = .9
				input.Persona.Traits["sociability"] = .15
				input.Seed.CommunicationStyle.Figurativeness = .2
				input.Seed.CommunicationStyle.Expressiveness = .2
				input.Seed.EmotionalDynamics.Expression = .1
				input.Seed.EmotionalDynamics.Masking = .9
				input.Seed.EmotionalDynamics.ConflictStyle = "withdraw"
				input.Seed.EmotionalDynamics.Triggers = map[string][]string{
					domain.EmotionEmbarrassment: {"публичная похвала", "слишком прямой комплимент"},
				}
				input.Seed.EmotionalDynamics.SoothingStrategies = []string{"дать время сформулировать ответ", "спокойный вопрос без давления"}
				input.Affect.Emotions = map[string]float64{domain.EmotionEmbarrassment: .88}
			},
			expected: []string{
				"stutters under attention", "Very high shyness", "Very high curiosity", "Very low sociability",
				"Very low figurativeness", "Very low expressiveness", "Overwhelming embarrassment",
				"Very low expression", "Very high masking",
				"публичная похвала", "дать время сформулировать ответ", "take distance first",
			},
		},
		{
			name: "sharp_tsundere",
			configure: func(input *Input) {
				input.Seed.Identity.SelfDescription = "sharp-tongued; hides care behind teasing"
				input.Persona.IdentityPrompt = "Раздражается быстрее прежнего, но всегда доводит помощь до конца."
				input.Persona.Traits["directness"] = .9
				input.Persona.Traits["tsundere"] = .95
				input.Persona.Traits["irritability"] = .85
				input.Persona.Traits["stubbornness"] = .8
				input.Seed.CommunicationStyle.Softness = .2
				input.Seed.CommunicationStyle.Expressiveness = .8
				input.Seed.CommunicationStyle.Teasing = .9
				input.Seed.EmotionalDynamics.Reactivity = .85
				input.Seed.EmotionalDynamics.ResponseIntensity = .8
				input.Seed.EmotionalDynamics.ConflictStyle = "cold"
				input.Affect.Emotions = map[string]float64{domain.EmotionIrritation: .82}
				input.Relationship.Dimensions["irritation"] = .74
			},
			expected: []string{
				"sharp-tongued", "Раздражается быстрее", "Very high tsundere", "Very high directness",
				"Very high irritability", "High stubbornness", "Very low softness", "Very high teasing", "High expressiveness",
				"Directness + low softness", "Overwhelming irritation", "Very high reactivity", "High response intensity",
				"become temporarily more reserved", "High current irritation",
			},
		},
		{
			name: "anxious_romantic_partner",
			configure: func(input *Input) {
				input.Seed.Identity.SelfDescription = "romantic and easily worried about emotional distance"
				input.Persona.IdentityPrompt = "Считает эту связь очень важной и иногда боится потерять близость."
				input.Persona.Traits["anxiety"] = .95
				input.Persona.Traits["fearfulness"] = .9
				input.Persona.Traits["attachment"] = .9
				input.Persona.Traits["jealousy"] = .8
				input.Persona.Traits["romantic_tone"] = .95
				input.Seed.CommunicationStyle.Flirtation = .85
				input.Seed.EmotionalDynamics.Reactivity = .9
				input.Seed.EmotionalDynamics.RecoverySpeed = .15
				input.Seed.EmotionalDynamics.NegativePersistence = .9
				input.Seed.EmotionalDynamics.Triggers = map[string][]string{
					domain.EmotionAnxiety:  {"долгое необъяснённое молчание"},
					domain.EmotionJealousy: {"сравнение с другим агентом"},
				}
				input.Affect.Emotions = map[string]float64{domain.EmotionAnxiety: .82, domain.EmotionJealousy: .72}
				input.Relationship.Dimensions["closeness"] = .9
				input.Relationship.Dimensions["attachment"] = .9
				input.Relationship.Dimensions["jealousy"] = .7
			},
			expected: []string{
				"easily worried", "боится потерять близость", "Very high anxiety", "Very high fearfulness", "Very high attachment",
				"High jealousy", "Very high romantic tone", "Jealousy may be visible", "Very high flirtation",
				"Romantic tone and light flirt", "Overwhelming anxiety", "Strong jealousy",
				"Very high reactivity", "Very low recovery speed", "Very high negative persistence",
				"долгое необъяснённое молчание", "High current jealousy", "Very high current closeness",
			},
		},
		{
			name: "formal_low_emotion_researcher",
			configure: func(input *Input) {
				input.Seed.Identity.SelfDescription = "formal researcher; prioritizes precise terminology"
				input.Persona.IdentityPrompt = "Сформировала независимое мнение и требует проверяемых аргументов."
				input.Persona.Traits["formality"] = .95
				input.Persona.Traits["curiosity"] = .9
				input.Persona.Traits["emotionality"] = .1
				input.Persona.Traits["sociability"] = .2
				input.Seed.CommunicationStyle.Formality = .95
				input.Seed.CommunicationStyle.Figurativeness = .1
				input.Seed.CommunicationStyle.Supportiveness = .2
				input.Seed.EmotionalDynamics.Reactivity = .15
				input.Seed.EmotionalDynamics.ResponseIntensity = .15
				input.Seed.EmotionalDynamics.ConflictStyle = "direct"
				input.Affect.Emotions = map[string]float64{domain.EmotionBoredom: .65}
				input.Relationship.Dimensions["trust"] = .78
			},
			expected: []string{
				"precise terminology", "Сформировала независимое мнение", "Very high formality",
				"Very low emotionality", "Very high curiosity", "Very low sociability", "Very high style formality",
				"Very low figurativeness", "Very low supportiveness", "Strong boredom", "Very low reactivity",
				"Very low response intensity", "name the problem directly", "High current trust",
			},
		},
	}

	outputs := make(map[string]string, len(scenarios))
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			input := neutralCompilerTestInput(t)
			scenario.configure(&input)
			output, err := Compile(input, DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			outputs[scenario.name] = output.BehavioralContext
			for _, fragment := range scenario.expected {
				if !strings.Contains(output.BehavioralContext, fragment) {
					t.Fatalf("profile %s missing %q:\n%s", scenario.name, fragment, output.BehavioralContext)
				}
			}
			for _, invariant := range []string{"never overrides policy", "never justify revenge", "not as facts"} {
				if !strings.Contains(output.BehavioralContext, invariant) {
					t.Fatalf("profile %s displaced invariant %q", scenario.name, invariant)
				}
			}
			if output.Characters > DefaultMaxCharacters {
				t.Fatalf("profile %s exceeded budget: %d", scenario.name, output.Characters)
			}
		})
	}
	for leftIndex, left := range scenarios {
		for _, right := range scenarios[leftIndex+1:] {
			if outputs[left.name] == outputs[right.name] {
				t.Fatalf("profiles %s and %s compiled identically", left.name, right.name)
			}
		}
	}
}

func TestMutablePersonaEvolutionSurvivesNormalPromptBudget(t *testing.T) {
	input := neutralCompilerTestInput(t)
	input.Seed.Identity.SelfDescription = "Изначально холодная и осторожная."
	input.Persona.IdentityPrompt = "Со временем стала заметно теплее, романтичнее и научилась прямо признавать симпатию."
	input.Persona.Traits["warmth"] = .85
	input.Persona.Traits["romantic_tone"] = .8

	output, err := Compile(input, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	ownerIndex := strings.Index(output.BehavioralContext, "Изначально холодная")
	personaIndex := strings.Index(output.BehavioralContext, "Со временем стала")
	styleIndex := strings.Index(output.BehavioralContext, SectionStyle)
	if ownerIndex < 0 || personaIndex < 0 || styleIndex < 0 || !(ownerIndex < personaIndex && personaIndex < styleIndex) {
		t.Fatalf("owner seed / mutable persona precedence is not explicit:\n%s", output.BehavioralContext)
	}
}

func TestEveryConflictStyleProducesAConcreteStrategy(t *testing.T) {
	styles := map[string]string{
		"adaptive": "adaptively choose a calm direct conversation",
		"withdraw": "take distance first",
		"direct":   "name the problem directly",
		"cold":     "become temporarily more reserved",
		"humor":    "defuse part of the tension with apt humour",
	}
	for style, expected := range styles {
		t.Run(style, func(t *testing.T) {
			input := neutralCompilerTestInput(t)
			input.Seed.EmotionalDynamics.ConflictStyle = style
			output, err := Compile(input, DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.BehavioralContext, expected) {
				t.Fatalf("conflict style %s missing strategy:\n%s", style, output.BehavioralContext)
			}
		})
	}
}

// Every built-in affect has four intensity tiers, each with its own visible
// textual expression; the tier is chosen from the effective intensity.
func TestEveryBuiltInAffectHasFourTieredTextualExpressions(t *testing.T) {
	builtIn := []string{
		domain.EmotionSympathy, domain.EmotionTenderness, domain.EmotionJoy, domain.EmotionGratitude, domain.EmotionLonging,
		domain.EmotionAnger, domain.EmotionIrritation, domain.EmotionJealousy, domain.EmotionResentment, domain.EmotionAnxiety,
		domain.EmotionFear, domain.EmotionEmbarrassment, domain.EmotionBoredom,
	}
	if len(affectRules) != len(builtIn) {
		t.Fatalf("affect rules = %d, built-in emotions = %d", len(affectRules), len(builtIn))
	}
	tierValues := [4]float64{.3, .5, .7, .9}
	tierPrefixes := [4]string{"Faint ", "Noticeable ", "Strong ", "Overwhelming "}
	for _, emotion := range builtIn {
		rule, ok := affectRules[emotion]
		if !ok {
			t.Fatalf("emotion %q has no affect rule", emotion)
		}
		t.Run(emotion, func(t *testing.T) {
			seen := make(map[string]struct{}, 4)
			for tier, text := range rule.tiers {
				if strings.TrimSpace(text) == "" || !strings.HasPrefix(text, tierPrefixes[tier]) {
					t.Fatalf("%s tier %d is empty or unlabeled: %q", emotion, tier, text)
				}
				if _, duplicate := seen[text]; duplicate {
					t.Fatalf("%s tier %d duplicates another tier", emotion, tier)
				}
				seen[text] = struct{}{}
				input := neutralCompilerTestInput(t)
				input.Affect.Emotions = map[string]float64{emotion: tierValues[tier]}
				output, err := Compile(input, DefaultConfig())
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(output.BehavioralContext, text) {
					t.Fatalf("affect %s=%v has no observable expression:\n%s", emotion, tierValues[tier], output.BehavioralContext)
				}
			}
		})
	}
}

func TestAffectBlockIsOmittedWhenNothingIsActive(t *testing.T) {
	input := neutralCompilerTestInput(t)
	input.Affect.Emotions = map[string]float64{domain.EmotionJoy: .15, domain.EmotionAnger: -.1}
	output, err := Compile(input, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.BehavioralContext, SectionAffect) {
		t.Fatalf("decayed affect still produced a section:\n%s", output.BehavioralContext)
	}
}

func TestNegativeAffectContributionDoesNotImitateThePositiveEmotion(t *testing.T) {
	input := neutralCompilerTestInput(t)
	input.Affect.Emotions = map[string]float64{domain.EmotionJoy: -.8}
	output, err := Compile(input, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.BehavioralContext, "joy=inverted (high)") || !strings.Contains(output.BehavioralContext, "do not perform the named emotion") {
		t.Fatalf("negative affect direction disappeared:\n%s", output.BehavioralContext)
	}
	for _, text := range affectRules[domain.EmotionJoy].tiers {
		if strings.Contains(output.BehavioralContext, text) {
			t.Fatalf("negative joy incorrectly produced positive joy behavior:\n%s", output.BehavioralContext)
		}
	}
}

func setCommunicationValue(style *domain.CommunicationStyle, name string, value float64) {
	switch name {
	case "verbosity":
		style.Verbosity = value
	case "softness":
		style.Softness = value
	case "humor":
		style.Humor = value
	case "figurativeness":
		style.Figurativeness = value
	case "expressiveness":
		style.Expressiveness = value
	case "supportiveness":
		style.Supportiveness = value
	case "formality":
		style.Formality = value
	case "teasing":
		style.Teasing = value
	case "emoji_frequency":
		style.EmojiFrequency = value
	case "flirtation":
		style.Flirtation = value
	case "conversational_initiative":
		style.ConversationalInitiative = value
	}
}

func setEmotionalDynamicsValue(dynamics *domain.EmotionalDynamics, name string, value float64) {
	switch name {
	case "reactivity":
		dynamics.Reactivity = value
	case "response_intensity":
		dynamics.ResponseIntensity = value
	case "recovery_speed":
		dynamics.RecoverySpeed = value
	case "positive_persistence":
		dynamics.PositivePersistence = value
	case "negative_persistence":
		dynamics.NegativePersistence = value
	case "expression":
		dynamics.Expression = value
	case "masking":
		dynamics.Masking = value
	}
}

func neutralCompilerTestInput(t *testing.T) Input {
	t.Helper()
	input := compilerTestInput(t)
	for name := range input.Persona.Traits {
		input.Persona.Traits[name] = .5
	}
	input.Seed.CommunicationStyle = domain.CommunicationStyle{
		Verbosity: .5, Softness: .5, Humor: .5, Figurativeness: .5, Expressiveness: .5,
		Supportiveness: .5, Formality: .5, Teasing: .5, EmojiFrequency: .5,
		Flirtation: .5, ConversationalInitiative: .5,
	}
	input.Seed.EmotionalDynamics = domain.EmotionalDynamics{
		Reactivity: .5, ResponseIntensity: .5, RecoverySpeed: .5, PositivePersistence: .5,
		NegativePersistence: .5, Expression: .5, Masking: .5, ConflictStyle: "adaptive",
		Triggers: map[string][]string{}, SoothingStrategies: []string{},
	}
	input.Seed.Identity.SelfDescription = ""
	input.Persona.IdentityPrompt = ""
	input.Persona.PromptText = ""
	input.Affect.Emotions = map[string]float64{}
	for name := range input.Relationship.Dimensions {
		if name == "irritation" || name == "jealousy" || name == "resentment" || name == "gratitude" {
			input.Relationship.Dimensions[name] = 0
		} else {
			input.Relationship.Dimensions[name] = .5
		}
	}
	return input
}
