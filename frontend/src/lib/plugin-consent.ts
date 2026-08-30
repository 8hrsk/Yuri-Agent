import type { PluginCapabilityConsent, PluginEnableRequest, PluginPermission } from './contracts'

/**
 * Scope kinds understood by the backend. Mirrors domain.ScopeKind — anything
 * else is rejected by domain.CapabilityScope.Valid() before a grant is written.
 */
export const pluginScopeKinds = ['unrestricted', 'filesystem', 'network', 'resource'] as const

export type PluginScopeKind = (typeof pluginScopeKinds)[number]

/** A capability scope as the owner and the manifest both express it. */
export interface PluginScope {
  kind: string
  values: string[]
}

/**
 * The backend caps an owner-chosen grant lifetime at one year
 * (desktop.maxPluginGrantHours) so an "expiring" grant cannot quietly become
 * permanent. The dialog refuses a longer one up front rather than letting the
 * user discover the ceiling from a rejected enable.
 */
export const maxPluginGrantHours = 24 * 366

export function normalizeScopeValues(values: readonly string[] | undefined): string[] {
  if (!values) return []
  return values.map((value) => value.trim()).filter(Boolean)
}

/** Splits the free-text scope editor into the list the bridge expects. */
export function parseScopeValues(raw: string): string[] {
  return raw.split(/[\n,]/).map((value) => value.trim()).filter(Boolean)
}

/**
 * The scope a manifest declares for one permission. An absent kind means the
 * plugin asked for everything: desktop.pluginConsentGrants substitutes
 * domain.UnrestrictedScope() for an empty declaration, so the dialog has to
 * show that as "unrestricted" rather than as "unspecified".
 */
export function declaredScope(permission: PluginPermission): PluginScope {
  const kind = (permission.scope ?? '').trim().toLowerCase()
  return { kind: kind || 'unrestricted', values: normalizeScopeValues(permission.scopeValues) }
}

/**
 * The scope that would actually be granted. An override that is entirely empty
 * means "use the declaration verbatim", which is exactly the branch
 * desktop.pluginConsentGrants takes when both ScopeKind and ScopeValues are
 * absent from the request.
 */
export function effectiveScope(declared: PluginScope, override: PluginScope): PluginScope {
  const kind = override.kind.trim().toLowerCase()
  const values = normalizeScopeValues(override.values)
  if (!kind && values.length === 0) return declared
  return { kind, values }
}

/**
 * Whether a scope leaves the plugin effectively unbounded.
 *
 * Kind alone is deliberately not the test. A scope of kind `network` with the
 * single value `*` is every host, and the backend (N-8) routes such a value
 * through the same explicit confirmation as kind `unrestricted` instead of
 * letting it slip past the gate while looking narrow. The dialog has to draw
 * the line in the same place, or it would present a bounded-looking grant that
 * the backend then rejects — or worse, that the owner approves without ever
 * being told what they agreed to.
 */
export function isUnrestrictedScope(scope: PluginScope): boolean {
  if (scope.kind.trim().toLowerCase() === 'unrestricted') return true
  return scope.values.some((value) => value.trim() === '*')
}

/** One row of the consent dialog, held as raw text so the editor stays simple. */
export interface PluginConsentDraft {
  approved: boolean
  /** Empty means "as declared in the manifest". */
  scopeKind: string
  /** Free text; comma or newline separated. */
  scopeValues: string
  allowUnrestricted: boolean
  /** Empty means "no expiry". */
  expiresInHours: string
}

export function emptyConsentDraft(approved = false): PluginConsentDraft {
  return { approved, scopeKind: '', scopeValues: '', allowUnrestricted: false, expiresInHours: '' }
}

/** The scope a draft would send, resolved against the manifest declaration. */
export function draftScope(permission: PluginPermission, draft: PluginConsentDraft): PluginScope {
  return effectiveScope(declaredScope(permission), { kind: draft.scopeKind, values: parseScopeValues(draft.scopeValues) })
}

/** Whether this row still needs its separate unrestricted confirmation. */
export function draftNeedsUnrestrictedConfirmation(permission: PluginPermission, draft: PluginConsentDraft): boolean {
  return draft.approved && isUnrestrictedScope(draftScope(permission, draft))
}

export type ConsentResult =
  | { ok: true; consent: PluginCapabilityConsent }
  | { ok: false; error: string }

