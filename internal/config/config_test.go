package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultPathsUsesExplicitIsolatedTestProfile(t *testing.T) {
	root := t.TempDir()
	t.Setenv(TestModeEnv, testModeValue)
	t.Setenv(TestProfileRootEnv, root)

	paths, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths() error = %v", err)
	}
	if paths.ConfigDirectory != filepath.Join(root, "config") || paths.DataDirectory != filepath.Join(root, "data") {
		t.Fatalf("DefaultPaths() = %#v, want isolated config/data roots under %q", paths, root)
	}
	if paths.ConfigFile != filepath.Join(root, "config", configFileName) || paths.DatabaseFile != filepath.Join(root, "data", "yuri.sqlite3") {
		t.Fatalf("DefaultPaths() store paths = %#v", paths)
	}
}

func TestDefaultPathsRejectsProfileOverrideWithoutTestMode(t *testing.T) {
	t.Setenv(TestModeEnv, "")
	t.Setenv(TestProfileRootEnv, t.TempDir())

	if _, err := DefaultPaths(); err == nil {
		t.Fatal("DefaultPaths() accepted a profile override outside explicit test mode")
	}
}

func TestDefaultPathsRejectsIncompleteOrRelativeTestProfile(t *testing.T) {
	t.Setenv(TestModeEnv, testModeValue)
	t.Setenv(TestProfileRootEnv, "")
	if _, err := DefaultPaths(); err == nil {
		t.Fatal("DefaultPaths() accepted test mode without a profile root")
	}

	t.Setenv(TestProfileRootEnv, "relative-profile")
	if _, err := DefaultPaths(); err == nil {
		t.Fatal("DefaultPaths() accepted a relative test profile root")
	}
}

func TestDefaultPathsRejectsFilesystemRootAsTestProfile(t *testing.T) {
	t.Setenv(TestModeEnv, testModeValue)
	t.Setenv(TestProfileRootEnv, string(filepath.Separator))

	if _, err := DefaultPaths(); err == nil {
		t.Fatal("DefaultPaths() accepted the filesystem root as an isolated profile")
	}
}

func TestLoadMissingReturnsDefaults(t *testing.T) {
	paths := testPaths(t)
	value, err := Load(paths)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if value.Locale != "ru-RU" || value.DataDirectory != paths.DataDirectory {
		t.Fatalf("Load() = %#v", value)
	}
	if value.Onboarding.Completed || value.Onboarding.ProviderTested {
		t.Fatalf("clean profile onboarding = %#v, want incomplete", value.Onboarding)
	}
}

