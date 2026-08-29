// Package config owns non-secret local application configuration.
// Provider credentials are represented only by opaque keyring references.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	appDirectoryName = "Yuri"
	configFileName   = "config.json"

	// TestModeEnv and TestProfileRootEnv are intentionally a paired,
	// test-only escape hatch for launch smoke tests. A profile root is never
	// honored unless the caller opts into the exact test mode value, so a
	// stray environment variable cannot redirect a normal owner profile.
	TestModeEnv        = "YURI_TEST_MODE"
	TestProfileRootEnv = "YURI_TEST_PROFILE_ROOT"
	testModeValue      = "1"
)

// Config contains process-level settings that are safe to persist as JSON.
// Feature-specific settings will be added through versioned migrations.
type Config struct {
	Version            int               `json:"version"`
	Locale             string            `json:"locale"`
	LogLevel           string            `json:"log_level"`
	DataDirectory      string            `json:"data_directory"`
	AllowedDirectories []string          `json:"allowed_directories,omitempty"`
	Providers          []ProviderConfig  `json:"providers,omitempty"`
	Onboarding         OnboardingConfig  `json:"onboarding"`
	Voice              VoiceConfig       `json:"voice,omitempty"`
	PluginDevMode      bool              `json:"plugin_dev_mode,omitempty"`
	Proactivity        ProactivityConfig `json:"proactivity"`
	Persona            PersonaConfig     `json:"persona"`
}

// OnboardingConfig contains only durable first-run lifecycle state. A profile
// is complete only after the desktop bridge has successfully probed a saved
// provider; saving provider metadata alone must not advance this flag.
//
// The field is additive to config version 1. Load starts from Default, so
// legacy config files without an onboarding object remain valid and safely
// default to an incomplete first run.
type OnboardingConfig struct {
	Completed       bool `json:"completed"`
	ProviderTested  bool `json:"provider_tested"`
	AgentConfigured bool `json:"agent_configured"`
}

type ProviderKind string

const (
	ProviderOpenAICompatible ProviderKind = "openai-compatible"
	ProviderCodexAppServer   ProviderKind = "codex-app-server"
	ProviderAntigravity      ProviderKind = "antigravity"
)

// ProviderConfig contains only non-secret metadata. CredentialRef addresses
// a system-keyring item and is never an API key itself.
type ProviderConfig struct {
	ID            string       `json:"id"`
	Kind          ProviderKind `json:"kind"`
	DisplayName   string       `json:"display_name"`
	BaseURL       string       `json:"base_url,omitempty"`
	Model         string       `json:"model,omitempty"`
	CredentialRef string       `json:"credential_ref,omitempty"`
	Binary        string       `json:"binary,omitempty"`
	Enabled       bool         `json:"enabled"`
}

type VoiceConfig struct {
	Enabled                 bool   `json:"enabled"`
	TranscriptionProviderID string `json:"transcription_provider_id,omitempty"`
	SpeechProviderID        string `json:"speech_provider_id,omitempty"`
	TranscriptionModel      string `json:"transcription_model,omitempty"`
	SpeechModel             string `json:"speech_model,omitempty"`
	Voice                   string `json:"voice,omitempty"`
}

// ProactivityConfig is persisted as simple UI-facing values. Runtime policy
// adapters convert cooldown minutes to durations and never store transient
// delivery counters in this file.
type ProactivityConfig struct {
	Enabled                 bool   `json:"enabled"`
	QuietHoursEnabled       bool   `json:"quiet_hours_enabled"`
	QuietHoursStart         string `json:"quiet_hours_start"`
	QuietHoursEnd           string `json:"quiet_hours_end"`
	Timezone                string `json:"timezone"`
	DailyLimit              int    `json:"daily_limit"`
	CooldownMinutes         int    `json:"cooldown_minutes"`
	AllowLocalNotifications bool   `json:"allow_local_notifications"`
}

// PersonaConfig contains owner-controlled switches for the mutable persona.
// ProfileID is a local logical identifier, not a user or account identifier:
// Yuri is intentionally a single-owner application.
type PersonaConfig struct {
	ProfileID                 string `json:"profile_id"`
	AutoEvolution             bool   `json:"auto_evolution"`
	ReflectionCooldownMinutes int    `json:"reflection_cooldown_minutes"`
}

// Paths contains all local roots owned by the single-user Yuri installation.
type Paths struct {
	ConfigDirectory string
	ConfigFile      string
	DataDirectory   string
	DatabaseFile    string
	PebbleDirectory string
	BlobDirectory   string
	LogDirectory    string
	PluginDirectory string
}

// WithDataDirectory returns paths whose durable stores live under directory.
func (p Paths) WithDataDirectory(directory string) Paths {
	p.DataDirectory = directory
	p.DatabaseFile = filepath.Join(directory, "yuri.sqlite3")
	p.PebbleDirectory = filepath.Join(directory, "pebble")
	p.BlobDirectory = filepath.Join(directory, "blobs")
	p.LogDirectory = filepath.Join(directory, "logs")
	p.PluginDirectory = filepath.Join(directory, "plugins")
	return p
}

