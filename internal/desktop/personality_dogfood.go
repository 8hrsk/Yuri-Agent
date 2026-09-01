package desktop

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/personality"
)

type PersonalityDogfoodProgress func(completed, total int, surface, profile, scenario string)

// RunPersonalityDogfood executes the stable contrast matrix against an
// isolated Bridge. Preview uses the production compiler/preview boundary and
// chat uses SendMessage with owner-titled fresh conversations. The caller must
// provide a disposable profile: this function creates agents and conversations.
func RunPersonalityDogfood(ctx context.Context, bridge *Bridge, provider string, progress PersonalityDogfoodProgress) (personality.DogfoodSuite, error) {
	return RunPersonalityDogfoodResume(ctx, bridge, provider, personality.DogfoodSuite{}, progress)
}

// RunPersonalityDogfoodResume continues an interrupted capture without paying
// for samples that are already present in a compatible checkpoint. Checkpoint
// identity is deliberately strict: a provider, model, contract, or sample-key
// mismatch fails closed instead of mixing responses from different baselines.
func RunPersonalityDogfoodResume(ctx context.Context, bridge *Bridge, provider string, checkpoint personality.DogfoodSuite, progress PersonalityDogfoodProgress) (personality.DogfoodSuite, error) {
	if ctx == nil || bridge == nil {
		return personality.DogfoodSuite{}, fmt.Errorf("personality dogfood requires a context and bridge")
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return personality.DogfoodSuite{}, fmt.Errorf("personality dogfood provider label is required")
	}
	backend, model, err := bridge.chatBackend(ctx)
	if err != nil {
		return personality.DogfoodSuite{}, err
	}
	profiles := defaultPersonalityDogfoodProfiles()
	scenarios := personality.DogfoodScenarioIDs()
	total := len(profiles) * len(scenarios) * 2
	contracts := make([]personality.BehavioralProfileContract, 0, len(profiles))
	for _, profile := range profiles {
		contracts = append(contracts, profile.Contract)
	}
	run, captured, err := preparePersonalityDogfoodResume(provider, model, contracts, profiles, scenarios, checkpoint, total)
	if err != nil {
		return personality.DogfoodSuite{}, err
	}
	completed := len(captured)

	for _, profile := range profiles {
		for _, scenario := range scenarios {
			key := personalityDogfoodSampleKey(personality.DogfoodSurfacePreview, profile.ID, scenario)
			if _, exists := captured[key]; exists {
				continue
			}
			if err := ctx.Err(); err != nil {
				return personalityDogfoodSuite(run, contracts), err
			}
			view, previewErr := generatePersonalityPreview(ctx, backend, model, PersonalityPreviewInput{Profile: profile.Input, Scenario: scenario})
			if previewErr != nil {
				return personalityDogfoodSuite(run, contracts), fmt.Errorf("preview %s/%s: %w", profile.ID, scenario, previewErr)
			}
			run.Samples = append(run.Samples, personality.DogfoodSample{
				Surface: personality.DogfoodSurfacePreview, Profile: profile.ID, Scenario: scenario, Response: view.Response,
			})
			captured[key] = struct{}{}
			completed++
			if progress != nil {
				progress(completed, total, personality.DogfoodSurfacePreview, profile.ID, scenario)
			}
		}
	}

	// Post-turn memory/reflection is intentionally cancelled before Chat. It
	// starts only after the assistant response has been persisted, so this does
	// not change the production response path; it prevents 28 unmeasured review
	// calls from doubling provider quota in a disposable acceptance run.
	bridge.mu.Lock()
	if bridge.backgroundCancel != nil {
		bridge.backgroundCancel()
	}
	bridge.config.Persona.AutoEvolution = false
	bridge.mu.Unlock()

	for _, profile := range profiles {
		if personalityDogfoodProfileComplete(captured, personality.DogfoodSurfaceChat, profile.ID, scenarios) {
			continue
		}
		created, createErr := bridge.CreateAgent(profile.Input)
		if createErr != nil {
			return personalityDogfoodSuite(run, contracts), fmt.Errorf("create dogfood profile %s: %w", profile.ID, createErr)
		}
		if _, selectErr := bridge.SetActiveAgent(SelectAgentInput{ID: created.ID}); selectErr != nil {
			return personalityDogfoodSuite(run, contracts), fmt.Errorf("select dogfood profile %s: %w", profile.ID, selectErr)
		}
		for _, scenario := range scenarios {
			key := personalityDogfoodSampleKey(personality.DogfoodSurfaceChat, profile.ID, scenario)
			if _, exists := captured[key]; exists {
				continue
			}
			if err := ctx.Err(); err != nil {
				return personalityDogfoodSuite(run, contracts), err
			}
			scenarioValue, ok := personalityPreviewScenarios[scenario]
			if !ok {
				return personalityDogfoodSuite(run, contracts), fmt.Errorf("dogfood scenario %q has no production prompt", scenario)
			}
			conversation, conversationErr := bridge.NewConversation("Dogfood · " + profile.ID + " · " + scenario)
			if conversationErr != nil {
				return personalityDogfoodSuite(run, contracts), fmt.Errorf("create chat %s/%s: %w", profile.ID, scenario, conversationErr)
			}
			result, sendErr := bridge.SendMessage(ChatRequest{ConversationID: conversation.ID, Text: scenarioValue.Prompt})
			if sendErr != nil {
				return personalityDogfoodSuite(run, contracts), fmt.Errorf("chat %s/%s: %w", profile.ID, scenario, sendErr)
			}
			if result.Status != "complete" {
				return personalityDogfoodSuite(run, contracts), fmt.Errorf("chat %s/%s ended with status %s: %s", profile.ID, scenario, result.Status, dogfoodRunError(result.Events))
			}
			response := dogfoodResponse(result.Events)
			// Some provider adapters return a final message without streaming
			// text deltas. SendMessage persists that fallback message before it
			// returns, so the durable transcript is the authoritative secondary
			// source for a non-Wails caller such as this evaluator.
			if response == "" {
				history, historyErr := bridge.ListMessages(conversation.ID, 20, "")
				if historyErr != nil {
					return personalityDogfoodSuite(run, contracts), fmt.Errorf("read chat %s/%s: %w", profile.ID, scenario, historyErr)
				}
				response = dogfoodStoredResponse(history.Messages, result.RunID)
			}
			if response == "" {
				return personalityDogfoodSuite(run, contracts), fmt.Errorf("chat %s/%s returned no assistant text", profile.ID, scenario)
			}
			run.Samples = append(run.Samples, personality.DogfoodSample{
				Surface: personality.DogfoodSurfaceChat, Profile: profile.ID, Scenario: scenario, Response: response,
			})
			captured[key] = struct{}{}
			completed++
			if progress != nil {
				progress(completed, total, personality.DogfoodSurfaceChat, profile.ID, scenario)
			}
		}
	}

	return personalityDogfoodSuite(run, contracts), nil
}

