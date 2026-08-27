package desktop

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/config"
)

func TestTranscribeAudioRejectsInvalidAndUnconfiguredInput(t *testing.T) {
	bridge := &Bridge{config: config.Config{}}
	if _, err := bridge.TranscribeAudio(TranscribeAudioInput{AudioBase64: "not-base64"}); err == nil {
		t.Fatal("TranscribeAudio() expected invalid base64 error")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("audio"))
	if _, err := bridge.TranscribeAudio(TranscribeAudioInput{AudioBase64: encoded}); err == nil || !strings.Contains(err.Error(), "enabled OpenAI-compatible") {
		t.Fatalf("TranscribeAudio() error = %v", err)
	}
}

func TestSelectVoiceProviderHonorsPreferenceAndFallback(t *testing.T) {
	providers := []config.ProviderConfig{
		{ID: "first", Kind: config.ProviderOpenAICompatible, Enabled: true},
		{ID: "preferred", Kind: config.ProviderOpenAICompatible, Enabled: true},
	}
	selected, err := selectVoiceProvider(providers, "preferred")
	if err != nil || selected.ID != "preferred" {
		t.Fatalf("selectVoiceProvider() = %#v, %v", selected, err)
	}
	selected, err = selectVoiceProvider(providers, "missing")
	if err != nil || selected.ID != "first" {
		t.Fatalf("selectVoiceProvider() fallback = %#v, %v", selected, err)
	}
}
