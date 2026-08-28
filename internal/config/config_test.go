package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadMissingReturnsDefaults(t *testing.T) {
	paths := testPaths(t)
	value, err := Load(paths)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if value.Locale != "ru-RU" || value.DataDirectory != paths.DataDirectory {
		t.Fatalf("Load() = %#v", value)
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
