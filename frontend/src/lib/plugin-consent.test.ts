import { describe, expect, it } from 'vitest'

import type { PluginPermission } from './contracts'
import {
  buildEnableRequest,
  consentFromDraft,
  declaredScope,
  draftNeedsUnrestrictedConfirmation,
  effectiveScope,
  emptyConsentDraft,
  isUnrestrictedScope,
  maxPluginGrantHours,
  pluginEnablePayload,
} from './plugin-consent'
import type { PluginConsentDraft } from './plugin-consent'

const network: PluginPermission = {
  capability: 'network.http',
  scope: 'network',
  scopeValues: ['*.example.test'],
  granted: false,
}

const files: PluginPermission = {
  capability: 'filesystem.read',
  scope: 'filesystem',
  scopeValues: ['/tmp/reference'],
  granted: false,
}

const unbounded: PluginPermission = {
  capability: 'system.exec',
  scope: 'unrestricted',
  granted: false,
}

function draft(patch: Partial<PluginConsentDraft> = {}): PluginConsentDraft {
  return { ...emptyConsentDraft(true), ...patch }
}

describe('declared scope', () => {
  it('reads the kind and values the manifest requested', () => {
    expect(declaredScope(network)).toEqual({ kind: 'network', values: ['*.example.test'] })
  })

  it('treats an absent declaration as unrestricted, the way the backend does', () => {
    // pluginConsentGrants substitutes domain.UnrestrictedScope() for an empty
    // manifest scope, so "unspecified" must not read as "narrow".
    expect(declaredScope({ capability: 'anything', granted: false })).toEqual({ kind: 'unrestricted', values: [] })
  })
})

describe('unrestricted detection', () => {
  it('flags scope kind unrestricted', () => {
    expect(isUnrestrictedScope({ kind: 'unrestricted', values: [] })).toBe(true)
  })

  it('flags a bare "*" value even when the kind looks narrow (N-8)', () => {
    // The whole point of the fix: kind alone is not the test. A network scope
    // of "*" is every host and must not slip past the confirmation gate.
    expect(isUnrestrictedScope({ kind: 'network', values: ['*'] })).toBe(true)
    expect(isUnrestrictedScope({ kind: 'filesystem', values: ['/tmp', '*'] })).toBe(true)
    expect(isUnrestrictedScope({ kind: 'resource', values: [' * '] })).toBe(true)
  })

  it('does not flag a bounded wildcard', () => {
    expect(isUnrestrictedScope({ kind: 'network', values: ['*.example.test'] })).toBe(false)
  })
})

describe('effective scope', () => {
  it('falls back to the declaration when the owner supplied no override', () => {
    expect(effectiveScope(declaredScope(network), { kind: '', values: [] }))
      .toEqual({ kind: 'network', values: ['*.example.test'] })
  })

  it('uses the override once either half of it is present', () => {
    expect(effectiveScope(declaredScope(network), { kind: 'network', values: ['api.example.test'] }))
      .toEqual({ kind: 'network', values: ['api.example.test'] })
  })
})

describe('consentFromDraft', () => {
  it('omits the scope entirely when the declaration is accepted verbatim', () => {
    const result = consentFromDraft(network, draft())
    // Both scope fields are omitempty: leaving them out is how the backend is
    // told to use the manifest declaration.
    expect(result).toEqual({ ok: true, consent: { capability: 'network.http' } })
  })

  it('sends a narrowed scope as scopeKind and scopeValues', () => {
    const result = consentFromDraft(network, draft({ scopeKind: 'network', scopeValues: 'api.example.test, cdn.example.test' }))
    expect(result).toEqual({
      ok: true,
      consent: { capability: 'network.http', scopeKind: 'network', scopeValues: ['api.example.test', 'cdn.example.test'] },
    })
  })

  it('requires a separate confirmation for a declared-unrestricted capability', () => {
    expect(consentFromDraft(unbounded, draft())).toMatchObject({ ok: false })
    expect(consentFromDraft(unbounded, draft({ allowUnrestricted: true })))
      .toEqual({ ok: true, consent: { capability: 'system.exec', allowUnrestricted: true } })
  })

  it('requires the same confirmation for a bare "*" value (N-8)', () => {
    const wildcard = draft({ scopeKind: 'network', scopeValues: '*' })
    expect(consentFromDraft(network, wildcard)).toMatchObject({ ok: false })
    expect(consentFromDraft(network, { ...wildcard, allowUnrestricted: true })).toEqual({
      ok: true,
      consent: { capability: 'network.http', scopeKind: 'network', scopeValues: ['*'], allowUnrestricted: true },
    })
  })

  it('does not carry a stale confirmation onto a scope that is no longer unbounded', () => {
    const narrowed = draft({ scopeKind: 'network', scopeValues: 'api.example.test', allowUnrestricted: true })
    expect(consentFromDraft(network, narrowed)).toEqual({
      ok: true,
      consent: { capability: 'network.http', scopeKind: 'network', scopeValues: ['api.example.test'] },
    })
  })

  it('rejects scopes the backend would call invalid', () => {
    expect(consentFromDraft(files, draft({ scopeKind: 'filesystem' }))).toMatchObject({ ok: false })
    expect(consentFromDraft(unbounded, draft({ scopeKind: 'unrestricted', scopeValues: '/tmp' }))).toMatchObject({ ok: false })
    expect(consentFromDraft(files, draft({ scopeValues: '/tmp/reference' }))).toMatchObject({ ok: false })
    expect(consentFromDraft(files, draft({ scopeKind: 'everything', scopeValues: '/tmp' }))).toMatchObject({ ok: false })
  })

  it('bounds the grant lifetime the way the backend does', () => {
    expect(consentFromDraft(files, draft({ expiresInHours: '24' })))
      .toEqual({ ok: true, consent: { capability: 'filesystem.read', expiresInHours: 24 } })
    expect(consentFromDraft(files, draft({ expiresInHours: '0' }))).toMatchObject({ ok: false })
    expect(consentFromDraft(files, draft({ expiresInHours: '1.5' }))).toMatchObject({ ok: false })
    expect(consentFromDraft(files, draft({ expiresInHours: String(maxPluginGrantHours + 1) }))).toMatchObject({ ok: false })
  })
})