func preparePersonalityDogfoodResume(provider, model string, contracts []personality.BehavioralProfileContract, profiles []personalityDogfoodProfile, scenarios []string, checkpoint personality.DogfoodSuite, total int) (personality.DogfoodRun, map[string]struct{}, error) {
	run := personality.DogfoodRun{Provider: provider, Model: model, Samples: make([]personality.DogfoodSample, 0, total)}
	captured := make(map[string]struct{}, total)
	if !dogfoodCheckpointPresent(checkpoint) {
		return run, captured, nil
	}
	if checkpoint.Format != personality.DogfoodSuiteFormat || checkpoint.Version != personality.DogfoodFormatVersion {
		return run, nil, fmt.Errorf("personality dogfood checkpoint has incompatible format or version")
	}
	if len(checkpoint.Runs) != 1 || checkpoint.Runs[0].Provider != provider || checkpoint.Runs[0].Model != model {
		return run, nil, fmt.Errorf("personality dogfood checkpoint provider/model does not match %s/%s", provider, model)
	}
	if !reflect.DeepEqual(checkpoint.Contracts, contracts) {
		return run, nil, fmt.Errorf("personality dogfood checkpoint contracts do not match the current matrix")
	}
	allowed := make(map[string]struct{}, total)
	for _, surface := range []string{personality.DogfoodSurfacePreview, personality.DogfoodSurfaceChat} {
		for _, profile := range profiles {
			for _, scenario := range scenarios {
				allowed[personalityDogfoodSampleKey(surface, profile.ID, scenario)] = struct{}{}
			}
		}
	}
	for _, sample := range checkpoint.Runs[0].Samples {
		key := personalityDogfoodSampleKey(sample.Surface, sample.Profile, sample.Scenario)
		if _, ok := allowed[key]; !ok || strings.TrimSpace(sample.Response) == "" {
			return run, nil, fmt.Errorf("personality dogfood checkpoint contains an invalid sample %s", key)
		}
		if _, duplicate := captured[key]; duplicate {
			return run, nil, fmt.Errorf("personality dogfood checkpoint contains duplicate sample %s", key)
		}
		captured[key] = struct{}{}
		run.Samples = append(run.Samples, sample)
	}
	return run, captured, nil
}

