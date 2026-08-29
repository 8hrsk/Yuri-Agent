import { describe, expect, it } from 'vitest'

import { defaultAgentDraft, normalizeAgentProfile, validateAgentDraft } from './agents'
import { createYuriClient, resetYuriClientForTests } from './client'

describe('agent profile contracts', () => {
  it('normalizes bridge payloads and bounds traits', () => {
    expect(normalizeAgentProfile({
      agent_id: 'agent-yuri', display_name: ' Yuri ', age: 21, gender: 'female',
      short_preferences: 'Коротко', initial_traits: { warmth: 2, jealousy: -1, 'bad key': 0.5 }, is_active: true,
      created_at: '2026-08-29T00:00:00Z',
    })).toMatchObject({
      id: 'agent-yuri', name: 'Yuri', age: 21, preferences: 'Коротко',
      traits: { warmth: 1, jealousy: 0 }, active: true,
    })
  })

  it('validates owner-controlled identity fields', () => {
    expect(validateAgentDraft({ ...defaultAgentDraft, name: ' ' })).toBe('Укажите имя агента.')
    expect(validateAgentDraft({ ...defaultAgentDraft, age: 201 })).toContain('от 1 до 200')
    expect(validateAgentDraft(defaultAgentDraft)).toBeUndefined()
  })

  it('keeps the mock roster and active selection coherent', async () => {
    resetYuriClientForTests()
    const client = createYuriClient()
    const first = await client.createAgent({ ...defaultAgentDraft, name: 'Yuri' })
    const second = await client.createAgent({ ...defaultAgentDraft, name: 'Sora' })

    expect((await client.listAgents()).map((agent) => ({ name: agent.name, active: agent.active }))).toEqual([
      { name: 'Yuri', active: false },
      { name: 'Sora', active: true },
    ])

    await expect(client.getActiveAgent()).resolves.toMatchObject({ id: second.id, name: 'Sora', active: true })
    await expect(client.setActiveAgent(first.id)).resolves.toMatchObject({ id: first.id, name: 'Yuri', active: true })
    expect((await client.listAgents()).map((agent) => agent.active)).toEqual([true, false])
  })
})
