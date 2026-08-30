import type { ToolRisk } from './chat'

export type PluginStatus = 'installed' | 'enabled' | 'running' | 'stopped' | 'crashed' | 'error' | 'disabled' | 'unknown'

export type PluginSignatureStatus = 'signed' | 'unsigned' | 'invalid' | 'dev' | 'unknown'

/**
 * One capability a plugin manifest *requests*. It is not a grant: the backend
 * writes grants only from the owner's explicit consent list, so `granted`
 * reflects a stored grant and never merely the fact that the manifest asked.
 *
 * `scope` carries the scope kind (`filesystem`, `network`, `resource` or
 * `unrestricted`) and `scopeValues` the values it is narrowed to — the two
 * halves of desktop.PluginPermissionDTO's `scope` and `values` fields.
 */
export interface PluginPermission {
  capability: string
  scope?: string
  scopeValues?: string[]
  description?: string
  risk?: ToolRisk
  granted: boolean
  grantExpiresAt?: string
}

/**
 * One capability the owner explicitly approved. Mirrors
 * desktop.PluginCapabilityConsent field for field.
 *
 * `scopeKind`/`scopeValues` are optional: omitting both accepts the manifest
 * declaration verbatim, supplying them narrows it. The backend rejects a scope
 * broader than the declaration rather than silently clamping it.
 */
export interface PluginCapabilityConsent {
  capability: string
  scopeKind?: string
  scopeValues?: string[]
  /**
   * Required whenever the resulting grant ends up unrestricted — by scope kind
   * or by a bare `*` value. An unbounded grant is always its own decision.
   */
  allowUnrestricted?: boolean
  /** Optional grant lifetime; the backend caps it at 24 * 366 hours. */
  expiresInHours?: number
}

/**
 * Enables a plugin with exactly this consent list. An empty list enables the
 * plugin with no capability grants at all, which is a valid outcome: its tools
 * are then denied at invocation time.
 */
export interface PluginEnableRequest {
  pluginId: string
  capabilities: PluginCapabilityConsent[]
}

export interface PluginTool {
  id: string
  name: string
  description?: string
  risk: ToolRisk
}

export interface PluginRecord {
  id: string
  name: string
  version: string
  publisher?: string
  description?: string
  protocolVersion?: string
  coreVersionRange?: string
  enabled: boolean
  running: boolean
  status: PluginStatus
  installPath?: string
  signatureStatus: PluginSignatureStatus
  checksum?: string
  repositoryUrl?: string
  releaseTag?: string
  sourceCommit?: string
  permissions: PluginPermission[]
  tools: PluginTool[]
  eventSources: string[]
  lastError?: string
  installedAt?: string
  updatedAt?: string
}

export interface PluginPackageInspection {
  path: string
  valid: boolean
  compatible: boolean
  manifest?: PluginRecord
  signatureStatus: PluginSignatureStatus
  checksum?: string
  warnings: string[]
  errors: string[]
  /**
   * The backend's own verdict, already accounting for the persisted dev-mode
   * switch: desktop.PluginPackageInspection computes
   * `compatible && (!requiresDevMode || pluginDevMode())`. Both fields are
   * tagged `json:"installable"` / `json:"requires_dev_mode"` without
   * `omitempty`, so they always arrive. The view must not recompute the
   * policy: it renders this verdict.
   */
  installable: boolean
  /**
   * True when the package is unsigned or its signature could not be verified,
   * so installing and starting it require dev mode to be enabled.
   */
  requiresDevMode: boolean
}
