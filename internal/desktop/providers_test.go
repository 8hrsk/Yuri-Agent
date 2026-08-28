package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/providers/antigravity"
	securitykeyring "github.com/OrdoAI/yuri-agent/internal/security/keyring"
)

type providerTestKeyring struct{ values map[string]string }

func (backend *providerTestKeyring) Set(service, account, secret string) error {
	backend.values[service+":"+account] = secret
	return nil
}
func (backend *providerTestKeyring) Get(service, account string) (string, error) {
	value, found := backend.values[service+":"+account]
	if !found {
		return "", securitykeyring.ErrNotFound
	}
	return value, nil
}
func (backend *providerTestKeyring) Delete(service, account string) error {
	delete(backend.values, service+":"+account)
	return nil
}

func TestSaveOpenAIProviderKeepsSecretOutOfConfig(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{
		ConfigDirectory: filepath.Join(root, "config"),
		ConfigFile:      filepath.Join(root, "config", "config.json"),
		DataDirectory:   filepath.Join(root, "data"),
	}
	store, err := securitykeyring.NewWithBackend("test.yuri", &providerTestKeyring{values: make(map[string]string)})
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{paths: paths, config: config.Default(paths), keyring: store}
	view, err := bridge.SaveOpenAIProvider(SaveOpenAIProviderInput{
		ID: "main", DisplayName: "Main", BaseURL: "https://api.example.com/v1",
		Model: "model", APIKey: "sk-super-secret", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !view.HasSecret || len(bridge.ListProviders()) != 1 {
		t.Fatalf("unexpected provider view %#v", view)
	}
	content, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "sk-super-secret") {
		t.Fatal("config leaked API key")
	}
}

func TestSaveCodexProviderRejectsCredentialFieldsByConstruction(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{ConfigDirectory: root, ConfigFile: filepath.Join(root, "config.json"), DataDirectory: root}
	bridge := &Bridge{paths: paths, config: config.Default(paths)}
	view, err := bridge.SaveCodexProvider(SaveCodexProviderInput{ID: "codex", DisplayName: "Codex", Model: "model", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if view.Kind != config.ProviderCodexAppServer || view.HasSecret {
		t.Fatalf("unexpected Codex provider view %#v", view)
	}
	if view.Model != "model" {
		t.Fatalf("Codex OAuth lost the model picker selection: %#v", view)
	}
}

func TestAntigravityProbeIsExplicitlyUnsupportedWithoutPersistenceOrCredentials(t *testing.T) {
	paths := providerTestPaths(t)
	backend := &providerTestKeyring{values: make(map[string]string)}
	store, err := securitykeyring.NewWithBackend("test.antigravity", backend)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{paths: paths, config: config.Default(paths), keyring: store}

	probe := bridge.ProbeProvider(ProviderProbeInput{Kind: config.ProviderAntigravity})
	if probe.OK || probe.ErrorCode != antigravity.ErrorCodeUnsupportedAuthMode ||
		probe.Alternative != antigravity.AlternativeOpenAICompatible || probe.ProviderID != antigravity.ProviderID {
		t.Fatalf("probe = %#v", probe)
	}
	result := bridge.CompleteOnboarding(CompleteOnboardingInput{
		Settings: ProviderSettingsInput{Kind: config.ProviderAntigravity},
		APIKey:   "must-not-be-stored",
	})
	if result.OK || result.ErrorCode != antigravity.ErrorCodeUnsupportedAuthMode ||
		result.Alternative != antigravity.AlternativeOpenAICompatible || result.State.Completed {
		t.Fatalf("onboarding result = %#v", result)
	}
	if len(backend.values) != 0 || len(bridge.ListProviders()) != 0 {
		t.Fatalf("unsupported provider changed credentials/config: keys=%#v providers=%#v", backend.values, bridge.ListProviders())
	}
	if _, err := os.Stat(paths.ConfigFile); !os.IsNotExist(err) {
		t.Fatalf("unsupported provider wrote config: %v", err)
	}
}

func TestAntigravityCannotStartChatBackend(t *testing.T) {
	bridge := &Bridge{config: config.Config{Providers: []config.ProviderConfig{{
		ID: "antigravity", Kind: config.ProviderAntigravity, Enabled: true,
	}}}}
	backend, model, err := bridge.chatBackend(context.Background())
	if backend != nil || model != "" || !errors.Is(err, antigravity.ErrUnsupportedAuthMode) {
		t.Fatalf("chatBackend() = %#v, %q, %v", backend, model, err)
	}
}

func TestMarkProviderUntestedPersistsLogoutGate(t *testing.T) {
	paths := providerTestPaths(t)
	value := config.Default(paths)
	value.Onboarding.Completed = true
	value.Onboarding.ProviderTested = true
	if err := config.Save(paths, value); err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{paths: paths, config: value}
	if err := bridge.markProviderUntested(); err != nil {
		t.Fatal(err)
	}
	state := bridge.GetOnboardingState()
	if state.Completed || state.ProviderTested || state.State != OnboardingStatePending {
		t.Fatalf("logout onboarding state = %#v", state)
	}
	loaded, err := config.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Onboarding.Completed || loaded.Onboarding.ProviderTested {
		t.Fatalf("persisted logout gate = %#v", loaded.Onboarding)
	}
}

func TestOnboardingRequiresSuccessfulProbeAndSurvivesReload(t *testing.T) {
	const secret = "sk-local-onboarding-secret"
	var requests atomic.Int32
	var authorization atomic.Value
	server := newIPv4ProviderTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		authorization.Store(request.Header.Get("Authorization"))
		if request.URL.Path != "/v1/responses" {
			http.Error(writer, "unexpected endpoint", http.StatusNotFound)
			return
		}
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.Model != "local-model" {
			http.Error(writer, "unexpected request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		writeProviderProbeSSE(writer, flusher, "response.created", map[string]any{
			"type": "response.created", "response": map[string]any{"id": "resp_probe"},
		})
		writeProviderProbeSSE(writer, flusher, "response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "response_id": "resp_probe", "delta": "OK",
		})
		writeProviderProbeSSE(writer, flusher, "response.completed", map[string]any{
			"type": "response.completed", "response_id": "resp_probe",
		})
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	paths := providerTestPaths(t)
	backend := &providerTestKeyring{values: make(map[string]string)}
	store, err := securitykeyring.NewWithBackend("test.onboarding", backend)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{paths: paths, config: config.Default(paths), keyring: store}

	initial := bridge.GetOnboardingState()
	if initial.Completed || initial.ProviderTested || initial.ProviderConfigured || initial.State != OnboardingStatePending {
		t.Fatalf("clean onboarding state = %#v, want pending and unconfigured", initial)
	}

	if _, err := bridge.SaveOpenAIProvider(SaveOpenAIProviderInput{
		ID: "main", DisplayName: "Local fake", BaseURL: server.URL + "/v1", Model: "local-model",
		APIKey: secret, Enabled: true,
	}); err != nil {
		t.Fatalf("save provider: %v", err)
	}
	saved := bridge.GetOnboardingState()
	if saved.Completed || saved.ProviderTested || !saved.ProviderConfigured || saved.State != OnboardingStatePending {
		t.Fatalf("after save onboarding state = %#v, want pending until probe", saved)
	}

	// CompleteOnboarding is intentionally exercised with the transient key.
	// The method must save-and-probe rather than accepting a state transition
	// from the renderer.
	result := bridge.CompleteOnboarding(CompleteOnboardingInput{
		Settings: ProviderSettingsInput{
			ProviderID: "main", Kind: config.ProviderOpenAICompatible,
			BaseURL: server.URL + "/v1", Model: "local-model", TimeoutSeconds: 2,
		},
		APIKey: secret,
	})
	if !result.OK || !result.State.Completed || !result.State.ProviderTested || result.State.State != OnboardingStateComplete {
		t.Fatalf("complete onboarding result = %#v, want successful complete state", result)
	}
	if requests.Load() != 1 || authorization.Load() != "Bearer "+secret {
		t.Fatalf("fake provider requests = %d authorization = %q", requests.Load(), authorization.Load())
	}

	configContent, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configContent), secret) {
		t.Fatalf("config leaked API key: %s", configContent)
	}
	loaded, err := config.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Onboarding.Completed || !loaded.Onboarding.ProviderTested {
		t.Fatalf("persisted onboarding = %#v, want complete after probe", loaded.Onboarding)
	}

	// A fresh bridge over the same config represents an application restart.
	restarted := &Bridge{paths: paths, config: loaded, keyring: store}
	restartedState := restarted.GetOnboardingState()
	if restartedState != result.State {
		t.Fatalf("restarted onboarding state = %#v, want %#v", restartedState, result.State)
	}
	legacyResult := restarted.TestProvider(ProviderSettingsInput{
		ProviderID: "main", Kind: config.ProviderOpenAICompatible, TimeoutSeconds: 2,
	})
	if !legacyResult.OK || !legacyResult.Onboarding.Completed || !legacyResult.Onboarding.ProviderTested {
		t.Fatalf("legacy TestProvider result = %#v, want successful completed state", legacyResult)
	}
	if requests.Load() != 2 {
		t.Fatalf("provider requests after legacy TestProvider = %d, want 2", requests.Load())
	}
	storedSecret, err := store.Get(context.Background(), "provider.main.api-key")
	if err != nil || storedSecret != secret {
		t.Fatalf("keyring secret = %q, %v", storedSecret, err)
	}
}

