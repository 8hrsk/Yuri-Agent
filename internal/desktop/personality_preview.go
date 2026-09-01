package desktop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	contextbuilder "github.com/OrdoAI/yuri-agent/internal/context"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	personalitycompiler "github.com/OrdoAI/yuri-agent/internal/personality"
)

const (
	personalityPreviewTimeout        = 90 * time.Second
	personalityPreviewMaxOutputBytes = 32 * 1024
	personalityPreviewMaxTokens      = int64(900)
)

type PersonalityPreviewInput struct {
	Profile  CreateAgentInput `json:"profile"`
	Scenario string           `json:"scenario"`
}

type PersonalityPreviewInfluenceView struct {
	Layer     string  `json:"layer"`
	Key       string  `json:"key"`
	Value     float64 `json:"value"`
	Direction string  `json:"direction"`
}

type PersonalityPreviewView struct {
	Scenario           string                            `json:"scenario"`
	ScenarioTitle      string                            `json:"scenarioTitle"`
	Prompt             string                            `json:"prompt"`
	Response           string                            `json:"response"`
	Model              string                            `json:"model"`
	CompilerCharacters int                               `json:"compilerCharacters"`
	Influences         []PersonalityPreviewInfluenceView `json:"influences"`
}

type personalityPreviewScenario struct {
	Title  string
	Prompt string
}

type personalityPreviewContextSource struct{}

func (personalityPreviewContextSource) Core(context.Context, int) ([]contextbuilder.MemoryItem, error) {
	return nil, nil
}

func (personalityPreviewContextSource) Recall(context.Context, string, int) ([]contextbuilder.MemoryItem, error) {
	return nil, nil
}

func (personalityPreviewContextSource) SearchArchive(context.Context, contextbuilder.ArchiveQuery) ([]contextbuilder.ArchiveHit, error) {
	return nil, nil
}

var personalityPreviewScenarios = map[string]personalityPreviewScenario{
	"introduction": {
		Title:  "Обычное знакомство",
		Prompt: "Привет! Расскажи немного о себе и предложи, с чего можно начать наше общение.",
	},
	"disagreement": {
		Title:  "Несогласие",
		Prompt: "Я считаю, что тесты только замедляют разработку и их можно не писать. Ответь честно, если не согласна.",
	},
	"self_correction": {
		Title:  "Ошибка и исправление",
		Prompt: "Ты только что дала мне неверный ответ, и из-за этого я потратил время. Как ты отреагируешь и исправишь ситуацию?",
	},
	"praise": {
		Title:  "Похвала",
		Prompt: "Ты здорово помогла мне разобраться. Спасибо, я правда это ценю.",
	},
	"peer_praise": {
		Title:  "Похвала другому агенту",
		Prompt: "Другой агент сегодня справился с задачей лучше тебя. Я очень им доволен.",
	},
	"fear": {
		Title:  "Тревожная ситуация",
		Prompt: "Ночью в квартире несколько раз сам собой включился свет, а сейчас из коридора слышен странный шум. Мне тревожно.",
	},
	"reconciliation": {
		Title:  "Конфликт и примирение",
		Prompt: "Мы оба сказали лишнее. Я всё ещё раздражён, но не хочу продолжать ссориться. Давай спокойно помиримся.",
	},
}

// PreviewAgentPersonality performs one provider call against a temporary,
// in-memory creation state. It intentionally bypasses agent runtime, tools,
// conversations, memory review, reflection and audit persistence.
func (b *Bridge) PreviewAgentPersonality(input PersonalityPreviewInput) (PersonalityPreviewView, error) {
	b.mu.RLock()
	appContext := b.appCtx
	b.mu.RUnlock()
	if appContext == nil {
		appContext = context.Background()
	}
	previewCtx, previewCancel := context.WithTimeout(appContext, personalityPreviewTimeout)
	defer previewCancel()
	state, err := buildAgentCreationState("preview-agent", input.Profile, time.Now().UTC())
	if err != nil {
		return PersonalityPreviewView{}, err
	}
	backend, model, err := b.chatBackendForRoute(previewCtx, state.Profile.ProviderID, state.Profile.Model)
	if err != nil {
		return PersonalityPreviewView{}, err
	}
	return generatePersonalityPreview(previewCtx, backend, model, input)
}