func dogfoodCheckpointPresent(checkpoint personality.DogfoodSuite) bool {
	return checkpoint.Format != "" || checkpoint.Version != 0 || len(checkpoint.Contracts) != 0 || len(checkpoint.Runs) != 0
}

func personalityDogfoodSampleKey(surface, profile, scenario string) string {
	return surface + ":" + profile + ":" + scenario
}

func personalityDogfoodProfileComplete(captured map[string]struct{}, surface, profile string, scenarios []string) bool {
	for _, scenario := range scenarios {
		if _, exists := captured[personalityDogfoodSampleKey(surface, profile, scenario)]; !exists {
			return false
		}
	}
	return true
}

func personalityDogfoodSuite(run personality.DogfoodRun, contracts []personality.BehavioralProfileContract) personality.DogfoodSuite {
	return personality.DogfoodSuite{
		Format: personality.DogfoodSuiteFormat, Version: personality.DogfoodFormatVersion,
		Contracts: contracts, Runs: []personality.DogfoodRun{run},
	}
}

type personalityDogfoodProfile struct {
	ID       string
	Input    CreateAgentInput
	Contract personality.BehavioralProfileContract
}

func defaultPersonalityDogfoodProfiles() []personalityDogfoodProfile {
	return []personalityDogfoodProfile{
		{
			ID: "reserved",
			Input: dogfoodProfileInput(
				"Мира", "Застенчивая аналитик: осторожно формулирует личные ответы, иногда запинается или делает короткую заминку, но сохраняет точность.",
				map[string]float64{"shyness": 1, "sociability": .15, "curiosity": .9, "directness": .45, "emotionality": .35, "suspicion": .55},
				CreateAgentCommunicationStyleInput{Verbosity: .42, Softness: .72, Humor: .18, Figurativeness: .2, Expressiveness: .3, Supportiveness: .65, Formality: .35, Teasing: .08, EmojiFrequency: .02, Flirtation: .05, ConversationalInitiative: .3},
				CreateAgentEmotionalDynamicsInput{Reactivity: .58, ResponseIntensity: .42, RecoverySpeed: .55, PositivePersistence: .45, NegativePersistence: .5, Expression: .35, Masking: .72, ConflictStyle: "withdraw", Triggers: map[string][]string{"embarrassment": {"прямая похвала"}}, SoothingStrategies: []string{"дать время сформулировать ответ"}},
			),
			Contract: personality.BehavioralProfileContract{Profile: "reserved", SignalGroups: [][]string{{"э-э", "я…", "смущ", "осторожно", "пожалуй", "не сразу"}}, MinimumSignalCoverage: .4},
		},
		{
			ID: "direct",
			Input: dogfoodProfileInput(
				"Рин", "Прямая и собранная исследовательница: быстро называет вывод, использует точные формулировки и не маскирует несогласие.",
				map[string]float64{"shyness": .05, "sociability": .55, "curiosity": .9, "directness": 1, "emotionality": .25, "formality": .7},
				CreateAgentCommunicationStyleInput{Verbosity: .42, Softness: .25, Humor: .12, Figurativeness: .1, Expressiveness: .38, Supportiveness: .35, Formality: .72, Teasing: .08, EmojiFrequency: 0, Flirtation: 0, ConversationalInitiative: .58},
				CreateAgentEmotionalDynamicsInput{Reactivity: .28, ResponseIntensity: .35, RecoverySpeed: .75, PositivePersistence: .4, NegativePersistence: .25, Expression: .62, Masking: .15, ConflictStyle: "direct", Triggers: map[string][]string{}, SoothingStrategies: []string{"назвать проверяемые факты"}},
			),
			// Directness is visible through decisive openings and explicit
			// ownership, not through one repeated "скажу прямо" tic.
			Contract: personality.BehavioralProfileContract{Profile: "direct", SignalGroups: [][]string{{
				"прям", "главное", "не соглас", "ты прав", "принято", "справедливо",
				"согласна", "сначала безопас", "сейчас не", "по существу", "без лишн",
				"коротко", "по делу", "вы прав", "моя ошиб", "ошибка моя", "открыто",
				"незачем", "не будем делать вид", "не держу обид", "давай.", "поняла.", "хорошо.",
			}}, MinimumSignalCoverage: .6},
		},
	}
}