func TestLoadLegacyConfigDefaultsToIncompleteOnboarding(t *testing.T) {
	paths := testPaths(t)
	if err := os.MkdirAll(paths.ConfigDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":1,"locale":"ru-RU","log_level":"info","data_directory":"` + paths.DataDirectory + `","providers":[{"id":"main","kind":"openai-compatible","display_name":"Main","base_url":"http://localhost:43111/v1","model":"model","credential_ref":"provider.main.api-key","enabled":true}]}`
	if err := os.WriteFile(paths.ConfigFile, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if value.Onboarding.Completed || value.Onboarding.ProviderTested {
		t.Fatalf("legacy config onboarding = %#v, want incomplete until a probe succeeds", value.Onboarding)
	}
}

func TestLoadClearsLegacyCodexOAuthModel(t *testing.T) {
	paths := testPaths(t)
	if err := os.MkdirAll(paths.ConfigDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":1,"locale":"ru-RU","log_level":"info","data_directory":"` + paths.DataDirectory + `","providers":[{"id":"codex","kind":"codex-app-server","display_name":"Codex App Server","model":"gpt-5-codex","binary":"codex","enabled":true}]}`
	if err := os.WriteFile(paths.ConfigFile, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Providers) != 1 || value.Providers[0].Model != "" {
		t.Fatalf("loaded Codex provider = %#v, want account-default model", value.Providers)
	}
}

func TestConfigRejectsPartialOnboardingTransition(t *testing.T) {
	paths := testPaths(t)
	value := Default(paths)
	value.Onboarding.Completed = true
	if err := value.Validate(); err == nil {
		t.Fatal("Validate() accepted completed onboarding without a successful provider probe")
	}
}

func TestLoadBackfillsStageFourProactivityDefaults(t *testing.T) {
	paths := testPaths(t)
	if err := os.MkdirAll(paths.ConfigDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":1,"locale":"ru-RU","log_level":"info","data_directory":"` + paths.DataDirectory + `"}`
	if err := os.WriteFile(paths.ConfigFile, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if value.Proactivity.Timezone != "UTC" || value.Proactivity.Enabled {
		t.Fatalf("legacy config proactivity = %#v", value.Proactivity)
	}
	if value.Persona.ProfileID != "owner" || !value.Persona.AutoEvolution || value.Persona.ReflectionCooldownMinutes != 60 {
		t.Fatalf("legacy config persona = %#v", value.Persona)
	}
}

func TestPersonaConfigPersistsExplicitDisabledEvolution(t *testing.T) {
	paths := testPaths(t)
	value := Default(paths)
	value.Persona.AutoEvolution = false
	value.Persona.ReflectionCooldownMinutes = 15
	if err := Save(paths, value); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Persona.AutoEvolution || loaded.Persona.ReflectionCooldownMinutes != 15 {
		t.Fatalf("loaded persona = %#v", loaded.Persona)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	paths := testPaths(t)
	want := Default(paths)
	want.LogLevel = "debug"
	want.AllowedDirectories = []string{filepath.Join(t.TempDir(), "documents")}
	if err := Save(paths, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load(paths)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
	info, err := os.Stat(paths.ConfigFile)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestValidateRejectsRelativeAndDuplicateAllowedDirectories(t *testing.T) {
	paths := testPaths(t)
	value := Default(paths)
	value.AllowedDirectories = []string{"relative"}
	if err := value.Validate(); err == nil {
		t.Fatal("Validate() expected relative allowed directory error")
	}
	directory := filepath.Join(t.TempDir(), "documents")
	value.AllowedDirectories = []string{directory, directory}
	if err := value.Validate(); err == nil {
		t.Fatal("Validate() expected duplicate allowed directory error")
	}
}

func TestValidateRejectsRelativeDataDirectory(t *testing.T) {
	value := Config{Version: 1, Locale: "ru-RU", LogLevel: "info", DataDirectory: "relative"}
	if err := value.Validate(); err == nil {
		t.Fatal("Validate() expected error")
	}
}

func TestWithDataDirectoryMovesDurableStores(t *testing.T) {
	paths := testPaths(t)
	directory := filepath.Join(t.TempDir(), "custom-data")
	got := paths.WithDataDirectory(directory)
	if got.DataDirectory != directory || got.DatabaseFile != filepath.Join(directory, "yuri.sqlite3") {
		t.Fatalf("WithDataDirectory() = %#v", got)
	}
	if got.ConfigFile != paths.ConfigFile {
		t.Fatal("WithDataDirectory() unexpectedly moved config file")
	}
}

func TestProviderConfigContainsOnlyKeyringReference(t *testing.T) {
	paths := testPaths(t)
	value := Default(paths)
	value.Providers = []ProviderConfig{{
		ID: "main", Kind: ProviderOpenAICompatible, DisplayName: "Main",
		BaseURL: "https://api.example.com/v1", Model: "model", CredentialRef: "provider.main", Enabled: true,
	}}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := Save(paths, value); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) == "" || contains(string(content), "sk-secret") {
		t.Fatal("config unexpectedly contains credential material")
	}
}

func TestProviderConfigRejectsInsecureRemoteURLAndCodexCredential(t *testing.T) {
	paths := testPaths(t)
	value := Default(paths)
	value.Providers = []ProviderConfig{{
		ID: "main", Kind: ProviderOpenAICompatible, BaseURL: "http://example.com/v1",
		Model: "model", CredentialRef: "provider.main",
	}}
	if err := value.Validate(); err == nil {
		t.Fatal("expected insecure remote URL error")
	}
	value.Providers = []ProviderConfig{{ID: "codex", Kind: ProviderCodexAppServer, CredentialRef: "must-not-exist"}}
	if err := value.Validate(); err == nil {
		t.Fatal("expected Codex credential reference error")
	}
}

func TestConfigRefusesPersistedAntigravityProvider(t *testing.T) {
	paths := testPaths(t)
	value := Default(paths)
	value.Providers = []ProviderConfig{{
		ID: "antigravity", Kind: ProviderAntigravity, DisplayName: "Antigravity", Enabled: true,
	}}
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "official integration contract") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestProactivityConfigValidation(t *testing.T) {
	paths := testPaths(t)
	value := Default(paths)
	value.Proactivity.Enabled = true
	value.Proactivity.Timezone = "Europe/Moscow"
	if err := value.Validate(); err != nil {
		t.Fatalf("valid proactivity config rejected: %v", err)
	}
	value.Proactivity.QuietHoursStart = "9:00"
	if err := value.Validate(); err == nil {
		t.Fatal("invalid quiet hours were accepted")
	}
	value.Proactivity.QuietHoursStart = "23:00"
	value.Proactivity.Timezone = "Mars/Phobos"
	if err := value.Validate(); err == nil {
		t.Fatal("invalid IANA timezone was accepted")
	}
}

func TestPersonaConfigValidation(t *testing.T) {
	paths := testPaths(t)
	value := Default(paths)
	value.Persona.ProfileID = "two owners"
	if err := value.Validate(); err == nil {
		t.Fatal("invalid persona profile id was accepted")
	}
	value.Persona.ProfileID = "owner"
	value.Persona.ReflectionCooldownMinutes = 43_201
	if err := value.Validate(); err == nil {
		t.Fatal("invalid reflection cooldown was accepted")
	}
}

func contains(value, substring string) bool {
	return len(substring) > 0 && len(value) >= len(substring) && strings.Contains(value, substring)
}

func testPaths(t *testing.T) Paths {
	t.Helper()
	root := t.TempDir()
	configDirectory := filepath.Join(root, "config")
	dataDirectory := filepath.Join(root, "data")
	return Paths{
		ConfigDirectory: configDirectory,
		ConfigFile:      filepath.Join(configDirectory, configFileName),
		DataDirectory:   dataDirectory,
		DatabaseFile:    filepath.Join(dataDirectory, "yuri.sqlite3"),
		PebbleDirectory: filepath.Join(dataDirectory, "pebble"),
		BlobDirectory:   filepath.Join(dataDirectory, "blobs"),
		LogDirectory:    filepath.Join(dataDirectory, "logs"),
	}
}
