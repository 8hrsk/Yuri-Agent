// Package plugin contains the public, dependency-free SDK for Yuri plugins.
//
// A plugin is an independently built process.  The SDK deliberately only
// depends on the Go standard library so that plugin authors can use it without
// importing Yuri's private application packages or tying their binary to the
// core ABI.
package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	// ManifestSchemaVersion is the schema version of plugin.json understood by
	// the SDK.  A future schema may add fields, but must keep old manifests
	// readable by the core.
	ManifestSchemaVersion = "1.0"

	// ProtocolVersion identifies the versioned JSON-lines wire contract.
	ProtocolVersion = "1.0"
	ProtocolName    = "yuri.plugin.v1"

	// CoreVersion is intentionally not hard-coded in the SDK.  The host owns
	// compatibility with a concrete Yuri release; manifests declare a range.
	MaxIdentifierLength = 128
	MaxNameLength       = 256
	MaxDescriptionBytes = 16 << 10
	MaxSchemaBytes      = 256 << 10
)

var (
	identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,127}$`)
	semverPattern     = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	sha256Pattern     = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
)

// Known capabilities are the stable capability names that a manifest may
// request.  A grant is still required at runtime: declaring a capability is
// never equivalent to receiving it.
const (
	CapabilityFilesystemRead    = "filesystem.read"
	CapabilityFilesystemWrite   = "filesystem.write"
	CapabilityFilesystemDelete  = "filesystem.delete"
	CapabilityNetworkHTTP       = "network.http"
	CapabilitySecretsUse        = "secrets.use"
	CapabilityNotificationsSend = "notifications.send"
	CapabilitySchedulerManage   = "scheduler.manage"
	CapabilityMemoryRead        = "memory.read"
	CapabilityMemoryWrite       = "memory.write"
	CapabilityMemoryDelete      = "memory.delete"
	CapabilityExternalSend      = "external.send"
)

var knownCapabilities = map[string]struct{}{
	CapabilityFilesystemRead:    {},
	CapabilityFilesystemWrite:   {},
	CapabilityFilesystemDelete:  {},
	CapabilityNetworkHTTP:       {},
	CapabilitySecretsUse:        {},
	CapabilityNotificationsSend: {},
	CapabilitySchedulerManage:   {},
	CapabilityMemoryRead:        {},
	CapabilityMemoryWrite:       {},
	CapabilityMemoryDelete:      {},
	CapabilityExternalSend:      {},
}

// RiskLevel is a product-level hint.  The host remains responsible for the
// final policy decision and confirmation flow.
type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

func (r RiskLevel) Valid() bool {
	switch r {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return true
	default:
		return false
	}
}

// Manifest describes a plugin package.  The executable path is package
// relative and is never trusted until the host canonicalizes it against the
// installed package root.
type Manifest struct {
	SchemaVersion string `json:"schema_version"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	Version       string `json:"version"`
	Publisher     string `json:"publisher"`
	Description   string `json:"description,omitempty"`
	Executable    string `json:"executable"`

	// SupportedOS and SupportedArch are retained as explicit lists because a
	// package can contain one binary selected for the current target. Platforms
	// is optional richer metadata for packages containing several assets.
	SupportedOS   []string   `json:"supported_os"`
	SupportedArch []string   `json:"supported_arch"`
	Platforms     []Platform `json:"platforms,omitempty"`

	ProtocolVersion string `json:"protocol_version"`
	MinCoreVersion  string `json:"min_core_version"`
	MaxCoreVersion  string `json:"max_core_version"`

	Tools       []ToolDefinition  `json:"tools"`
	Events      []EventDefinition `json:"event_sources"`
	Permissions []Permission      `json:"permissions"`

	Repository    *Repository    `json:"repository,omitempty"`
	ReleaseAssets []ReleaseAsset `json:"release_assets,omitempty"`
	Checksum      *Checksum      `json:"checksum,omitempty"`
	Signature     *Signature     `json:"signature,omitempty"`
}