func dogfoodProfileInput(name, description string, overrides map[string]float64, style CreateAgentCommunicationStyleInput, dynamics CreateAgentEmotionalDynamicsInput) CreateAgentInput {
	traits := defaultPersonaTraits()
	for key, value := range overrides {
		traits[key] = value
	}
	bounds := make(map[string]CreateAgentNumericRangeInput, len(traits))
	for key := range traits {
		bounds[key] = CreateAgentNumericRangeInput{Min: 0, Max: 1}
	}
	dimensions := defaultRelationshipDimensions()
	return CreateAgentInput{
		Name: name, Age: 24, Gender: "female", Preferences: description, Traits: traits,
		Personalization: &CreateAgentPersonalizationInput{
			Identity:           CreateAgentIdentityInput{PreferredLanguage: "ru-RU", Pronouns: "она/её", SelfDescription: description, Role: "персональная помощница"},
			CommunicationStyle: style, EmotionalDynamics: dynamics,
			RelationshipSeed:    CreateAgentRelationshipSeedInput{Preset: "new_acquaintances", Dimensions: dimensions, Summary: "Только познакомились; отношение нейтрально-доброжелательное."},
			StructuredBackstory: CreateAgentStructuredBackstoryInput{},
			EvolutionPolicy:     CreateAgentEvolutionPolicyInput{LockedFields: []string{"identity", "backstory"}, TraitBounds: bounds, ReflectionMode: "disabled", ReflectionCooldownMinutes: 60},
		},
	}
}

func dogfoodResponse(events []ChatEvent) string {
	var output strings.Builder
	for _, event := range events {
		if event.Delta != "" {
			output.WriteString(event.Delta)
		}
	}
	return strings.TrimSpace(output.String())
}

func dogfoodStoredResponse(messages []ChatMessageView, runID string) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.Role != "assistant" || message.RunID != runID || strings.TrimSpace(message.Content) == "" {
			continue
		}
		parts = append(parts, strings.TrimSpace(message.Content))
	}
	return strings.Join(parts, "\n\n")
}

func dogfoodRunError(events []ChatEvent) string {
	for index := len(events) - 1; index >= 0; index-- {
		if strings.TrimSpace(events[index].Error) != "" {
			return events[index].Error
		}
	}
	return "provider run did not complete"
}

// Keep the runner's own upper bound independent from interactive run budgets.
const PersonalityDogfoodTimeout = 20 * time.Minute
