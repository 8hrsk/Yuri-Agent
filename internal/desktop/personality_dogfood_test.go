package desktop

import (
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/personality"
)

func TestDefaultPersonalityDogfoodProfilesAreValidAndContrasting(t *testing.T) {
	profiles := defaultPersonalityDogfoodProfiles()
	if len(profiles) < 2 {
		t.Fatalf("profiles = %d", len(profiles))
	}
	compiled := make(map[string]string, len(profiles))
	for _, profile := range profiles {
		state, err := buildAgentCreationState(domain.ID("dogfood-"+profile.ID), profile.Input, time.Now().UTC())
		if err != nil {
			t.Fatalf("%s: %v", profile.ID, err)
		}
		output, err := compilePersonalityContext(state.Personalization, state.Persona, state.Relationship, state.Affect)
		if err != nil {
			t.Fatalf("compile %s: %v", profile.ID, err)
		}
		compiled[profile.ID] = output.BehavioralContext
		if profile.Contract.Profile != profile.ID || profile.Contract.MinimumSignalCoverage <= 0 || len(profile.Contract.SignalGroups) == 0 {
			t.Fatalf("contract %s = %#v", profile.ID, profile.Contract)
		}
	}
	if compiled["reserved"] == compiled["direct"] || !strings.Contains(compiled["reserved"], "Очень высокая стеснительность") || !strings.Contains(compiled["direct"], "Высокая прямота") {
		t.Fatalf("profiles are not observably contrasting:\nreserved=%s\ndirect=%s", compiled["reserved"], compiled["direct"])
	}
}

func TestDogfoodResponseUsesOnlyVisibleDeltas(t *testing.T) {
	events := []ChatEvent{
		{Type: "thinking", Delta: ""},
		{Type: "assistant.delta", Delta: "Скажу "},
		{Type: "tool.completed", Error: "redacted"},
		{Type: "assistant.delta", Delta: "прямо."},
	}
	if response := dogfoodResponse(events); response != "Скажу прямо." {
		t.Fatalf("response = %q", response)
	}
	if message := dogfoodRunError(events); message != "redacted" {
		t.Fatalf("error = %q", message)
	}
}

func TestDogfoodStoredResponseUsesAssistantSegmentsFromRun(t *testing.T) {
	messages := []ChatMessageView{
		{Role: "user", Content: "prompt", RunID: ""},
		{Role: "assistant", Content: "Первая часть.", RunID: "run-target"},
		{Role: "assistant", Content: "чужой ответ", RunID: "run-other"},
		{Role: "assistant", Content: "Вторая часть.", RunID: "run-target"},
	}
	if response := dogfoodStoredResponse(messages, "run-target"); response != "Первая часть.\n\nВторая часть." {
		t.Fatalf("response = %q", response)
	}
}

func TestDogfoodSuiteProfilesFitEvaluatorContract(t *testing.T) {
	profiles := defaultPersonalityDogfoodProfiles()
	contracts := make([]personality.BehavioralProfileContract, 0, len(profiles))
	for _, profile := range profiles {
		contracts = append(contracts, profile.Contract)
	}
	if len(contracts) != 2 || contracts[0].Profile == contracts[1].Profile {
		t.Fatalf("contracts = %#v", contracts)
	}
}
