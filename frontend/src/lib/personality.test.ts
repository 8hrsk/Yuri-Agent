import { beforeEach, describe, expect, it } from 'vitest'

import {
  createYuriClient,
  normalizePersonalitySnapshot,
  resetYuriClientForTests,
} from './client'
import { mapAvatarState } from './personality'

describe('personality client contract', () => {
  beforeEach(() => {
    resetYuriClientForTests()
  })

  it('normalizes bounded traits, affect and explicitly subjective evidence', () => {
    const snapshot = normalizePersonalitySnapshot({
      persona_id: 'persona-42',
      current_version: 'v7',
      auto_evolution: 'false',
      traits: [{ id: 'directness', name: 'Directness', value: 87, min: 25, max: 90, pinned: 'true' }],
      affective_state: {
        mood: 'Focused',
        valence: 70,
        emotions: { joy: 88, anxiety: 130 },
      },
      subjective_opinions: [{
        id: 'op-1',
        subject: 'owner',
        statement: 'Likely values clear explanations.',
        kind: 'inference',
        confidence: 140,
        evidence: [{ source_type: 'message', source_id: 'm-1', excerpt: 'Please explain why.' }],
      }],
      relationship_state: {
        version: 3,
        dimensions_json: { trust: 65 },
        reason: 'Repeated reliable interactions.',
        versions: [{ id: 'relationship-v3', version: 3, operation: 'update', dimensions: { trust: 65 }, diff: { trust: -12 }, reason: 'Trust was revised.', evidence: [] }],
      },
    })

    expect(snapshot.id).toBe('persona-42')
    expect(snapshot.currentVersion).toBe(7)
    expect(snapshot.autoEvolution).toBe(false)
    expect(snapshot.traits[0]).toMatchObject({ value: 0.87, min: 0.25, max: 0.9, pinned: true })
    expect(snapshot.affect).toMatchObject({ valence: 0.7, mood: 'Focused' })
    expect(snapshot.affect.dimensions.find((dimension) => dimension.id === 'anxiety')?.value).toBe(1)
    expect(snapshot.opinions[0]).toMatchObject({ label: 'inference', confidence: 1 })
    expect(snapshot.opinions[0]?.evidence[0]).toMatchObject({ sourceType: 'message', sourceId: 'm-1' })
    expect(snapshot.relationship.dimensions[0]).toMatchObject({ id: 'trust', value: 0.65 })
    expect(snapshot.relationship.versions[0]).toMatchObject({ id: 'relationship-v3', operation: 'update', diff: { trust: -0.12 } })
  })

  it('maps run and voice states to the finite avatar state machine', () => {
    expect(mapAvatarState('idle')).toBe('idle')
    expect(mapAvatarState('idle', true)).toBe('listening')
    expect(mapAvatarState('thinking')).toBe('thinking')
    expect(mapAvatarState('tool_running')).toBe('tool_running')
    expect(mapAvatarState('waiting_approval')).toBe('tool_running')
    expect(mapAvatarState('idle', false, true)).toBe('speaking')
    expect(mapAvatarState('error', true)).toBe('error')
    expect(mapAvatarState('cancelled')).toBe('idle')
  })

  it('keeps offline owner relationship recovery append-only and separate from persona', async () => {
    const client = createYuriClient()
    const before = await client.getPersonaSnapshot()
    const target = before.relationship.versions[0]
    const rolledBack = await client.rollbackRelationship(target.id)
    expect(rolledBack?.relationship.version).toBeGreaterThan(before.relationship.version)
    expect(rolledBack?.relationship.versions).toHaveLength(before.relationship.versions.length + 1)
    expect(rolledBack?.currentVersion).toBe(before.currentVersion)

    const reset = await client.resetRelationship()
    expect(reset?.relationship.version).toBeGreaterThan(rolledBack?.relationship.version ?? 0)
    expect(reset?.relationship.versions.at(-1)?.operation).toBe('reset')
    expect(reset?.currentVersion).toBe(before.currentVersion)
  })

  it('forwards rollback and reset through typed Wails bridge calls', async () => {
    const calls: Array<{ name: string; args: unknown[] }> = []
    const bridge = {
      ListConversations: () => [],
      GetPersonaSnapshot: () => ({
        id: 'persona-42',
        currentVersion: 2,
        traits: [{ id: 'directness', value: 0.7, min: 0.2, max: 0.9, pinned: true }],
        versions: [{ id: 'persona-v2', version: 2, reason: 'Current', traits: [{ id: 'directness', value: 0.7, min: 0.2, max: 0.9, pinned: true }] }],
      }),
      RollbackPersona: (request: unknown) => {
        calls.push({ name: 'RollbackPersona', args: [request] })
        return { id: 'persona-42', currentVersion: 1, versions: [{ id: 'persona-v1', version: 1, reason: 'Seed', traits: [] }] }
      },
      ResetPersona: (request: unknown) => {
        calls.push({ name: 'ResetPersona', args: [request] })
        return { id: 'persona-42', currentVersion: 1, versions: [{ id: 'persona-v1', version: 1, reason: 'Seed', traits: [] }] }
      },
      RollbackRelationship: (request: unknown) => {
        calls.push({ name: 'RollbackRelationship', args: [request] })
        return { id: 'persona-42', relationship: { id: 'owner', version: 3, dimensions: {}, versions: [] } }
      },
      ResetRelationship: (request: unknown) => {
        calls.push({ name: 'ResetRelationship', args: [request] })
        return { id: 'persona-42', relationship: { id: 'owner', version: 4, dimensions: {}, versions: [] } }
      },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })

    try {
      const client = createYuriClient()
      await client.rollbackPersona('persona-v1')
      await client.resetPersona()
      await client.rollbackRelationship('relationship-v1')
      await client.resetRelationship()
      expect(calls).toEqual([
        { name: 'RollbackPersona', args: [{ id: 'persona-v1', versionId: 'persona-v1', version_id: 'persona-v1' }] },
        { name: 'ResetPersona', args: [{}] },
        { name: 'RollbackRelationship', args: [{ id: 'relationship-v1', versionId: 'relationship-v1', version_id: 'relationship-v1' }] },
        { name: 'ResetRelationship', args: [{}] },
      ])
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })
})