// Platform identifies an OS/architecture combination supported by a package.
type Platform struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	Executable string `json:"executable,omitempty"`
}

// ToolDefinition declares a callable tool. InputSchema and OutputSchema are
// JSON Schema documents, kept as raw JSON to avoid bringing a schema library
// into every plugin binary.
type ToolDefinition struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Risk         RiskLevel       `json:"risk"`
	Permissions  []string        `json:"permissions,omitempty"`
}

// EventDefinition declares an event source exposed by a plugin.
type EventDefinition struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
	Permissions []string        `json:"permissions,omitempty"`
}

// Permission is a requested capability and optional declarative scope. Scope
// is interpreted by the host policy layer, never by the plugin itself.
type Permission struct {
	Capability string          `json:"capability"`
	Reason     string          `json:"reason"`
	Scope      json.RawMessage `json:"scope,omitempty"`
}

type Repository struct {
	URL        string `json:"url"`
	Source     string `json:"source,omitempty"`
	Commit     string `json:"commit,omitempty"`
	ReleaseTag string `json:"release_tag,omitempty"`
}

type ReleaseAsset struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Checksum string `json:"checksum,omitempty"`
}

type Checksum struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

type Signature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Value     string `json:"value"`
}

// Validate checks structural and security-relevant manifest invariants. It
// intentionally does not check core-version range compatibility because the
// host knows its actual version and release channel.
func (m Manifest) Validate() error {
	if m.SchemaVersion == "" {
		m.SchemaVersion = ManifestSchemaVersion
	}
	if m.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("manifest.schema_version: unsupported version %q", m.SchemaVersion)
	}
	if err := validateIdentifier("manifest.id", m.ID); err != nil {
		return err
	}
	if err := validateName("manifest.name", m.Name); err != nil {
		return err
	}
	if !semverPattern.MatchString(m.Version) {
		return fmt.Errorf("manifest.version: expected semantic version, got %q", m.Version)
	}
	if strings.TrimSpace(m.Publisher) == "" || len(m.Publisher) > MaxNameLength {
		return fmt.Errorf("manifest.publisher: must be 1..%d characters", MaxNameLength)
	}
	if len(m.Description) > MaxDescriptionBytes {
		return fmt.Errorf("manifest.description: exceeds %d bytes", MaxDescriptionBytes)
	}
	if err := validateRelativePath("manifest.executable", m.Executable); err != nil {
		return err
	}
	if len(m.SupportedOS) == 0 || len(m.SupportedArch) == 0 {
		return errors.New("manifest.supported_os and manifest.supported_arch are required")
	}
	if err := validateUniqueStrings("manifest.supported_os", m.SupportedOS, validOS); err != nil {
		return err
	}
	if err := validateUniqueStrings("manifest.supported_arch", m.SupportedArch, validArch); err != nil {
		return err
	}
	if m.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("manifest.protocol_version: expected %q, got %q", ProtocolVersion, m.ProtocolVersion)
	}
	if err := validateVersionRange("manifest.min_core_version", m.MinCoreVersion, true); err != nil {
		return err
	}
	if err := validateVersionRange("manifest.max_core_version", m.MaxCoreVersion, true); err != nil {
		return err
	}
	if m.MinCoreVersion != "" && m.MaxCoreVersion != "" && compareSemver(m.MinCoreVersion, m.MaxCoreVersion) > 0 {
		return errors.New("manifest.min_core_version: must not exceed max_core_version")
	}

	seenTools := make(map[string]struct{}, len(m.Tools))
	for i, tool := range m.Tools {
		if err := tool.validate(i); err != nil {
			return err
		}
		if _, exists := seenTools[tool.ID]; exists {
			return fmt.Errorf("manifest.tools[%d].id: duplicate %q", i, tool.ID)
		}
		seenTools[tool.ID] = struct{}{}
	}
	seenEvents := make(map[string]struct{}, len(m.Events))
	for i, event := range m.Events {
		if err := event.validate(i); err != nil {
			return err
		}
		if _, exists := seenEvents[event.ID]; exists {
			return fmt.Errorf("manifest.event_sources[%d].id: duplicate %q", i, event.ID)
		}
		seenEvents[event.ID] = struct{}{}
	}
	seenPermissions := make(map[string]struct{}, len(m.Permissions))
	for i, permission := range m.Permissions {
		if err := permission.validate(i); err != nil {
			return err
		}
		if _, exists := seenPermissions[permission.Capability]; exists {
			return fmt.Errorf("manifest.permissions[%d].capability: duplicate %q", i, permission.Capability)
		}
		seenPermissions[permission.Capability] = struct{}{}
	}
	for i, platform := range m.Platforms {
		if err := platform.validate(i); err != nil {
			return err
		}
	}
	if m.Repository != nil {
		if err := m.Repository.validate(); err != nil {
			return err
		}
	}
	seenAssets := make(map[string]struct{}, len(m.ReleaseAssets))
	for i, asset := range m.ReleaseAssets {
		if err := asset.validate(i); err != nil {
			return err
		}
		key := asset.OS + "/" + asset.Arch
		if _, exists := seenAssets[key]; exists {
			return fmt.Errorf("manifest.release_assets[%d]: duplicate target %q", i, key)
		}
		seenAssets[key] = struct{}{}
	}
	if m.Checksum != nil {
		if err := m.Checksum.validate(); err != nil {
			return err
		}
	}
	if m.Signature != nil {
		if err := m.Signature.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (t ToolDefinition) validate(index int) error {
	prefix := fmt.Sprintf("manifest.tools[%d]", index)
	if err := validateIdentifier(prefix+".id", t.ID); err != nil {
		return err
	}
	if err := validateName(prefix+".name", t.Name); err != nil {
		return err
	}
	if len(t.Description) > MaxDescriptionBytes {
		return fmt.Errorf("%s.description: exceeds %d bytes", prefix, MaxDescriptionBytes)
	}
	if !t.Risk.Valid() {
		return fmt.Errorf("%s.risk: unknown risk %q", prefix, t.Risk)
	}
	if err := validateSchema(prefix+".input_schema", t.InputSchema); err != nil {
		return err
	}
	if err := validateSchema(prefix+".output_schema", t.OutputSchema); err != nil {
		return err
	}
	if err := validateCapabilities(prefix+".permissions", t.Permissions); err != nil {
		return err
	}
	return nil
}

func (e EventDefinition) validate(index int) error {
	prefix := fmt.Sprintf("manifest.event_sources[%d]", index)
	if err := validateIdentifier(prefix+".id", e.ID); err != nil {
		return err
	}
	if err := validateName(prefix+".name", e.Name); err != nil {
		return err
	}
	if len(e.Description) > MaxDescriptionBytes {
		return fmt.Errorf("%s.description: exceeds %d bytes", prefix, MaxDescriptionBytes)
	}
	if err := validateSchema(prefix+".schema", e.Schema); err != nil {
		return err
	}
	return validateCapabilities(prefix+".permissions", e.Permissions)
}

func (p Permission) validate(index int) error {
	prefix := fmt.Sprintf("manifest.permissions[%d]", index)
	if _, ok := knownCapabilities[p.Capability]; !ok {
		return fmt.Errorf("%s.capability: unknown capability %q", prefix, p.Capability)
	}
	if strings.TrimSpace(p.Reason) == "" || len(p.Reason) > MaxDescriptionBytes {
		return fmt.Errorf("%s.reason: must be 1..%d bytes", prefix, MaxDescriptionBytes)
	}
	if len(p.Scope) > 0 && !json.Valid(p.Scope) {
		return fmt.Errorf("%s.scope: invalid JSON", prefix)
	}
	return nil
}

func (p Platform) validate(index int) error {
	prefix := fmt.Sprintf("manifest.platforms[%d]", index)
	if !validOS(p.OS) {
		return fmt.Errorf("%s.os: unsupported OS %q", prefix, p.OS)
	}
	if !validArch(p.Arch) {
		return fmt.Errorf("%s.arch: unsupported architecture %q", prefix, p.Arch)
	}
	if p.Executable != "" {
		return validateRelativePath(prefix+".executable", p.Executable)
	}
	return nil
}

func (r Repository) validate() error {
	parsed, err := url.Parse(r.URL)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return errors.New("manifest.repository.url: must be an absolute URL without credentials")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("manifest.repository.url: unsupported scheme %q", parsed.Scheme)
	}
	if parsed.Scheme == "http" && parsed.Host != "localhost" && parsed.Host != "127.0.0.1" {
		return errors.New("manifest.repository.url: HTTPS is required except for localhost")
	}
	if r.Commit != "" && len(r.Commit) > 128 {
		return errors.New("manifest.repository.commit: too long")
	}
	return nil
}

