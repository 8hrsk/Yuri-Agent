package desktop

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/config"
)

func TestWebSearchSettingsRoundTrip(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{ConfigDirectory: filepath.Join(root, "config"), ConfigFile: filepath.Join(root, "config", "config.json"), DataDirectory: filepath.Join(root, "data")}
	bridge := &Bridge{paths: paths, config: config.Default(paths)}
	input := WebSearchSettingsView{Enabled: true, Provider: "searxng", Endpoint: "http://localhost:8080/", DefaultResultLimit: 7}
	if err := bridge.SaveWebSearchSettings(input); err != nil {
		t.Fatal(err)
	}
	want := input
	want.Endpoint = "http://localhost:8080"
	if got := bridge.GetWebSearchSettings(); got != want {
		t.Fatalf("settings = %#v, want %#v", got, want)
	}
	loaded, err := config.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if got := webSearchSettingsView(loaded.WebSearch); got != want {
		t.Fatalf("persisted settings = %#v, want %#v", got, want)
	}
}

func TestWebSearchSettingsProbeUsesCandidateWithoutPersisting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/search" || request.URL.Query().Get("q") != webSearchConnectivityQuery || request.URL.Query().Get("format") != "json" {
			t.Fatalf("probe request = %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"results":[{"title":"Yuri","url":"https://example.com/yuri","content":"ok"}]}`))
	}))
	defer server.Close()
	paths := config.Paths{ConfigDirectory: t.TempDir(), ConfigFile: filepath.Join(t.TempDir(), "config.json"), DataDirectory: t.TempDir()}
	bridge := &Bridge{paths: paths, config: config.Default(paths)}
	result := bridge.TestWebSearchSettings(WebSearchSettingsView{Enabled: true, Provider: "searxng", Endpoint: server.URL, DefaultResultLimit: 5})
	if !result.OK || !strings.Contains(result.Message, "получено результатов: 1") {
		t.Fatalf("probe result = %#v", result)
	}
	if bridge.config.WebSearch.Enabled || bridge.config.WebSearch.Endpoint != "" {
		t.Fatalf("probe persisted candidate settings: %#v", bridge.config.WebSearch)
	}
}

func TestWebSearchSettingsRejectsInsecureRemoteEndpoint(t *testing.T) {
	paths := config.Paths{ConfigDirectory: t.TempDir(), ConfigFile: filepath.Join(t.TempDir(), "config.json"), DataDirectory: t.TempDir()}
	bridge := &Bridge{paths: paths, config: config.Default(paths)}
	if err := bridge.SaveWebSearchSettings(WebSearchSettingsView{Enabled: true, Provider: "searxng", Endpoint: "http://search.example.com", DefaultResultLimit: 5}); err == nil {
		t.Fatal("insecure endpoint was accepted")
	}
}