describe('draftNeedsUnrestrictedConfirmation', () => {
  it('stays quiet for an unapproved capability', () => {
    expect(draftNeedsUnrestrictedConfirmation(unbounded, emptyConsentDraft(false))).toBe(false)
  })

  it('asks once the row is approved and the effective scope is unbounded', () => {
    expect(draftNeedsUnrestrictedConfirmation(unbounded, draft())).toBe(true)
    expect(draftNeedsUnrestrictedConfirmation(network, draft({ scopeKind: 'network', scopeValues: '*' }))).toBe(true)
    expect(draftNeedsUnrestrictedConfirmation(network, draft())).toBe(false)
  })
})

describe('buildEnableRequest', () => {
  const permissions = [network, files, unbounded]

  it('sends exactly the approved subset', () => {
    const result = buildEnableRequest('reference.demo', permissions, {
      'network.http': draft({ scopeKind: 'network', scopeValues: 'api.example.test' }),
      'filesystem.read': emptyConsentDraft(false),
      'system.exec': emptyConsentDraft(false),
    })
    expect(result.request).toEqual({
      pluginId: 'reference.demo',
      capabilities: [{ capability: 'network.http', scopeKind: 'network', scopeValues: ['api.example.test'] }],
    })
  })

  it('produces an empty consent list when nothing is approved', () => {
    const result = buildEnableRequest('reference.demo', permissions, {
      'network.http': emptyConsentDraft(false),
      'filesystem.read': emptyConsentDraft(false),
      'system.exec': emptyConsentDraft(false),
    })
    // A valid outcome: the plugin runs with no grants at all.
    expect(result.request).toEqual({ pluginId: 'reference.demo', capabilities: [] })
  })

  it('reports per-capability problems instead of dropping the row', () => {
    const result = buildEnableRequest('reference.demo', permissions, {
      'network.http': draft(),
      'filesystem.read': draft({ scopeKind: 'filesystem' }),
      'system.exec': draft(),
    })
    expect(result.request).toBeUndefined()
    expect(Object.keys(result.errors).sort()).toEqual(['filesystem.read', 'system.exec'])
  })
})

describe('pluginEnablePayload', () => {
  it('uses the Go json tag names verbatim', () => {
    expect(pluginEnablePayload({
      pluginId: 'reference.demo',
      capabilities: [{ capability: 'filesystem.read', scopeKind: 'filesystem', scopeValues: [' /tmp/a ', '', '/tmp/b'], expiresInHours: 12 }],
    })).toEqual({
      id: 'reference.demo',
      pluginId: 'reference.demo',
      capabilities: [{ capability: 'filesystem.read', scopeKind: 'filesystem', scopeValues: ['/tmp/a', '/tmp/b'], expiresInHours: 12 }],
    })
  })

  it('refuses an unconfirmed unbounded grant, by kind or by a bare "*"', () => {
    expect(() => pluginEnablePayload({
      pluginId: 'reference.demo',
      capabilities: [{ capability: 'system.exec', scopeKind: 'unrestricted' }],
    })).toThrow(/подтверждения/)
    expect(() => pluginEnablePayload({
      pluginId: 'reference.demo',
      capabilities: [{ capability: 'network.http', scopeKind: 'network', scopeValues: ['*'] }],
    })).toThrow(/подтверждения/)
  })
})