func (a ReleaseAsset) validate(index int) error {
	prefix := fmt.Sprintf("manifest.release_assets[%d]", index)
	if !validOS(a.OS) {
		return fmt.Errorf("%s.os: unsupported OS %q", prefix, a.OS)
	}
	if !validArch(a.Arch) {
		return fmt.Errorf("%s.arch: unsupported architecture %q", prefix, a.Arch)
	}
	if strings.TrimSpace(a.Filename) == "" || strings.ContainsAny(a.Filename, `/\\`) {
		return fmt.Errorf("%s.filename: must be a plain filename", prefix)
	}
	parsed, err := url.Parse(a.URL)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("%s.url: must be an absolute URL without credentials", prefix)
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Host == "localhost" || parsed.Host == "127.0.0.1")) {
		return fmt.Errorf("%s.url: HTTPS is required except for localhost", prefix)
	}
	if a.Checksum != "" && !sha256Pattern.MatchString(a.Checksum) {
		return fmt.Errorf("%s.checksum: expected SHA-256 hex", prefix)
	}
	return nil
}

func (c Checksum) validate() error {
	if strings.ToLower(c.Algorithm) != "sha256" {
		return fmt.Errorf("manifest.checksum.algorithm: unsupported algorithm %q", c.Algorithm)
	}
	if !sha256Pattern.MatchString(c.Value) {
		return errors.New("manifest.checksum.value: expected SHA-256 hex")
	}
	return nil
}

