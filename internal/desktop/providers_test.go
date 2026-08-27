package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/config"
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
}
