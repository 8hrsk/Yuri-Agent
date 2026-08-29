package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/config"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func TestCodexBridgeAccountLifecycleSmoke(t *testing.T) {
	const tokenCanary = "codex-oauth-token-canary-7d91"
	root := t.TempDir()
	markerPath := filepath.Join(root, "codex-methods.log")
	t.Setenv("YURI_TEST_CODEX_MARKER", markerPath)
	t.Setenv("YURI_TEST_CODEX_TOKEN", tokenCanary)
	binary := buildFakeCodexAppServer(t, root)
	paths := config.Paths{
		ConfigDirectory: filepath.Join(root, "config"), ConfigFile: filepath.Join(root, "config", "config.json"),
		DataDirectory: filepath.Join(root, "data"), DatabaseFile: filepath.Join(root, "data", "yuri.sqlite3"),
		BlobDirectory: filepath.Join(root, "data", "blobs"), LogDirectory: filepath.Join(root, "data", "logs"),
		PluginDirectory: filepath.Join(root, "data", "plugins"), PebbleDirectory: filepath.Join(root, "data", "pebble"),
	}
	if err := os.MkdirAll(paths.DataDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := storage.Open(context.Background(), paths.DatabaseFile)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	value := config.Default(paths)
	value.Onboarding.AgentConfigured = true
	bridge := &Bridge{paths: paths, config: value, database: database}
	defer func() {
		bridge.mu.Lock()
		client := bridge.codex
		bridge.codex = nil
		bridge.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
	}()

	provider, err := bridge.SaveCodexProvider(SaveCodexProviderInput{
		ID: "codex", DisplayName: "Codex fake app-server", Model: "codex-default", Binary: binary, Enabled: true,
	})
	if err != nil {
		t.Fatalf("save Codex provider: %v", err)
	}
	if provider.HasSecret || provider.Binary != binary {
		t.Fatalf("Codex provider exposed a secret or lost binary: %#v", provider)
	}
	login, err := bridge.StartCodexLogin("device-code")
	if err != nil || login.LoginID != "login-fake-codex" || login.UserCode != "YURI-CODE" {
		t.Fatalf("Codex login metadata = %#v err=%v", login, err)
	}
	account, err := bridge.CodexAccount()
	if err != nil || account.Account == nil || account.Account.PlanType == nil || *account.Account.PlanType != "plus" {
		t.Fatalf("Codex account = %#v err=%v", account, err)
	}
	limits, err := bridge.CodexRateLimits()
	if err != nil || limits.RateLimits == nil || limits.RateLimits.Primary == nil || limits.RateLimits.Primary.UsedPercent != 25 {
		t.Fatalf("Codex rate limits = %#v err=%v", limits, err)
	}
	models, err := bridge.CodexModels()
	if err != nil || len(models) != 1 || models[0].Model != "gpt-test-default" || !models[0].IsDefault {
		t.Fatalf("Codex models = %#v err=%v", models, err)
	}
	probe := bridge.ProbeProvider(ProviderProbeInput{ProviderID: "codex", Kind: config.ProviderCodexAppServer})
	if !probe.OK || !probe.Onboarding.Completed || !probe.Onboarding.ProviderTested {
		t.Fatalf("Codex provider probe = %#v", probe)
	}
	logout, err := bridge.CodexLogout()
	if err != nil || !logout.Disconnected || logout.Onboarding.Completed || logout.Onboarding.ProviderTested {
		t.Fatalf("Codex logout = %#v err=%v", logout, err)
	}

	viewsJSON, err := json.Marshal(struct {
		Provider any `json:"provider"`
		Login    any `json:"login"`
		Account  any `json:"account"`
		Limits   any `json:"limits"`
		Logout   any `json:"logout"`
	}{provider, login, account, limits, logout})
	if err != nil {
		t.Fatal(err)
	}
	assertBytesDoNotContain(t, "Codex Bridge DTOs", viewsJSON, tokenCanary)
	configContent, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	assertBytesDoNotContain(t, "Codex config", configContent, tokenCanary)
	if _, err := database.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.DatabaseFile, paths.DatabaseFile + "-wal", paths.DatabaseFile + "-shm"} {
		content, readErr := os.ReadFile(path)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		assertBytesDoNotContain(t, filepath.Base(path), content, tokenCanary)
	}
	loaded, err := config.Load(paths)
	if err != nil || loaded.Onboarding.Completed || loaded.Onboarding.ProviderTested || len(loaded.Providers) != 1 || loaded.Providers[0].CredentialRef != "" {
		t.Fatalf("durable Codex logout/config state = %#v err=%v", loaded, err)
	}
	waitForCodexMethods(t, markerPath, []string{
		"initialize", "initialized", "account/login/start", "account/read",
		"account/rateLimits/read", "model/list", "account/logout",
	}, 2*time.Second)
}

func buildFakeCodexAppServer(t *testing.T, root string) string {
	t.Helper()
	binary := filepath.Join(root, "fake-codex")
	command := exec.Command("go", "build", "-o", binary, "./testdata/fakecodex")
	command.Dir = filepath.Dir(executableSourcePath(t))
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fake Codex app-server: %v\n%s", err, output)
	}
	return binary
}

func assertBytesDoNotContain(t *testing.T, name string, content []byte, canary string) {
	t.Helper()
	if bytes.Contains(content, []byte(canary)) {
		t.Fatalf("%s retained OAuth token canary", name)
	}
}

func waitForCodexMethods(t *testing.T, path string, required []string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil {
			value := string(content)
			complete := true
			for _, method := range required {
				if !strings.Contains(value, method+"\n") {
					complete = false
					break
				}
			}
			if complete {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	content, _ := os.ReadFile(path)
	t.Fatalf("Codex app-server method trace incomplete: %s", content)
}
