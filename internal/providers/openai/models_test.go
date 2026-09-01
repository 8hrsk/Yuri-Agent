package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

func TestListModelsPreservesOpenRouterCatalogMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/models" || request.URL.Query().Get("sort") != "throughput-high-to-low" {
			t.Fatalf("request URL = %s", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer openrouter-secret" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, `{"data":[{"id":"vendor/model:free","name":"Model Free","description":"Fast model","context_length":131072,"created":1700000000,"pricing":{"prompt":"0","completion":"0","request":"0"},"supported_parameters":["tools","temperature"],"architecture":{"input_modalities":["text","image"],"output_modalities":["text"]},"top_provider":{"context_length":131072,"max_completion_tokens":8192}}]}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL + "/api/v1", APIKey: "openrouter-secret"})
	if err != nil {
		t.Fatal(err)
	}
	models, err := client.ListModels(context.Background(), ModelListOptions{Sort: "throughput-high-to-low"})
	if err != nil || len(models) != 1 {
		t.Fatalf("models = %#v err=%v", models, err)
	}
	model := models[0]
	if model.ID != "vendor/model:free" || !model.Free || model.ContextLength != 131072 || model.MaxCompletionTokens != 8192 || len(model.InputModalities) != 2 || model.SupportedParameters[0] != "tools" {
		t.Fatalf("model = %#v", model)
	}
}

func TestListModelsRedactsCredentialAndRejectsUnknownSort(t *testing.T) {
	const secret = "sk-openrouter-super-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(writer, `{"error":{"message":"invalid %s"}}`, secret)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIKey: secret})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListModels(context.Background(), ModelListOptions{Sort: "unknown"}); err == nil {
		t.Fatal("unknown sort accepted")
	}
	_, err = client.ListModels(context.Background(), ModelListOptions{})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("catalog error = %v", err)
	}
}

func TestListModelsExtractsCapabilityMetadataAndModalityFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/models" {
			t.Fatalf("request path = %s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"data":[
{"id":"vendor/no-tools","context_length":0,"max_completion_tokens":2048,"supported_parameters":["structured_outputs","json_schema","response_format"],"architecture":{"modality":"text+image->text"},"top_provider":{"context_length":64000,"max_completion_tokens":4096}},
{"id":"vendor/unknown","context_length":32000,"architecture":{"input_modalities":["text"],"output_modalities":["text"]}}
]}`)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	models, err := client.ListModels(context.Background(), ModelListOptions{})
	if err != nil || len(models) != 2 {
		t.Fatalf("models = %#v err=%v", models, err)
	}
	withoutTools := models[0]
	if withoutTools.SupportsTools || !withoutTools.SupportsToolsKnown ||
		!withoutTools.SupportsStructuredOutput || !withoutTools.SupportsStructuredOutputKnown ||
		!withoutTools.SupportsJSONSchema || !withoutTools.SupportsJSONSchemaKnown ||
		!withoutTools.SupportsVision || !withoutTools.SupportsVisionKnown ||
		withoutTools.ContextLength != 64000 || withoutTools.MaxCompletionTokens != 2048 ||
		strings.Join(withoutTools.InputModalities, ",") != "text,image" ||
		strings.Join(withoutTools.OutputModalities, ",") != "text" {
		t.Fatalf("capabilities = %#v", withoutTools)
	}
	unknown := models[1]
	if unknown.SupportsToolsKnown || unknown.SupportsStructuredOutputKnown || unknown.SupportsJSONSchemaKnown ||
		!unknown.SupportsVisionKnown || unknown.SupportsVision {
		t.Fatalf("unknown capabilities = %#v", unknown)
	}
	capabilities, found := client.ModelCapabilities(withoutTools.ID)
	if !found || capabilities.SupportsTools || !capabilities.SupportsToolsKnown || capabilities.ContextLength != 64000 {
		t.Fatalf("cached capabilities = %#v found=%v", capabilities, found)
	}
}

func TestStartFailsBeforeInferenceForKnownModelWithoutTools(t *testing.T) {
	var modelsCalls, inferenceCalls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/models":
			modelsCalls++
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"data":[{"id":"vendor/no-tools","supported_parameters":[],"architecture":{"input_modalities":["text"],"output_modalities":["text"]}}]}`)
		case "/responses":
			inferenceCalls++
			http.Error(writer, "must not reach inference", http.StatusInternalServerError)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Model: "vendor/no-tools"})
	if err != nil {
		t.Fatal(err)
	}
	request := testRequest()
	request.Model = "vendor/no-tools"
	request.ToolChoice = agent.ToolChoice{Mode: agent.ToolChoiceRequired}
	_, err = client.Start(context.Background(), request)
	if err == nil || !errors.Is(err, agent.ErrModelCapabilityUnsupported) {
		t.Fatalf("start error = %v", err)
	}
	if modelsCalls != 1 || inferenceCalls != 0 {
		t.Fatalf("models calls=%d inference calls=%d", modelsCalls, inferenceCalls)
	}
}

func TestStartAllowsUnknownManualModelIDWhenToolsAreRequired(t *testing.T) {
	var modelsCalls, inferenceCalls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/models":
			modelsCalls++
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"data":[{"id":"vendor/listed","supported_parameters":[],"architecture":{"input_modalities":["text"],"output_modalities":["text"]}}]}`)
		case "/responses":
			inferenceCalls++
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	request := testRequest()
	request.Model = "vendor/private-manual-model"
	request.ToolChoice = agent.ToolChoice{Mode: agent.ToolChoiceRequired}
	stream, err := client.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("unknown manual model rejected: %v", err)
	}
	defer stream.Close()
	for {
		_, receiveErr := stream.Recv(context.Background())
		if errors.Is(receiveErr, io.EOF) {
			break
		}
		if receiveErr != nil {
			t.Fatal(receiveErr)
		}
	}
	if modelsCalls != 1 || inferenceCalls != 1 {
		t.Fatalf("models calls=%d inference calls=%d", modelsCalls, inferenceCalls)
	}
}