func (s Signature) validate() error {
	if strings.ToLower(s.Algorithm) != "ed25519" {
		return fmt.Errorf("manifest.signature.algorithm: unsupported algorithm %q", s.Algorithm)
	}
	if strings.TrimSpace(s.KeyID) == "" || len(s.KeyID) > MaxIdentifierLength {
		return errors.New("manifest.signature.key_id: required")
	}
	if strings.TrimSpace(s.Value) == "" {
		return errors.New("manifest.signature.value: required")
	}
	return nil
}

func validateIdentifier(field, value string) error {
	if len(value) == 0 || len(value) > MaxIdentifierLength || !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s: must match %s", field, identifierPattern.String())
	}
	return nil
}

func validateName(field, value string) error {
	if strings.TrimSpace(value) == "" || len(value) > MaxNameLength {
		return fmt.Errorf("%s: must be 1..%d characters", field, MaxNameLength)
	}
	return nil
}

func validateRelativePath(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, 0) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) || isWindowsAbsolutePath(value) {
		return fmt.Errorf("%s: must be a non-empty relative package path", field)
	}
	normalized := path.Clean(strings.ReplaceAll(value, `\`, "/"))
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") {
		return fmt.Errorf("%s: path escapes package root", field)
	}
	return nil
}

func isWindowsAbsolutePath(value string) bool {
	return len(value) >= 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':'
}

func validateSchema(field string, schema json.RawMessage) error {
	if len(schema) == 0 || len(schema) > MaxSchemaBytes || !json.Valid(schema) {
		return fmt.Errorf("%s: must be valid JSON up to %d bytes", field, MaxSchemaBytes)
	}
	var document map[string]any
	if err := json.Unmarshal(schema, &document); err != nil || document == nil {
		return fmt.Errorf("%s: must be a JSON object", field)
	}
	return nil
}

func validateCapabilities(field string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, capability := range values {
		if _, ok := knownCapabilities[capability]; !ok {
			return fmt.Errorf("%s: unknown capability %q", field, capability)
		}
		if _, ok := seen[capability]; ok {
			return fmt.Errorf("%s: duplicate capability %q", field, capability)
		}
		seen[capability] = struct{}{}
	}
	return nil
}

var validOS = func(value string) bool {
	switch value {
	case "darwin", "macos", "windows", "linux":
		return true
	default:
		return false
	}
}

var validArch = func(value string) bool {
	switch value {
	case "amd64", "arm64", "386", "arm":
		return true
	default:
		return false
	}
}

func validateUniqueStrings(field string, values []string, valid func(string) bool) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !valid(value) {
			return fmt.Errorf("%s: unsupported value %q", field, value)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("%s: duplicate value %q", field, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateVersionRange(field, value string, required bool) error {
	if value == "" && !required {
		return nil
	}
	if !semverPattern.MatchString(value) {
		return fmt.Errorf("%s: expected semantic version, got %q", field, value)
	}
	return nil
}

// compareSemver compares the numeric major/minor/patch components. It is
// intentionally only used for the manifest's simple compatibility bounds.
func compareSemver(left, right string) int {
	left = strings.TrimPrefix(left, "v")
	right = strings.TrimPrefix(right, "v")
	var lMajor, lMinor, lPatch, rMajor, rMinor, rPatch int
	_, _ = fmt.Sscanf(left, "%d.%d.%d", &lMajor, &lMinor, &lPatch)
	_, _ = fmt.Sscanf(right, "%d.%d.%d", &rMajor, &rMinor, &rPatch)
	for _, pair := range [][2]int{{lMajor, rMajor}, {lMinor, rMinor}, {lPatch, rPatch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

// MarshalJSON ensures callers do not accidentally emit a manifest with a
// missing schema version. Validation is still explicit so a rejected
// manifest is never silently repaired by the SDK.
func (m Manifest) MarshalJSON() ([]byte, error) {
	type alias Manifest
	if m.SchemaVersion == "" {
		m.SchemaVersion = ManifestSchemaVersion
	}
	if m.Tools == nil {
		m.Tools = []ToolDefinition{}
	}
	if m.Events == nil {
		m.Events = []EventDefinition{}
	}
	if m.Permissions == nil {
		m.Permissions = []Permission{}
	}
	return json.Marshal(alias(m))
}

// DecodeManifest decodes and validates a plugin.json document. Unknown JSON
// fields are rejected so a typo cannot silently weaken a manifest declaration.
func DecodeManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Manifest{}, errors.New("decode manifest: multiple JSON values")
		}
		return Manifest{}, fmt.Errorf("decode manifest: trailing data: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// LoadManifest is the streaming counterpart of DecodeManifest.
func LoadManifest(reader io.Reader) (Manifest, error) {
	if reader == nil {
		return Manifest{}, errors.New("load manifest: nil reader")
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxFrameBytes))
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	if len(data) == MaxFrameBytes {
		return Manifest{}, errors.New("load manifest: document is too large")
	}
	return DecodeManifest(data)
}

// SHA256 returns a lower-case checksum suitable for a release asset or a
// package archive.
func SHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// SortedCapabilities returns a deterministic copy useful in handshake and
// audit payloads.
func SortedCapabilities(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