// DefaultPaths resolves platform-standard per-user directories.
func DefaultPaths() (Paths, error) {
	if paths, enabled, err := testPathsFromEnvironment(); enabled || err != nil {
		return paths, err
	}
	configRoot, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user config directory: %w", err)
	}

	dataRoot, err := userDataRoot()
	if err != nil {
		return Paths{}, err
	}

	configDirectory := filepath.Join(configRoot, appDirectoryName)
	dataDirectory := filepath.Join(dataRoot, appDirectoryName)
	return pathsForDirectories(configDirectory, dataDirectory), nil
}

func testPathsFromEnvironment() (Paths, bool, error) {
	mode, modeSet := os.LookupEnv(TestModeEnv)
	root, rootSet := os.LookupEnv(TestProfileRootEnv)
	mode = strings.TrimSpace(mode)
	root = strings.TrimSpace(root)

	if !modeSet && !rootSet {
		return Paths{}, false, nil
	}
	if mode != testModeValue {
		return Paths{}, true, fmt.Errorf("%s must be %q when test profile paths are configured", TestModeEnv, testModeValue)
	}
	if !rootSet || root == "" {
		return Paths{}, true, fmt.Errorf("%s is required when %s=%q", TestProfileRootEnv, TestModeEnv, testModeValue)
	}
	if !filepath.IsAbs(root) {
		return Paths{}, true, fmt.Errorf("%s must be an absolute path", TestProfileRootEnv)
	}
	root = filepath.Clean(root)
	if filepath.Dir(root) == root {
		return Paths{}, true, fmt.Errorf("%s must name an isolated profile directory", TestProfileRootEnv)
	}

	configDirectory := filepath.Join(root, "config")
	dataDirectory := filepath.Join(root, "data")
	return pathsForDirectories(configDirectory, dataDirectory), true, nil
}

func pathsForDirectories(configDirectory, dataDirectory string) Paths {
	return Paths{
		ConfigDirectory: configDirectory,
		ConfigFile:      filepath.Join(configDirectory, configFileName),
		DataDirectory:   dataDirectory,
		DatabaseFile:    filepath.Join(dataDirectory, "yuri.sqlite3"),
		PebbleDirectory: filepath.Join(dataDirectory, "pebble"),
		BlobDirectory:   filepath.Join(dataDirectory, "blobs"),
		LogDirectory:    filepath.Join(dataDirectory, "logs"),
		PluginDirectory: filepath.Join(dataDirectory, "plugins"),
	}
}

func userDataRoot() (string, error) {
	if runtime.GOOS == "windows" {
		if value := os.Getenv("LOCALAPPDATA"); value != "" {
			return value, nil
		}
	}
	return os.UserConfigDir()
}

// Default returns the initial non-secret configuration.
func Default(paths Paths) Config {
	return Config{
		Version:       1,
		Locale:        "ru-RU",
		LogLevel:      "info",
		DataDirectory: paths.DataDirectory,
		Onboarding:    OnboardingConfig{},
		Proactivity: ProactivityConfig{
			Enabled: false, QuietHoursEnabled: true, QuietHoursStart: "23:00", QuietHoursEnd: "07:00",
			Timezone: "UTC", DailyLimit: 5, CooldownMinutes: 30, AllowLocalNotifications: true,
		},
		Persona: PersonaConfig{
			ProfileID: "owner", AutoEvolution: true, ReflectionCooldownMinutes: 60,
		},
	}
}

// Load reads a config file. A missing file yields defaults and is not an error.
func Load(paths Paths) (Config, error) {
	value := Default(paths)
	content, err := os.ReadFile(paths.ConfigFile)
	if errors.Is(err, os.ErrNotExist) {
		return value, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(content, &value); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	// Config files created before Stage 4 have no proactivity object. Preserve
	// backward compatibility while keeping new installations deny-by-default.
	if strings.TrimSpace(value.Proactivity.Timezone) == "" {
		value.Proactivity = Default(paths).Proactivity
	}
	// Stage 4 and older config files have no persona object. An empty local
	// profile ID is reserved as the migration signal so an explicit false
	// AutoEvolution setting is preserved on subsequent loads.
	if strings.TrimSpace(value.Persona.ProfileID) == "" {
		value.Persona = Default(paths).Persona
	}
	// Stage 7 considered a successful provider probe sufficient to complete
	// onboarding. Those installations already have the legacy owner persona,
	// which the desktop bridge migrates to an AgentProfile on startup. Treat
	// the missing Stage 8 flag as configured so upgrades do not reopen first
	// run or fail validation before the database migration can execute.
	if value.Onboarding.Completed && value.Onboarding.ProviderTested && !value.Onboarding.AgentConfigured {
		value.Onboarding.AgentConfigured = true
	}
	// Early Codex OAuth builds persisted model placeholders or copied the
	// generic OpenAI-compatible default. Clear only those legacy values; models
	// explicitly selected from model/list remain durable.
	for index := range value.Providers {
		if value.Providers[index].Kind == ProviderCodexAppServer && legacyCodexModel(value.Providers[index].Model) {
			value.Providers[index].Model = ""
		}
	}
	if err := value.Validate(); err != nil {
		return Config{}, err
	}
	return value, nil
}

func legacyCodexModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "codex-default", "gpt-5-codex", "gpt-4o-mini":
		return true
	default:
		return false
	}
}

