package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