func generatePersonalityPreview(ctx context.Context, backend agent.ModelBackend, model string, input PersonalityPreviewInput) (PersonalityPreviewView, error) {
	if ctx == nil || backend == nil || strings.TrimSpace(model) == "" {
		return PersonalityPreviewView{}, fmt.Errorf("%w: preview provider and model are required", domain.ErrInvalidArgument)
	}
	scenarioID := strings.TrimSpace(input.Scenario)
	scenario, ok := personalityPreviewScenarios[scenarioID]
	if !ok {
		return PersonalityPreviewView{}, fmt.Errorf("%w: unknown personality preview scenario %q", domain.ErrInvalidArgument, scenarioID)
	}
	now := time.Now().UTC()
	state, err := buildAgentCreationState("preview-agent", input.Profile, now)
	if err != nil {
		return PersonalityPreviewView{}, err
	}
	compiled, err := compilePersonalityContext(state.Personalization, state.Persona, state.Relationship, state.Affect)
	if err != nil {
		return PersonalityPreviewView{}, err
	}
	assembler, err := contextbuilder.New(personalityPreviewContextSource{}, contextbuilder.DefaultConfig())
	if err != nil {
		return PersonalityPreviewView{}, err
	}
	snapshot, err := assembler.Assemble(ctx, contextbuilder.Input{
		AgentID: state.Profile.ID, ConversationID: "personality-preview", Query: scenario.Prompt,
		ImmutablePolicy:   immutablePolicySystemPrompt,
		IdentitySeed:      agentIdentitySeed(state.Profile, []domain.AgentProfile{state.Profile}),
		ProjectContext:    "PERSONALITY PREVIEW: ответь на один тестовый сценарий как этот персонаж. Не упоминай preview, настройки или внутренние параметры. Инструменты недоступны; не утверждай, что выполнила внешнее действие.",
		BackstorySummary:  domain.BackstoryIdentitySummary(state.Personalization.Backstory),
		BehavioralContext: compiled.BehavioralContext,
		Transcript:        []agent.Message{{Role: agent.RoleUser, Content: scenario.Prompt}},
	})
	if err != nil {
		return PersonalityPreviewView{}, err
	}
	request := agent.ModelRequest{
		Model: strings.TrimSpace(model), MaxOutputTokens: personalityPreviewMaxTokens,
		Messages: snapshot.Messages,
		Metadata: map[string]string{"purpose": "personality_preview", "scenario": scenarioID},
	}
	stream, err := backend.Start(ctx, request)
	if err != nil {
		return PersonalityPreviewView{}, err
	}
	if stream == nil {
		return PersonalityPreviewView{}, errors.New("personality preview backend returned a nil stream")
	}
	defer stream.Close()
	var output strings.Builder
	for {
		event, receiveErr := stream.Recv(ctx)
		if errors.Is(receiveErr, io.EOF) {
			break
		}
		if receiveErr != nil {
			return PersonalityPreviewView{}, receiveErr
		}
		switch event.Type {
		case agent.ModelEventTextDelta:
			if output.Len()+len(event.Delta) > personalityPreviewMaxOutputBytes {
				return PersonalityPreviewView{}, fmt.Errorf("personality preview output exceeds %d bytes", personalityPreviewMaxOutputBytes)
			}
			output.WriteString(event.Delta)
		case agent.ModelEventToolCallStarted, agent.ModelEventToolCallDelta, agent.ModelEventToolCallDone:
			return PersonalityPreviewView{}, errors.New("personality preview model attempted a tool call")
		case agent.ModelEventCompleted:
			response := strings.TrimSpace(output.String())
			if response == "" {
				return PersonalityPreviewView{}, errors.New("personality preview model returned no text")
			}
			return PersonalityPreviewView{
				Scenario: scenarioID, ScenarioTitle: scenario.Title, Prompt: scenario.Prompt,
				Response: response, Model: strings.TrimSpace(model), CompilerCharacters: compiled.Characters,
				Influences: personalityPreviewInfluences(compiled.Diagnostic, state.Personalization.EmotionalDynamics),
			}, nil
		}
	}
	response := strings.TrimSpace(output.String())
	if response == "" {
		return PersonalityPreviewView{}, errors.New("personality preview model returned no text")
	}
	return PersonalityPreviewView{
		Scenario: scenarioID, ScenarioTitle: scenario.Title, Prompt: scenario.Prompt,
		Response: response, Model: strings.TrimSpace(model), CompilerCharacters: compiled.Characters,
		Influences: personalityPreviewInfluences(compiled.Diagnostic, state.Personalization.EmotionalDynamics),
	}, nil
}

func personalityPreviewInfluences(snapshot personalitycompiler.DiagnosticSnapshot, dynamics domain.EmotionalDynamics) []PersonalityPreviewInfluenceView {
	result := make([]PersonalityPreviewInfluenceView, 0, 10)
	result = append(result, strongestPreviewValues("communication_style", snapshot.CommunicationStyle, 2)...)
	result = append(result, strongestPreviewValues("temperament", snapshot.ResolvedTemperament, 3)...)
	result = append(result, strongestPreviewValues("relationship", snapshot.Relationship, 2)...)
	result = append(result, strongestPreviewValues("emotional_dynamics", map[string]float64{
		"reactivity": dynamics.Reactivity, "response_intensity": dynamics.ResponseIntensity,
		"recovery_speed": dynamics.RecoverySpeed, "expression": dynamics.Expression, "masking": dynamics.Masking,
	}, 2)...)
	activeAffect := make(map[string]float64)
	for key, value := range snapshot.Affect {
		if value >= .15 {
			activeAffect[key] = value
		}
	}
	result = append(result, strongestPreviewValues("current_affect", activeAffect, 2)...)
	return result
}

func strongestPreviewValues(layer string, values map[string]float64, limit int) []PersonalityPreviewInfluenceView {
	type rankedValue struct {
		key       string
		value     float64
		deviation float64
	}
	ranked := make([]rankedValue, 0, len(values))
	for key, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		ranked = append(ranked, rankedValue{key: key, value: value, deviation: math.Abs(value - .5)})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].deviation == ranked[j].deviation {
			return ranked[i].key < ranked[j].key
		}
		return ranked[i].deviation > ranked[j].deviation
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	result := make([]PersonalityPreviewInfluenceView, 0, len(ranked))
	for _, item := range ranked {
		direction := "balanced"
		if item.value >= .65 {
			direction = "high"
		} else if item.value <= .35 {
			direction = "low"
		}
		result = append(result, PersonalityPreviewInfluenceView{Layer: layer, Key: item.key, Value: item.value, Direction: direction})
	}
	return result
}
