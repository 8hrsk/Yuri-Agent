package openai

import (
	"encoding/json"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

func multimodalMessage() agent.Message {
	return agent.Message{Role: agent.RoleUser, Content: "Что на изображении?", Parts: []agent.ContentPart{{
		Type: agent.ContentPartImage, Name: "screen.png", MediaType: "image/png", Data: "iVBORw0KGgo=",
	}}}
}

func TestResponsesInputEncodesImageAsNativeContentPart(t *testing.T) {
	encoded, err := json.Marshal(responsesInput([]agent.Message{multimodalMessage()}))
	if err != nil {
		t.Fatal(err)
	}
	var payload []map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	content := payload[0]["content"].([]any)
	image := content[1].(map[string]any)
	if image["type"] != "input_image" || image["image_url"] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("responses image part = %#v", image)
	}
}

func TestChatInputEncodesImageAsNativeContentPart(t *testing.T) {
	encoded, err := json.Marshal(chatMessages([]agent.Message{multimodalMessage()}))
	if err != nil {
		t.Fatal(err)
	}
	var payload []map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	content := payload[0]["content"].([]any)
	image := content[1].(map[string]any)
	imageURL := image["image_url"].(map[string]any)
	if image["type"] != "image_url" || imageURL["url"] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("chat image part = %#v", image)
	}
}
