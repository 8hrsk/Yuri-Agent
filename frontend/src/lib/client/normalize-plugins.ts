import type {
  PluginPackageInspection,
  PluginPermission,
  PluginRecord,
  PluginSignatureStatus,
  PluginStatus,
  PluginTool,
  ToolRisk,
} from '../contracts'
import type { UnknownRecord } from './primitives'

function normalizePluginRisk(value: unknown): ToolRisk {
  const risk = String(value ?? '').toLowerCase()
  if (risk === 'medium' || risk === 'high' || risk === 'critical') return risk
  return 'low'
}

function normalizePluginStatus(value: unknown, enabled = false, running = false): PluginStatus {
  const status = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (running || status === 'running' || status === 'started' || status === 'healthy') return 'running'
  if (status === 'crashed' || status === 'crash') return 'crashed'
  if (status === 'error' || status === 'failed' || status === 'failure') return 'error'
  if (status === 'stopped' || status === 'idle') return 'stopped'
  if (status === 'disabled' || status === 'off') return 'disabled'
  if (status === 'enabled' || status === 'on' || enabled) return 'enabled'
  if (status === 'installed' || status === 'validated') return 'installed'
  return 'unknown'
}

// The signature status is reported by the backend and never rewritten here.
// Dev mode decides whether an unsigned package may be *installed*; it does not
// make the package signed, so relabelling `unsigned` as `dev` would hide the
// one fact the owner is being asked to accept.
function normalizePluginSignature(value: unknown): PluginSignatureStatus {
  const status = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (status === 'signed' || status === 'valid' || status === 'verified' || status === 'trusted') return 'signed'
  if (status === 'unsigned' || status === 'unverified' || status === 'none') return 'unsigned'
  if (status === 'dev' || status === 'development') return 'dev'
  if (status === 'invalid' || status === 'rejected' || status === 'tampered') return 'invalid'
  return 'unknown'
}

function normalizeStringList(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.map((item) => {
    if (typeof item === 'string' || typeof item === 'number') return String(item)
    if (!item || typeof item !== 'object') return ''
    const record = item as UnknownRecord
    return String(record.name ?? record.id ?? record.type ?? '')
  }).filter(Boolean)
}

function normalizePluginPermission(value: unknown): PluginPermission | undefined {
  if (typeof value === 'string') {
    return { capability: value, granted: false }
  }
  if (!value || typeof value !== 'object') return undefined
  const raw = value as UnknownRecord
  const capability = String(raw.capability ?? raw.name ?? raw.id ?? '')
  if (!capability) return undefined
  const scopeValue = raw.scope ?? raw.scopeJson ?? raw.scope_json
  const scope = scopeValue === undefined
    ? undefined
    : typeof scopeValue === 'string' ? scopeValue : JSON.stringify(scopeValue)
  // PluginPermissionDTO splits the scope into `scope` (the kind) and `values`.
  // Dropping the values would leave the consent dialog unable to show what the
  // manifest actually asked for.
  const rawValues = raw.values ?? raw.scopeValues ?? raw.scope_values
  const scopeValues = Array.isArray(rawValues) ? normalizeStringList(rawValues) : undefined
  const expiry = raw.grantExpiresAt ?? raw.grant_expires_at ?? raw.expiresAt ?? raw.expires_at
  return {
    capability,
    scope,
    scopeValues: scopeValues && scopeValues.length > 0 ? scopeValues : undefined,
    description: raw.description || raw.reason ? String(raw.description ?? raw.reason) : undefined,
    risk: raw.risk === undefined ? undefined : normalizePluginRisk(raw.risk),
    granted: Boolean(raw.granted ?? raw.approved ?? raw.enabled ?? raw.allowed),
    grantExpiresAt: expiry ? String(expiry) : undefined,
  }
}

function normalizePluginTool(value: unknown): PluginTool | undefined {
  if (typeof value === 'string') {
    return { id: value, name: value, risk: 'low' }
  }
  if (!value || typeof value !== 'object') return undefined
  const raw = value as UnknownRecord
  const id = String(raw.id ?? raw.toolId ?? raw.tool_id ?? raw.name ?? '')
  if (!id) return undefined
  return {
    id,
    name: String(raw.name ?? raw.label ?? id),
    description: raw.description || raw.summary ? String(raw.description ?? raw.summary) : undefined,
    risk: normalizePluginRisk(raw.risk ?? raw.riskLevel ?? raw.risk_level),
  }
}

