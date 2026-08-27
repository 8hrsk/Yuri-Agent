package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/voice"
)

func TestTranscribeAndSynthesize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("missing credential")
		}
		switch request.URL.Path {
		case "/v1/audio/transcriptions":
			if err := request.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			if request.FormValue("model") != "stt-test" {
				t.Errorf("unexpected model %q", request.FormValue("model"))
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"text":"привет","language":"ru"}`)
		case "/v1/audio/speech":
			writer.Header().Set("Content-Type", "audio/mpeg")
			_, _ = writer.Write([]byte("audio"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	adapter, err := New(Options{
		BaseURL: server.URL + "/v1", TranscriptionModel: "stt-test", SpeechModel: "tts-test",
		Credential: func(context.Context) (string, error) { return "test-secret", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := adapter.Transcribe(context.Background(), voice.AudioInput{Data: []byte("audio")})
	if err != nil || transcript.Text != "привет" || transcript.Language != "ru" {
		t.Fatalf("unexpected transcript %#v, %v", transcript, err)
	}
	audio, err := adapter.Synthesize(context.Background(), voice.SpeechRequest{Text: "ответ", Voice: "alloy"})
	if err != nil || string(audio.Data) != "audio" || audio.ContentType != "audio/mpeg" {
		t.Fatalf("unexpected audio %#v, %v", audio, err)
	}
}

func TestRequiresHTTPSOutsideLoopbackAndBoundsData(t *testing.T) {
	_, err := New(Options{
		BaseURL: "http://example.com/v1", TranscriptionModel: "stt", SpeechModel: "tts",
		Credential: func(context.Context) (string, error) { return "secret", nil },
	})
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("expected HTTPS error, got %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	adapter, err := New(Options{
		BaseURL: server.URL, TranscriptionModel: "stt", SpeechModel: "tts", MaxInputBytes: 2,
		Credential: func(context.Context) (string, error) { return "secret", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Transcribe(context.Background(), voice.AudioInput{Data: []byte("too large")})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size error, got %v", err)
	}
}

func TestProviderErrorDoesNotExposeBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, `{"error":"token-secret"}`)
	}))
	defer server.Close()
	adapter, err := New(Options{
		BaseURL: server.URL, TranscriptionModel: "stt", SpeechModel: "tts",
		Credential: func(context.Context) (string, error) { return "credential-secret", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Synthesize(context.Background(), voice.SpeechRequest{Text: "hi", Voice: "voice"})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("expected redacted error, got %v", err)
	}
}
