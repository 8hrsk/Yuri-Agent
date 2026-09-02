package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

const (
	ManifestSchemaVersion = "1.0"
	ManifestFileName      = "plugin.json"
	MaxManifestBytes      = 1 << 20
)

var (
	identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,127}$`)
	versionPattern    = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	checksumPattern   = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
)

// Manifest describes a plugin package. It intentionally contains only
// declarations: a manifest never grants the declared capabilities by itself.
// The host intersects it with the owner's grants through Authorizer.
type Manifest struct {
	SchemaVersion   string                  `json:"schema_version"`
	ID              string                  `json:"id"`
	Name            string                  `json:"name"`
	Version         string                  `json:"version"`
	Publisher       string                  `json:"publisher"`
	Description     string                  `json:"description,omitempty"`
	Executable      string                  `json:"executable"`
	SupportedOS     []string                `json:"supported_os"`
	SupportedArch   []string                `json:"supported_arch"`
	ProtocolVersion string                  `json:"protocol_version"`
	MinCoreVersion  string                  `json:"min_core_version"`
	MaxCoreVersion  string                  `json:"max_core_version"`
	Tools           []ToolDeclaration       `json:"tools"`
	EventSources    []EventSource           `json:"event_sources"`
	Permissions     []PermissionDeclaration `json:"permissions"`
	Platforms       []Platform              `json:"platforms,omitempty"`
	Repository      *Repository             `json:"repository,omitempty"`
	ReleaseAssets   []ReleaseAsset          `json:"release_assets,omitempty"`
	Checksum        *ChecksumMetadata       `json:"checksum,omitempty"`
	Signature       *SignatureMetadata      `json:"signature,omitempty"`
}

// ToolDeclaration is the capability and schema declaration for one plugin
// tool. Tool invocation is still subject to a host-side authorization check.
type ToolDeclaration struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Description  string           `json:"description,omitempty"`
	InputSchema  json.RawMessage  `json:"input_schema"`
	OutputSchema json.RawMessage  `json:"output_schema"`
	Risk         domain.RiskLevel `json:"risk"`
	Permissions  []string         `json:"permissions,omitempty"`
}

type EventSource struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
	Permissions []string        `json:"permissions,omitempty"`
}

type EventDefinition = EventSource

type PermissionDeclaration struct {
	Capability string          `json:"capability"`
	Scope      json.RawMessage `json:"scope,omitempty"`
	Reason     string          `json:"reason,omitempty"`
}

type SignatureMetadata struct {
	Algorithm string `json:"algorithm,omitempty"`
	KeyID     string `json:"key_id,omitempty"`
	Value     string `json:"value,omitempty"`
}

type ChecksumMetadata struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

type Repository struct {
	URL        string `json:"url"`
	Source     string `json:"source,omitempty"`
	Commit     string `json:"commit,omitempty"`
	ReleaseTag string `json:"release_tag,omitempty"`
}

type Platform struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	Executable string `json:"executable,omitempty"`
}

type ReleaseAsset struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Checksum string `json:"checksum,omitempty"`
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != "" && m.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("%w: unsupported schema_version %q", ErrInvalidManifest, m.SchemaVersion)
	}
	if !identifierPattern.MatchString(m.ID) {
		return fmt.Errorf("%w: id must match %s", ErrInvalidManifest, identifierPattern.String())
	}
	if strings.TrimSpace(m.Name) == "" || len(m.Name) > 256 {
		return fmt.Errorf("%w: name is required and must be at most 256 characters", ErrInvalidManifest)
	}
	if !versionPattern.MatchString(m.Version) {
		return fmt.Errorf("%w: invalid version", ErrInvalidManifest)
	}
	if strings.TrimSpace(m.Publisher) == "" || len(m.Publisher) > 256 {
		return fmt.Errorf("%w: publisher is required and must be at most 256 characters", ErrInvalidManifest)
	}
	if len(m.Description) > 16<<10 {
		return fmt.Errorf("%w: description is too long", ErrInvalidManifest)
	}
	if err := validateRelativeExecutable(m.Executable); err != nil {
		return fmt.Errorf("%w: executable: %v", ErrInvalidManifest, err)
	}
	if m.ProtocolVersion == "" {
		return fmt.Errorf("%w: protocol_version is required", ErrInvalidManifest)
	}
	if m.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: unsupported protocol version %q", ErrInvalidManifest, m.ProtocolVersion)
	}
	if len(m.SupportedOS) == 0 || len(m.SupportedArch) == 0 {
		return fmt.Errorf("%w: supported_os and supported_arch are required", ErrInvalidManifest)
	}
	if err := validateTargetList(m.SupportedOS, knownOS, "supported_os"); err != nil {
		return err
	}
	if err := validateTargetList(m.SupportedArch, knownArch, "supported_arch"); err != nil {
		return err
	}
	if !contains(m.SupportedOS, runtime.GOOS) && !(runtime.GOOS == "darwin" && contains(m.SupportedOS, "macos")) {
		return fmt.Errorf("%w: unsupported operating system %q", ErrInvalidManifest, runtime.GOOS)
	}
	if !contains(m.SupportedArch, runtime.GOARCH) {
		return fmt.Errorf("%w: unsupported architecture %q", ErrInvalidManifest, runtime.GOARCH)
	}
	if m.MinCoreVersion != "" && !versionPattern.MatchString(m.MinCoreVersion) {
		return fmt.Errorf("%w: invalid min_core_version", ErrInvalidManifest)
	}
	if m.MaxCoreVersion != "" && !versionPattern.MatchString(m.MaxCoreVersion) {
		return fmt.Errorf("%w: invalid max_core_version", ErrInvalidManifest)
	}
	if m.MinCoreVersion != "" && m.MaxCoreVersion != "" && compareVersions(m.MinCoreVersion, m.MaxCoreVersion) > 0 {
		return fmt.Errorf("%w: min_core_version exceeds max_core_version", ErrInvalidManifest)
	}
	if m.Checksum != nil && (strings.ToLower(strings.TrimSpace(m.Checksum.Algorithm)) != "sha256" || !checksumPattern.MatchString(m.Checksum.Value)) {
		return fmt.Errorf("%w: checksum must be a SHA-256 object", ErrInvalidManifest)
	}
	if err := validateSignature(m.Signature); err != nil {
		return err
	}
	if err := validateTools(m.Tools); err != nil {
		return err
	}
	if err := validateEventSources(m.EventSources); err != nil {
		return err
	}
	if err := validatePermissions(m.Permissions); err != nil {
		return err
	}
	if err := validatePlatforms(m.Platforms); err != nil {
		return err
	}
	if err := validateRepository(m.Repository); err != nil {
		return err
	}
	if err := validateReleaseAssets(m.ReleaseAssets); err != nil {
		return err
	}
	return nil
}

var knownOS = map[string]struct{}{
	"darwin": {}, "macos": {}, "windows": {}, "linux": {},
}

var knownArch = map[string]struct{}{
	"386": {}, "amd64": {}, "arm": {}, "arm64": {},
}

func validateTargetList(values []string, known map[string]struct{}, field string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != strings.ToLower(value) {
			return fmt.Errorf("%w: %s values must be lowercase", ErrInvalidManifest, field)
		}
		if _, ok := known[value]; !ok {
			return fmt.Errorf("%w: unknown %s value %q", ErrInvalidManifest, field, value)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%w: duplicate %s value %q", ErrInvalidManifest, field, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateTools(tools []ToolDeclaration) error {
	seen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if !identifierPattern.MatchString(tool.ID) {
			return fmt.Errorf("%w: invalid tool id %q", ErrInvalidManifest, tool.ID)
		}
		if _, duplicate := seen[tool.ID]; duplicate {
			return fmt.Errorf("%w: duplicate tool id %q", ErrInvalidManifest, tool.ID)
		}
		seen[tool.ID] = struct{}{}
		if strings.TrimSpace(tool.Name) == "" {
			return fmt.Errorf("%w: tool %q has no name", ErrInvalidManifest, tool.ID)
		}
		if !tool.Risk.Valid() {
			return fmt.Errorf("%w: tool %q has invalid risk", ErrInvalidManifest, tool.ID)
		}
		// Critical side effects are intentionally unavailable to the MVP. A
		// plugin cannot opt into this class merely by declaring a manifest
		// entry; rejecting it here keeps the restriction in force even when a
		// caller forgets to consult the policy engine later in the lifecycle.
		if tool.Risk == domain.RiskCritical {
			return fmt.Errorf("%w: tool %q uses unavailable critical risk", ErrInvalidManifest, tool.ID)
		}
		if tool.Risk == domain.RiskCritical {
			return fmt.Errorf("%w: tool %q uses critical risk, which is unavailable in MVP", ErrInvalidManifest, tool.ID)
		}
		if (tool.Risk == domain.RiskMedium || tool.Risk == domain.RiskHigh) && len(tool.Permissions) == 0 {
			return fmt.Errorf("%w: tool %q must declare a capability for its risk boundary", ErrInvalidManifest, tool.ID)
		}
		if err := validateJSONObject(tool.InputSchema); err != nil {
			return fmt.Errorf("%w: tool %q input_schema: %v", ErrInvalidManifest, tool.ID, err)
		}
		if err := validateJSONObject(tool.OutputSchema); err != nil {
			return fmt.Errorf("%w: tool %q output_schema: %v", ErrInvalidManifest, tool.ID, err)
		}
		if err := validateCapabilities(tool.Permissions); err != nil {
			return fmt.Errorf("%w: tool %q capabilities: %v", ErrInvalidManifest, tool.ID, err)
		}
		if err := validateCapabilityRisk(tool.Risk, tool.Permissions); err != nil {
			return fmt.Errorf("%w: tool %q risk: %v", ErrInvalidManifest, tool.ID, err)
		}
	}
	return nil
}

func validateCapabilityRisk(risk domain.RiskLevel, capabilities []string) error {
	minimum := domain.RiskLow
	for _, raw := range capabilities {
		capability := domain.NormalizeCapabilityName(raw)
		if string(capability) != strings.TrimSpace(raw) {
			return fmt.Errorf("capability names must be lowercase")
		}
		candidate := domain.RiskLow
		switch capability {
		case domain.CapabilityFilesystemWrite, domain.CapabilitySchedulerManage,
			domain.CapabilityMemoryWrite, domain.CapabilityMemoryDelete,
			domain.CapabilityNotificationsSend:
			candidate = domain.RiskMedium
		case domain.CapabilityFilesystemDelete, domain.CapabilityExternalSend,
			domain.CapabilitySecretsUse:
			candidate = domain.RiskHigh
		}
		if riskRank(candidate) > riskRank(minimum) {
			minimum = candidate
		}
	}
	if riskRank(risk) < riskRank(minimum) {
		return fmt.Errorf("risk %q is below required minimum %q", risk, minimum)
	}
	return nil
}

func riskRank(risk domain.RiskLevel) int {
	switch risk {
	case domain.RiskLow:
		return 0
	case domain.RiskMedium:
		return 1
	case domain.RiskHigh:
		return 2
	case domain.RiskCritical:
		return 3
	default:
		return -1
	}
}

func validateEventSources(sources []EventSource) error {
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if !identifierPattern.MatchString(source.ID) {
			return fmt.Errorf("%w: invalid event source id %q", ErrInvalidManifest, source.ID)
		}
		if _, duplicate := seen[source.ID]; duplicate {
			return fmt.Errorf("%w: duplicate event source id %q", ErrInvalidManifest, source.ID)
		}
		seen[source.ID] = struct{}{}
		if strings.TrimSpace(source.Name) == "" {
			return fmt.Errorf("%w: event source %q has no name", ErrInvalidManifest, source.ID)
		}
		if err := validateJSONObject(source.Schema); err != nil {
			return fmt.Errorf("%w: event source %q schema: %v", ErrInvalidManifest, source.ID, err)
		}
		if err := validateCapabilities(source.Permissions); err != nil {
			return fmt.Errorf("%w: event source %q capabilities: %v", ErrInvalidManifest, source.ID, err)
		}
	}
	return nil
}

// ValidatePermissionDeclarations is the single rule for a manifest permission
// block, exported so every manifest decode in the tree applies exactly the
// same one. In particular it rejects a manifest that declares the same
// capability twice: pluginDTO renders one row per declaration while a
// consumer keying declarations by capability can only keep one scope, so a
// duplicate lets the scope an owner reads and approves differ from the scope
// that is enforced. Merging the declarations would leave the same ambiguity
// in the manifest format, so a duplicate is treated as malformed.
func ValidatePermissionDeclarations(permissions []PermissionDeclaration) error {
	return validatePermissions(permissions)
}

func validatePermissions(permissions []PermissionDeclaration) error {
	seen := make(map[string]struct{}, len(permissions))
	for _, permission := range permissions {
		capability := domain.NormalizeCapabilityName(permission.Capability)
		if string(capability) != strings.TrimSpace(permission.Capability) {
			return fmt.Errorf("%w: permission capability must be lowercase", ErrInvalidManifest)
		}
		if !capability.Valid() {
			return fmt.Errorf("%w: unknown permission capability %q", ErrInvalidManifest, permission.Capability)
		}
		if _, duplicate := seen[string(capability)]; duplicate {
			return fmt.Errorf("%w: duplicate permission capability %q", ErrInvalidManifest, capability)
		}
		seen[string(capability)] = struct{}{}
		if strings.TrimSpace(permission.Reason) == "" || len(permission.Reason) > 16<<10 {
			return fmt.Errorf("%w: permission %q requires a reason", ErrInvalidManifest, capability)
		}
		if len(permission.Scope) > 0 {
			if err := validateJSONObject(permission.Scope); err != nil {
				return fmt.Errorf("%w: permission %q scope: %v", ErrInvalidManifest, capability, err)
			}
		}
	}
	return nil
}

func validatePlatforms(platforms []Platform) error {
	seen := make(map[string]struct{}, len(platforms))
	for _, platform := range platforms {
		if _, ok := knownOS[platform.OS]; !ok {
			return fmt.Errorf("%w: unsupported platform OS %q", ErrInvalidManifest, platform.OS)
		}
		if _, ok := knownArch[platform.Arch]; !ok {
			return fmt.Errorf("%w: unsupported platform architecture %q", ErrInvalidManifest, platform.Arch)
		}
		key := platform.OS + "/" + platform.Arch
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: duplicate platform %q", ErrInvalidManifest, key)
		}
		seen[key] = struct{}{}
		if platform.Executable != "" {
			if err := validateRelativeExecutable(platform.Executable); err != nil {
				return fmt.Errorf("%w: platform executable: %v", ErrInvalidManifest, err)
			}
		}
	}
	return nil
}

func validateRepository(repository *Repository) error {
	if repository == nil {
		return nil
	}
	parsed, err := url.Parse(repository.URL)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1"))) {
		return fmt.Errorf("%w: repository URL must use HTTPS (except localhost)", ErrInvalidManifest)
	}
	if len(repository.Commit) > 128 {
		return fmt.Errorf("%w: repository commit is too long", ErrInvalidManifest)
	}
	return nil
}

func validateReleaseAssets(assets []ReleaseAsset) error {
	seen := make(map[string]struct{}, len(assets))
	for _, asset := range assets {
		if _, ok := knownOS[asset.OS]; !ok {
			return fmt.Errorf("%w: unsupported release asset OS %q", ErrInvalidManifest, asset.OS)
		}
		if _, ok := knownArch[asset.Arch]; !ok {
			return fmt.Errorf("%w: unsupported release asset architecture %q", ErrInvalidManifest, asset.Arch)
		}
		if strings.TrimSpace(asset.Filename) == "" || strings.ContainsAny(asset.Filename, `/\\`) {
			return fmt.Errorf("%w: release asset filename must be plain", ErrInvalidManifest)
		}
		parsed, err := url.Parse(asset.URL)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1"))) {
			return fmt.Errorf("%w: release asset URL must use HTTPS (except localhost)", ErrInvalidManifest)
		}
		if asset.Checksum != "" && !checksumPattern.MatchString(asset.Checksum) {
			return fmt.Errorf("%w: release asset checksum is invalid", ErrInvalidManifest)
		}
		key := asset.OS + "/" + asset.Arch + "/" + asset.Filename
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: duplicate release asset %q", ErrInvalidManifest, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateCapabilities(capabilities []string) error {
	seen := make(map[string]struct{}, len(capabilities))
	for _, raw := range capabilities {
		capability := domain.NormalizeCapabilityName(raw)
		if !capability.Valid() {
			return fmt.Errorf("unknown capability %q", raw)
		}
		if _, duplicate := seen[string(capability)]; duplicate {
			return fmt.Errorf("duplicate capability %q", capability)
		}
		seen[string(capability)] = struct{}{}
	}
	return nil
}

func validateSignature(signature *SignatureMetadata) error {
	if signature == nil {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(signature.Algorithm)) != "ed25519" || strings.TrimSpace(signature.KeyID) == "" || strings.TrimSpace(signature.Value) == "" {
		return fmt.Errorf("%w: signature requires ed25519 algorithm, key_id and value", ErrInvalidManifest)
	}
	return nil
}

func validateJSONObject(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("schema is required")
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value == nil {
		return fmt.Errorf("schema must be an object")
	}
	return nil
}

func validateRelativeExecutable(executable string) error {
	if strings.TrimSpace(executable) == "" {
		return fmt.Errorf("path is required")
	}
	if len(executable) > 512 {
		return fmt.Errorf("path is too long")
	}
	if strings.IndexByte(executable, 0) >= 0 {
		return fmt.Errorf("path contains NUL")
	}
	// Validate both path syntaxes because a package can be inspected on one
	// platform and installed on another.
	if filepath.IsAbs(executable) || strings.HasPrefix(executable, "/") || strings.HasPrefix(executable, "\\") || windowsVolumePath(executable) {
		return fmt.Errorf("path must be relative")
	}
	normalized := strings.ReplaceAll(executable, "\\", "/")
	clean := filepath.Clean(filepath.FromSlash(normalized))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path contains parent traversal")
	}
	for _, part := range strings.FieldsFunc(normalized, func(r rune) bool { return r == '/' }) {
		if part == ".." {
			return fmt.Errorf("path contains parent traversal")
		}
	}
	return nil
}

// LoadManifest reads and validates the package manifest without accepting a
// symlink that points outside the package root. The returned manifest is safe
// to use as the declaration input for Supervisor.
func LoadManifest(packageDir string) (Manifest, error) {
	if strings.TrimSpace(packageDir) == "" {
		return Manifest{}, fmt.Errorf("%w: package directory is required", ErrInvalidManifest)
	}
	rootAbs, err := filepath.Abs(packageDir)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: package directory: %v", ErrInvalidManifest, err)
	}
	root, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: package directory: %v", ErrInvalidManifest, err)
	}
	manifestPath := filepath.Join(root, ManifestFileName)
	realManifestPath, err := filepath.EvalSymlinks(manifestPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: manifest: %v", ErrInvalidManifest, err)
	}
	if !withinPath(root, realManifestPath) {
		return Manifest{}, fmt.Errorf("%w: manifest escapes package directory", ErrPathEscape)
	}
	file, err := os.Open(realManifestPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: open manifest: %v", ErrInvalidManifest, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: read manifest: %v", ErrInvalidManifest, err)
	}
	if len(data) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("%w: manifest exceeds %d bytes", ErrMessageTooLarge, MaxManifestBytes)
	}
	manifest, err := DecodeManifest(data)
	if err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// DecodeManifest rejects unknown fields so a misspelled declaration cannot
// silently change the permission or executable semantics of a package.
func DecodeManifest(data []byte) (Manifest, error) {
	if len(data) == 0 || len(data) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("%w: manifest size is outside bounds", ErrInvalidManifest)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode manifest: %v", ErrInvalidManifest, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Manifest{}, fmt.Errorf("%w: manifest contains trailing JSON", ErrInvalidManifest)
		}
		return Manifest{}, fmt.Errorf("%w: manifest trailing data: %v", ErrInvalidManifest, err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func windowsVolumePath(value string) bool {
	return len(value) >= 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':'
}

// ResolveExecutable canonicalizes the package root and executable, rejects
// lexical traversal and symlink escape, and returns an executable regular
// file. It is the only path resolver used by the supervisor.
func (m Manifest) ResolveExecutable(packageDir string) (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(packageDir) == "" {
		return "", fmt.Errorf("%w: package directory is required", ErrPathEscape)
	}
	rootAbs, err := filepath.Abs(packageDir)
	if err != nil {
		return "", fmt.Errorf("%w: resolve package directory: %v", ErrPathEscape, err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("%w: resolve package directory: %v", ErrPathEscape, err)
	}
	rootInfo, err := os.Stat(rootReal)
	if err != nil || !rootInfo.IsDir() {
		return "", fmt.Errorf("%w: package directory is not a directory", ErrPathEscape)
	}
	normalized := strings.ReplaceAll(m.Executable, "\\", "/")
	joined := filepath.Join(rootReal, filepath.FromSlash(normalized))
	if !withinPath(rootReal, joined) {
		return "", fmt.Errorf("%w: executable %q", ErrPathEscape, m.Executable)
	}
	executableReal, err := filepath.EvalSymlinks(joined)
	if err != nil && runtime.GOOS == "windows" && filepath.Ext(joined) == "" {
		// Go and common Windows build tools append .exe even when -o is given
		// an extensionless name. Manifests stay portable by naming the same
		// executable on every platform, so resolve that conventional suffix.
		joined += ".exe"
		executableReal, err = filepath.EvalSymlinks(joined)
	}
	if err != nil {
		return "", fmt.Errorf("%w: resolve executable: %v", ErrExecutableInvalid, err)
	}
	if !withinPath(rootReal, executableReal) {
		return "", fmt.Errorf("%w: executable %q", ErrPathEscape, m.Executable)
	}
	info, err := os.Stat(executableReal)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: executable %q", ErrExecutableInvalid, m.Executable)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("%w: file is not executable", ErrExecutableInvalid)
	}
	return executableReal, nil
}

func withinPath(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// VerifyChecksum computes the SHA-256 digest of the resolved executable and
// compares it with the manifest checksum. An empty checksum is accepted only
// for unsigned/dev packages; production callers can require it separately.
func (m Manifest) VerifyChecksum(packageDir string) error {
	if m.Checksum == nil || strings.TrimSpace(m.Checksum.Value) == "" {
		return nil
	}
	path, err := m.ResolveExecutable(packageDir)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: open executable: %v", ErrInvalidManifest, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("%w: hash executable: %v", ErrInvalidManifest, err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, m.Checksum.Value) {
		return fmt.Errorf("%w: checksum mismatch", ErrInvalidManifest)
	}
	return nil
}

// CompatibleWithCore verifies the optional semver-like bounds without
// bringing a semver dependency into the plugin runtime. Versions are compared
// by numeric dot-separated components followed by lexical pre-release text.
func (m Manifest) CompatibleWithCore(coreVersion string) bool {
	if strings.TrimSpace(coreVersion) == "" {
		return true
	}
	if m.MinCoreVersion != "" && compareVersions(coreVersion, m.MinCoreVersion) < 0 {
		return false
	}
	if m.MaxCoreVersion != "" && compareVersions(coreVersion, m.MaxCoreVersion) > 0 {
		return false
	}
	return true
}

func compareVersions(left, right string) int {
	parse := func(value string) ([]int, string) {
		value = strings.TrimPrefix(strings.TrimSpace(value), "v")
		parts := strings.SplitN(value, "-", 2)
		numeric := strings.Split(parts[0], ".")
		values := make([]int, len(numeric))
		for i, part := range numeric {
			var n int
			_, _ = fmt.Sscanf(part, "%d", &n)
			values[i] = n
		}
		pre := ""
		if len(parts) == 2 {
			pre = parts[1]
		}
		return values, pre
	}
	leftNumbers, leftPre := parse(left)
	rightNumbers, rightPre := parse(right)
	for i := 0; i < len(leftNumbers) || i < len(rightNumbers); i++ {
		var l, r int
		if i < len(leftNumbers) {
			l = leftNumbers[i]
		}
		if i < len(rightNumbers) {
			r = rightNumbers[i]
		}
		if l < r {
			return -1
		}
		if l > r {
			return 1
		}
	}
	if leftPre == rightPre {
		return 0
	}
	if leftPre == "" {
		return 1
	}
	if rightPre == "" {
		return -1
	}
	return strings.Compare(leftPre, rightPre)
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), wanted) {
			return true
		}
	}
	return false
}

// SortedCapabilities makes manifest capability display deterministic.
func SortedCapabilities(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