function normalizePlugin(value: unknown): PluginRecord | undefined {
  if (!value || typeof value !== 'object') return undefined
  const rawValue = value as UnknownRecord
  const source = rawValue.plugin && typeof rawValue.plugin === 'object' ? rawValue.plugin as UnknownRecord : rawValue
  const id = String(source.id ?? source.pluginId ?? source.plugin_id ?? '')
  if (!id) return undefined
  const enabled = Boolean(source.enabled ?? source.isEnabled ?? source.is_enabled)
  const running = Boolean(source.running ?? source.isRunning ?? source.is_running)
  const status = normalizePluginStatus(source.status ?? source.state, enabled, running)
  const permissionValues = source.permissions ?? source.requestedPermissions ?? source.requested_permissions ?? source.capabilities
  const toolValues = source.tools ?? source.toolDescriptors ?? source.tool_descriptors
  const minCore = source.minCoreVersion ?? source.min_core_version
  const maxCore = source.maxCoreVersion ?? source.max_core_version
  const coreVersionRange = source.coreVersionRange || source.core_version_range
    ? String(source.coreVersionRange ?? source.core_version_range)
    : minCore || maxCore
      ? `${minCore ? `>= ${String(minCore)}` : ''}${minCore && maxCore ? ' · ' : ''}${maxCore ? `<= ${String(maxCore)}` : ''}`
      : undefined
  const installedAt = source.installedAt ?? source.installed_at
  const updatedAt = source.updatedAt ?? source.updated_at
  const permissions = Array.isArray(permissionValues)
    ? permissionValues.map((item) => normalizePluginPermission(item)).filter((item): item is PluginPermission => Boolean(item))
    : []
  const tools = Array.isArray(toolValues)
    ? toolValues.map((item) => normalizePluginTool(item)).filter((item): item is PluginTool => Boolean(item))
    : []
  return {
    id,
    name: String(source.name ?? source.displayName ?? source.display_name ?? id),
    version: String(source.version ?? 'unknown'),
    publisher: source.publisher ? String(source.publisher) : undefined,
    description: source.description ? String(source.description) : undefined,
    protocolVersion: source.protocolVersion || source.protocol_version ? String(source.protocolVersion ?? source.protocol_version) : undefined,
    coreVersionRange,
    enabled,
    running,
    status,
    installPath: source.installPath || source.install_path ? String(source.installPath ?? source.install_path) : undefined,
    signatureStatus: normalizePluginSignature(source.signatureStatus ?? source.signature_status ?? source.signature),
    checksum: source.checksum ? String(source.checksum) : undefined,
    repositoryUrl: source.repositoryUrl || source.repository_url ? String(source.repositoryUrl ?? source.repository_url) : undefined,
    releaseTag: source.releaseTag || source.release_tag ? String(source.releaseTag ?? source.release_tag) : undefined,
    sourceCommit: source.sourceCommit || source.source_commit ? String(source.sourceCommit ?? source.source_commit) : undefined,
    permissions,
    tools,
    eventSources: normalizeStringList(source.eventSources ?? source.event_sources),
    lastError: source.lastError || source.last_error || source.error ? String(source.lastError ?? source.last_error ?? source.error) : undefined,
    installedAt: installedAt ? String(installedAt) : undefined,
    updatedAt: updatedAt ? String(updatedAt) : undefined,
  }
}

function normalizePluginList(value: unknown): PluginRecord[] {
  const rawItems = Array.isArray(value)
    ? value
    : value && typeof value === 'object'
      ? ((value as UnknownRecord).items ?? (value as UnknownRecord).plugins ?? (value as UnknownRecord).results)
      : undefined
  return Array.isArray(rawItems)
    ? rawItems.map((item) => normalizePlugin(item)).filter((item): item is PluginRecord => Boolean(item))
    : []
}

function emptyPluginInspection(path: string, message: string): PluginPackageInspection {
  return {
    path,
    valid: false,
    compatible: false,
    signatureStatus: 'unknown',
    warnings: [],
    errors: [message],
    installable: false,
    requiresDevMode: false,
  }
}

function normalizePluginInspection(value: unknown, path: string): PluginPackageInspection {
  if (!value || typeof value !== 'object') return emptyPluginInspection(path, 'Backend не вернул результат проверки пакета.')
  const raw = value as UnknownRecord
  const source = raw.inspection && typeof raw.inspection === 'object' ? raw.inspection as UnknownRecord : raw
  const manifest = normalizePlugin(source.manifest ?? source.plugin ?? source.metadata)
  const warnings = normalizeStringList(source.warnings ?? source.warning)
  const errors = normalizeStringList(source.errors ?? source.error)
  return {
    path: String(source.path ?? path),
    valid: Boolean(source.valid ?? source.isValid ?? source.is_valid ?? manifest),
    compatible: Boolean(source.compatible ?? source.isCompatible ?? source.is_compatible ?? manifest),
    manifest,
    signatureStatus: normalizePluginSignature(source.signatureStatus ?? source.signature_status ?? manifest?.signatureStatus),
    checksum: source.checksum ? String(source.checksum) : manifest?.checksum,
    warnings,
    errors,
    // Both fields are unconditional in desktop.PluginPackageInspection. A
    // payload without them is not a permissive one: an absent verdict means
    // "not cleared for install", never "install anyway".
    installable: Boolean(source.installable),
    requiresDevMode: Boolean(source.requiresDevMode ?? source.requires_dev_mode),
  }
}

export { emptyPluginInspection, normalizePlugin, normalizePluginInspection, normalizePluginList, normalizeStringList }
