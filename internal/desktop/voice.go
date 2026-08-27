package desktop

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/voice"
	voiceopenai "github.com/OrdoAI/yuri-agent/internal/voice/openai"
)

const maxVoiceInputBytes = 25 << 20

type TranscribeAudioInput struct {
	AudioBase64 string `json:"audioBase64"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Language    string `json:"language,omitempty"`
}

type TranscriptView struct {
	Text     string `json:"text"`
	Language string `json:"language,omitempty"`
}

// TranscribeAudio connects the Stage 1 push-to-talk UI to the configured
// OpenAI-compatible speech endpoint. The audio is bounded before decoding and
// never persisted in SQLite or logs.
func (b *Bridge) TranscribeAudio(input TranscribeAudioInput) (TranscriptView, error) {
	encoded := strings.TrimSpace(input.AudioBase64)
	if encoded == "" {
		return TranscriptView{}, errors.New("audio is required")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxVoiceInputBytes) {
		return TranscriptView{}, errors.New("audio exceeds the 25 MB input limit")
	}
	audio, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return TranscriptView{}, errors.New("audio payload is not valid base64")
	}
	if len(audio) == 0 || len(audio) > maxVoiceInputBytes {
		return TranscriptView{}, errors.New("audio size is outside the allowed range")
	}

	b.mu.RLock()
	providers := append([]config.ProviderConfig(nil), b.config.Providers...)
	voiceConfig := b.config.Voice
	b.mu.RUnlock()
	provider, err := selectVoiceProvider(providers, voiceConfig.TranscriptionProviderID)
	if err != nil {
		return TranscriptView{}, err
	}
	transcriptionModel := strings.TrimSpace(voiceConfig.TranscriptionModel)
	if transcriptionModel == "" {
		transcriptionModel = "whisper-1"
	}
	speechModel := strings.TrimSpace(voiceConfig.SpeechModel)
	if speechModel == "" {
		speechModel = "gpt-4o-mini-tts"
	}
	adapter, err := voiceopenai.New(voiceopenai.Options{
		BaseURL: provider.BaseURL, TranscriptionModel: transcriptionModel, SpeechModel: speechModel,
		Credential: func(ctx context.Context) (string, error) {
			return b.keyring.Get(ctx, provider.CredentialRef)
		},
		MaxInputBytes: maxVoiceInputBytes,
	})
	if err != nil {
		return TranscriptView{}, err
	}
	ctx, cancel := b.context()
	defer cancel()
	transcript, err := adapter.Transcribe(ctx, voice.AudioInput{
		Data: audio, Filename: strings.TrimSpace(input.Filename), ContentType: strings.TrimSpace(input.ContentType),
		Language: strings.TrimSpace(input.Language),
	})
	if err != nil {
		return TranscriptView{}, errors.New(safeError(err.Error()))
	}
	return TranscriptView{Text: transcript.Text, Language: transcript.Language}, nil
}

func selectVoiceProvider(providers []config.ProviderConfig, preferredID string) (config.ProviderConfig, error) {
	for _, provider := range providers {
		if provider.ID == preferredID && provider.Kind == config.ProviderOpenAICompatible && provider.Enabled {
			return provider, nil
		}
	}
	for _, provider := range providers {
		if provider.Kind == config.ProviderOpenAICompatible && provider.Enabled {
			return provider, nil
		}
	}
	return config.ProviderConfig{}, errors.New("STT requires an enabled OpenAI-compatible provider")
}