/**
 * Turns one approved row into the consent the bridge accepts, or explains why
 * it cannot. Every rule here has a counterpart in
 * desktop.pluginConsentGrants; the point is to state it in the owner's words
 * before the request leaves, not to replace the backend's own check.
 */
export function consentFromDraft(permission: PluginPermission, draft: PluginConsentDraft): ConsentResult {
  const kind = draft.scopeKind.trim().toLowerCase()
  const values = parseScopeValues(draft.scopeValues)

  if (kind && !pluginScopeKinds.includes(kind as PluginScopeKind)) {
    return { ok: false, error: `Scope «${kind}» не поддерживается.` }
  }
  if (kind === 'unrestricted' && values.length > 0) {
    return { ok: false, error: 'Scope «unrestricted» не принимает значения: он и так покрывает всё.' }
  }
  if (kind && kind !== 'unrestricted' && values.length === 0) {
    return { ok: false, error: `Scope «${kind}» требует хотя бы одно значение.` }
  }
  if (!kind && values.length > 0) {
    return { ok: false, error: 'Укажите вид scope для перечисленных значений.' }
  }

  if (isUnrestrictedScope(draftScope(permission, draft)) && !draft.allowUnrestricted) {
    return {
      ok: false,
      error: 'Этот доступ ничем не ограничен. Подтвердите его отдельно, иначе он не будет выдан.',
    }
  }

  const consent: PluginCapabilityConsent = { capability: permission.capability }
  if (kind) consent.scopeKind = kind
  if (values.length > 0) consent.scopeValues = values
  if (isUnrestrictedScope(draftScope(permission, draft))) consent.allowUnrestricted = true

  const rawExpiry = draft.expiresInHours.trim()
  if (rawExpiry) {
    const hours = Number(rawExpiry)
    if (!Number.isInteger(hours) || hours <= 0) {
      return { ok: false, error: 'Срок действия указывается целым числом часов.' }
    }
    if (hours > maxPluginGrantHours) {
      return { ok: false, error: `Максимальный срок действия — ${maxPluginGrantHours} часов.` }
    }
    consent.expiresInHours = hours
  }

  return { ok: true, consent }
}

export interface EnableRequestResult {
  request?: PluginEnableRequest
  /** Per-capability problems, keyed by capability name. */
  errors: Record<string, string>
}

/**
 * Collects the approved rows into an enable request. Unapproved capabilities
 * are simply absent: the backend grants exactly what is listed, so leaving a
 * capability out is how the owner refuses it.
 */
export function buildEnableRequest(
  pluginId: string,
  permissions: readonly PluginPermission[],
  drafts: Readonly<Record<string, PluginConsentDraft>>,
): EnableRequestResult {
  const capabilities: PluginCapabilityConsent[] = []
  const errors: Record<string, string> = {}
  for (const permission of permissions) {
    const draft = drafts[permission.capability]
    if (!draft?.approved) continue
    const result = consentFromDraft(permission, draft)
    if (result.ok) capabilities.push(result.consent)
    else errors[permission.capability] = result.error
  }
  if (Object.keys(errors).length > 0) return { errors }
  return { request: { pluginId, capabilities }, errors }
}

/**
 * The wire payload for desktop.PluginEnableRequest. Field names are the Go
 * json tags verbatim; anything omitted is an `omitempty` field the backend
 * then fills from the manifest declaration.
 *
 * The unrestricted check is repeated here on purpose. This is the last place
 * the renderer controls before the bridge call, and an unconfirmed unbounded
 * grant must never be attempted — not merely be rejected on arrival.
 */
export function pluginEnablePayload(request: PluginEnableRequest): Record<string, unknown> {
  const capabilities = request.capabilities.map((consent) => {
    const kind = consent.scopeKind?.trim().toLowerCase() ?? ''
    const values = normalizeScopeValues(consent.scopeValues)
    if (isUnrestrictedScope({ kind, values }) && !consent.allowUnrestricted) {
      throw new Error(`Capability «${consent.capability}» выдаётся без ограничений и требует отдельного подтверждения.`)
    }
    const payload: Record<string, unknown> = { capability: consent.capability }
    if (kind) payload.scopeKind = kind
    if (values.length > 0) payload.scopeValues = values
    if (consent.allowUnrestricted) payload.allowUnrestricted = true
    if (consent.expiresInHours && consent.expiresInHours > 0) payload.expiresInHours = Math.floor(consent.expiresInHours)
    return payload
  })
  return { id: request.pluginId, pluginId: request.pluginId, capabilities }
}
