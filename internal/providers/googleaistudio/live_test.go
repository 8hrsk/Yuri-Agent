package googleaistudio

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

// TestLiveGoogleAIStudio is deliberately opt-in and bounded. It gives the
// owner a secret-safe verification path without committing a key or model ID
// to source. Set the variables in the launching environment, never as command
// arguments or test fixtures.
func TestLiveGoogleAIStudio(t *testing.T) {
	if os.Getenv("YURI_GOOGLE_AI_STUDIO_LIVE") != "1" {
		t.Skip("set YURI_GOOGLE_AI_STUDIO_LIVE=1 for the bounded live smoke test")
	}
	key := strings.TrimSpace(os.Getenv("YURI_GOOGLE_AI_STUDIO_API_KEY"))
	model := strings.TrimSpace(os.Getenv("YURI_GOOGLE_AI_STUDIO_MODEL"))
	if key == "" || model == "" {
		t.Fatal("live smoke requires API key and exact model ID in the environment")
	}

	client, err := New(Config{APIKey: key, Model: model, Timeout: 30 * time.Second, ClientVersion: "live-smoke"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	models, err := client.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range models {
		if candidate.ID == model {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("configured model %q is absent from the generation catalog", model)
	}

	request := agent.ModelRequest{
		Model: model, Messages: []agent.Message{{Role: agent.RoleUser, Content: "Reply with OK."}}, MaxOutputTokens: 8,
	}
	count, err := client.CountTokens(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if count.TotalTokens <= 0 {
		t.Fatalf("countTokens returned %d", count.TotalTokens)
	}

	stream, err := client.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var output strings.Builder
	for {
		event, receiveErr := stream.Recv(ctx)
		if errors.Is(receiveErr, io.EOF) {
			break
		}
		if receiveErr != nil {
			t.Fatal(receiveErr)
		}
		if event.Type == agent.ModelEventTextDelta {
			output.WriteString(event.Delta)
		}
	}
	if strings.TrimSpace(output.String()) == "" {
		t.Fatal("live smoke returned no text")
	}
}
