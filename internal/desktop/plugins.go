package desktop

import (
	"github.com/OrdoAI/yuri-agent/internal/buildinfo"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

var pluginCoreVersion = buildinfo.Version

const maxPluginManifestBytes = 1024 * 1024

type PluginPermissionDTO struct {
	Capability  string   `json:"capability"`
	Scope       string   `json:"scope,omitempty"`
	Values      []string `json:"values,omitempty"`
	Description string   `json:"description,omitempty"`
	Granted     bool     `json:"granted"`
}

type PluginToolDTO struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Risk         string   `json:"risk"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type PluginDTO struct {
	ID              string                `json:"id"`
	Name            string                `json:"name"`
	Publisher       string                `json:"publisher"`
	Version         string                `json:"version"`
	ProtocolVersion string                `json:"protocol_version"`
	Enabled         bool                  `json:"enabled"`
	RuntimeStatus   string                `json:"runtime_status"`
	Status          string                `json:"status"`
	Running         bool                  `json:"running"`
	SignatureStatus string                `json:"signature_status"`
	Checksum        string                `json:"checksum,omitempty"`
	RepositoryURL   string                `json:"repository_url,omitempty"`
	InstallPath     string                `json:"install_path,omitempty"`
	ReleaseTag      string                `json:"release_tag,omitempty"`
	SourceCommit    string                `json:"source_commit,omitempty"`
	LastError       string                `json:"last_error,omitempty"`
	Permissions     []PluginPermissionDTO `json:"permissions"`
	Tools           []PluginToolDTO       `json:"tools"`
	EventSources    []string              `json:"event_sources"`
	InstalledAt     string                `json:"installed_at,omitempty"`
	UpdatedAt       string                `json:"updated_at,omitempty"`
}

type PluginPackageInspection struct {
	Path            string     `json:"path"`
	Valid           bool       `json:"valid"`
	Compatible      bool       `json:"compatible"`
	Manifest        *PluginDTO `json:"manifest,omitempty"`
	SignatureStatus string     `json:"signature_status"`
	Checksum        string     `json:"checksum,omitempty"`
	Warnings        []string   `json:"warnings"`
	Errors          []string   `json:"errors"`
	ExecutablePath  string     `json:"executable_path,omitempty"`
	Installable     bool       `json:"installable"`
	RequiresDevMode bool       `json:"requires_dev_mode"`
}

// PluginPathRequest deliberately carries nothing but the path. Whether an
// unsigned or unverified package may be installed is decided exclusively by
// the global, owner-controlled plugin dev-mode switch; a per-request field
// would let the renderer opt itself out of that boundary.
type PluginPathRequest struct {
	Path string `json:"path"`
}

type PluginIDRequest struct {
	ID       string `json:"id"`
	PluginID string `json:"pluginId,omitempty"`
}

// PluginCapabilityConsent is one capability the owner explicitly approved for
// a plugin. It is not derived from the manifest: the manifest only says what
// the plugin may ask for, this says what the owner agreed to.
type PluginCapabilityConsent struct {
	Capability string `json:"capability"`
	// ScopeKind and ScopeValues are optional. When omitted the manifest
	// declaration is used verbatim; when present they must be narrower than
	// or equal to the declaration.
	ScopeKind   string   `json:"scopeKind,omitempty"`
	ScopeValues []string `json:"scopeValues,omitempty"`
	// AllowUnrestricted must be set when the effective grant ends up
	// unrestricted, so an unbounded grant is always a separate decision.
	AllowUnrestricted bool `json:"allowUnrestricted,omitempty"`
	// ExpiresInHours optionally bounds the grant lifetime.
	ExpiresInHours int `json:"expiresInHours,omitempty"`
}

// PluginEnableRequest enables a plugin with an explicit consent list. An
// empty list enables the plugin with no capability grants at all: tools that
// declare a capability are then denied at invocation time.
type PluginEnableRequest struct {
	ID           string                    `json:"id"`
	PluginID     string                    `json:"pluginId,omitempty"`
	Capabilities []PluginCapabilityConsent `json:"capabilities,omitempty"`
}

func (request PluginEnableRequest) pluginID() domain.ID {
	return PluginIDRequest{ID: request.ID, PluginID: request.PluginID}.pluginID()
}

// PluginPublisherKeyRequest adds one ed25519 publisher key to the local trust
// store. This is a dedicated owner action with its own audit record; a key is
// never added as a side effect of installing a package.
type PluginPublisherKeyRequest struct {
	KeyID     string `json:"keyId"`
	PublicKey string `json:"publicKey"`
	Publisher string `json:"publisher,omitempty"`
	Comment   string `json:"comment,omitempty"`
}

type PluginPublisherKeyDTO struct {
	KeyID     string `json:"keyId"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"publicKey"`
	Publisher string `json:"publisher,omitempty"`
	Comment   string `json:"comment,omitempty"`
	AddedAt   string `json:"addedAt"`
}