func TestFailedProviderProbeLeavesOnboardingPendingAndRedactsError(t *testing.T) {
	const secret = "sk-failed-onboarding-secret"
	server := newIPv4ProviderTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprintf(writer, `{"error":{"message":"upstream secret %s"}}`, secret)
	}))
	defer server.Close()

	paths := providerTestPaths(t)
	store, err := securitykeyring.NewWithBackend("test.onboarding.failure", &providerTestKeyring{values: make(map[string]string)})
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{paths: paths, config: config.Default(paths), keyring: store}
	if _, err := bridge.SaveOpenAIProvider(SaveOpenAIProviderInput{
		ID: "main", BaseURL: server.URL + "/v1", Model: "local-model", APIKey: secret, Enabled: true,
	}); err != nil {
		t.Fatalf("save provider: %v", err)
	}

	result := bridge.ProbeProvider(ProviderProbeInput{ProviderID: "main", Kind: config.ProviderOpenAICompatible, TimeoutSeconds: 2})
	if result.OK || result.Onboarding.Completed || result.Onboarding.ProviderTested || result.Onboarding.State != OnboardingStatePending {
		t.Fatalf("failed probe result = %#v, want pending failure", result)
	}
	if strings.Contains(result.Message, secret) || strings.Contains(result.Message, "upstream") || strings.Contains(result.Message, "secret") {
		t.Fatalf("provider error was not redacted: %q", result.Message)
	}
	loaded, err := config.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Onboarding.Completed || loaded.Onboarding.ProviderTested {
		t.Fatalf("failed probe persisted onboarding = %#v", loaded.Onboarding)
	}
}

func providerTestPaths(t *testing.T) config.Paths {
	t.Helper()
	root := t.TempDir()
	configDirectory := filepath.Join(root, "config")
	dataDirectory := filepath.Join(root, "data")
	return config.Paths{
		ConfigDirectory: configDirectory,
		ConfigFile:      filepath.Join(configDirectory, "config.json"),
		DataDirectory:   dataDirectory,
	}
}

func writeProviderProbeSSE(writer http.ResponseWriter, flusher http.Flusher, event string, value map[string]any) {
	data, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event, data)
	flusher.Flush()
}

func newIPv4ProviderTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for local provider: %v", err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	return server
}