// Save writes configuration atomically with owner-only permissions.
func Save(paths Paths, value Config) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.ConfigDirectory, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	content = append(content, '\n')
	temporary, err := os.CreateTemp(paths.ConfigDirectory, "config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)

	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set config permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	if err := os.Rename(temporaryName, paths.ConfigFile); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// Validate rejects configuration that cannot be safely consumed.
func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	if c.Locale == "" {
		return errors.New("locale is required")
	}
	if c.DataDirectory == "" || !filepath.IsAbs(c.DataDirectory) {
		return errors.New("data_directory must be an absolute path")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("unsupported log level %q", c.LogLevel)
	}
	allowedDirectories := make(map[string]struct{}, len(c.AllowedDirectories))
	for _, directory := range c.AllowedDirectories {
		cleaned := filepath.Clean(strings.TrimSpace(directory))
		if cleaned == "." || !filepath.IsAbs(cleaned) {
			return fmt.Errorf("allowed directory must be an absolute path: %q", directory)
		}
		if _, exists := allowedDirectories[cleaned]; exists {
			return fmt.Errorf("duplicate allowed directory %q", cleaned)
		}
		allowedDirectories[cleaned] = struct{}{}
	}
	seen := make(map[string]struct{}, len(c.Providers))
	for _, provider := range c.Providers {
		if provider.ID == "" || strings.ContainsAny(provider.ID, " /\\") {
			return fmt.Errorf("invalid provider id %q", provider.ID)
		}
		if _, found := seen[provider.ID]; found {
			return fmt.Errorf("duplicate provider id %q", provider.ID)
		}
		seen[provider.ID] = struct{}{}
		switch provider.Kind {
		case ProviderOpenAICompatible:
			if provider.Model == "" || provider.CredentialRef == "" {
				return fmt.Errorf("provider %q requires model and credential_ref", provider.ID)
			}
			if err := validateRemoteURL(provider.BaseURL); err != nil {
				return fmt.Errorf("provider %q: %w", provider.ID, err)
			}
		case ProviderCodexAppServer:
			if provider.CredentialRef != "" {
				return fmt.Errorf("provider %q must not configure a credential_ref", provider.ID)
			}
		case ProviderAntigravity:
			return fmt.Errorf("provider %q cannot be persisted: Antigravity OAuth is unavailable without an official integration contract", provider.ID)
		default:
			return fmt.Errorf("provider %q has unsupported kind %q", provider.ID, provider.Kind)
		}
	}
	if err := c.Proactivity.Validate(); err != nil {
		return err
	}
	if err := c.Persona.Validate(); err != nil {
		return err
	}
	if err := c.Onboarding.Validate(); err != nil {
		return err
	}
	return nil
}

func (c OnboardingConfig) Validate() error {
	if c.Completed != (c.ProviderTested && c.AgentConfigured) {
		return errors.New("onboarding completed requires both provider_tested and agent_configured")
	}
	return nil
}

func (c PersonaConfig) Validate() error {
	profileID := strings.TrimSpace(c.ProfileID)
	if profileID == "" || strings.ContainsAny(profileID, " /\\") {
		return fmt.Errorf("invalid local persona profile id %q", c.ProfileID)
	}
	if c.ReflectionCooldownMinutes < 0 || c.ReflectionCooldownMinutes > 30*24*60 {
		return errors.New("persona reflection_cooldown_minutes must be between 0 and 43200")
	}
	return nil
}

func (c ProactivityConfig) Validate() error {
	if _, err := time.LoadLocation(strings.TrimSpace(c.Timezone)); err != nil {
		return fmt.Errorf("invalid proactivity timezone %q: %w", c.Timezone, err)
	}
	if c.DailyLimit < 0 || c.DailyLimit > 10_000 {
		return errors.New("proactivity daily_limit must be between 0 and 10000")
	}
	if c.CooldownMinutes < 0 || c.CooldownMinutes > 365*24*60 {
		return errors.New("proactivity cooldown_minutes is outside the supported range")
	}
	if c.QuietHoursEnabled {
		if !validClockTime(c.QuietHoursStart) || !validClockTime(c.QuietHoursEnd) {
			return errors.New("proactivity quiet hours must use HH:MM")
		}
	}
	return nil
}

func validClockTime(value string) bool {
	if len(value) != 5 || value[2] != ':' {
		return false
	}
	parsed, err := time.Parse("15:04", value)
	return err == nil && parsed.Format("15:04") == value
}

func validateRemoteURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return errors.New("base_url is invalid")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("base_url must not contain credentials, query, or fragment")
	}
	loopback := strings.EqualFold(parsed.Hostname(), "localhost") || net.ParseIP(parsed.Hostname()).IsLoopback()
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
		return errors.New("base_url must use HTTPS outside localhost")
	}
	return nil
}
