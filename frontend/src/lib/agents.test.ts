// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'

import {
  AGENT_BACKSTORY_MAX_LENGTH,
  applyAgentPreset,
  clearAgentDraft,
  defaultAgentDraft,
  defaultAgentTraits,
  loadAgentDraft,
  normalizeAgentPersonalizationProfile,
  normalizeAgentProfile,
  normalizeAgentProfileInput,
  saveAgentDraft,
  validateAgentDraft,
} from './agents'
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

  it('normalizes a fictional backstory from bridge aliases and bounds it by runes', () => {
    const backstory = `${'Ю'.repeat(AGENT_BACKSTORY_MAX_LENGTH)}лишнее`
    const profile = normalizeAgentProfile({
      id: 'agent-yuri', name: 'Юри', gender: 'female', identity_backstory: backstory,
    })

    expect(profile?.backstory).toHaveLength(AGENT_BACKSTORY_MAX_LENGTH)
    expect(profile?.backstory).toBe('Ю'.repeat(AGENT_BACKSTORY_MAX_LENGTH))
  })

  it('normalizes the opt-in fallback route from both wire naming conventions', () => {
    const input = normalizeAgentProfileInput({
      ...defaultAgentDraft,
      name: 'Emily',
      fallback_enabled: true,
      fallback_provider_id: ' openrouter ',
      fallback_model: ' openrouter/free ',
      personalization: defaultAgentDraft.personalization,
    })
    expect(input).toMatchObject({ fallbackEnabled: true, fallbackProviderId: 'openrouter', fallbackModel: 'openrouter/free' })

    const profile = normalizeAgentProfile({
      id: 'agent-emily', name: 'Emily', gender: 'female', fallbackEnabled: true,
      fallback_provider_id: 'openrouter', fallback_model: 'openrouter/free',
    })
    expect(profile).toMatchObject({ fallbackEnabled: true, fallbackProviderId: 'openrouter', fallbackModel: 'openrouter/free' })
  })

  it('keeps the complete starter trait vocabulary in the draft', () => {
    expect(Object.keys(defaultAgentTraits)).toHaveLength(25)
    expect(defaultAgentDraft.backstory).toBe('')
    expect(Object.keys(defaultAgentTraits)).toEqual(expect.arrayContaining([
      'warmth', 'directness', 'emotionality', 'playfulness', 'jealousy', 'irritability',
      'empathy', 'sociability', 'shyness', 'anxiety', 'fearfulness', 'emotional_stability',
      'sensitivity', 'possessiveness', 'romantic_tone', 'initiative', 'impulsivity',
      'stubbornness', 'optimism', 'curiosity', 'suspicion', 'trust', 'attachment', 'formality', 'tsundere',
    ]))
  })

  it('applies visible presets without destroying owner-authored identity or backstory', () => {
    const source = {
      ...defaultAgentDraft,
      name: 'Emilu',
      backstory: 'Жила у моря.',
      personalization: {
        ...defaultAgentDraft.personalization,
        identity: { ...defaultAgentDraft.personalization.identity, pronouns: 'она/её', role: 'архивистка' },
        structuredBackstory: { ...defaultAgentDraft.personalization.structuredBackstory, narrative: 'Жила у моря.' },
      },
    }
    const result = applyAgentPreset(source, 'reserved')

    expect(result).toMatchObject({ name: 'Emilu', backstory: 'Жила у моря.', presetId: 'reserved' })
    expect(result.personalization.identity).toMatchObject({ pronouns: 'она/её', role: 'архивистка' })
    expect(result.personalization.structuredBackstory.narrative).toBe('Жила у моря.')
    expect(result.traits.shyness).toBe(0.84)
    expect(result.personalization.emotionalDynamics.conflictStyle).toBe('withdraw')
  })

  it('restores a versioned local draft and fills fields added by newer schemas', () => {
    clearAgentDraft()
    window.localStorage.setItem('yuri.agent-profile-draft.v2', JSON.stringify({
      name: 'Sora', creationMode: 'advanced', traits: { fearfulness: 0.77 },
      personalization: { identity: { preferredLanguage: 'ja-JP' } },
    }))

    const restored = loadAgentDraft()
    expect(restored).toMatchObject({ name: 'Sora', creationMode: 'advanced' })
    expect(restored.traits).toMatchObject({ warmth: defaultAgentTraits.warmth, fearfulness: 0.77 })
    expect(restored.personalization.identity).toMatchObject({ preferredLanguage: 'ja-JP', pronouns: 'она/её' })

    saveAgentDraft({ ...restored, name: 'Sora II' })
    expect(JSON.parse(window.localStorage.getItem('yuri.agent-profile-draft.v2') ?? '{}')).toMatchObject({ name: 'Sora II' })
  })

  it('normalizes the bridge personalization view with camelCase and snake_case aliases', () => {
    const profile = normalizeAgentPersonalizationProfile({
      agent_id: 'agent-emilu', schema_version: 2, version: 3, revision_id: 'revision-3',
      identity: { preferred_language: 'ru-RU', pronouns: 'она/её', role: 'исследовательница' },
      communication_style: { verbosity: 0.7, emoji_frequency: 0.15 },
      temperament: { warmth: 0.8, custom: { fearfulness: 0.65 } },
      emotional_dynamics: { reactivity: 0.75, conflict_style: 'direct', triggers: { fear: ['темнота'] } },
      relationship_seed: { preset: 'friends', dimensions: { trust: 0.8 }, summary: 'Давние друзья.' },
      backstory: { narrative: 'Помнит старый маяк.', summary: 'Архивистка.', episodes: [{ id: 'lighthouse', content: 'Нашла карту.', emotional_valence: 0.4 }] },
      evolution_policy: { locked_fields: ['identity'], trait_bounds: { warmth: { min: 0.4, max: 1 } }, reflection_mode: 'disabled', reflection_cooldown_minutes: 90, reflection_max_tokens: 1800, reflection_max_duration_seconds: 45, reflection_max_evidence: 6 },
      created_at: '2026-08-31T10:00:00Z', updated_at: '2026-08-31T11:00:00Z',
    })

    expect(profile).toMatchObject({ agentId: 'agent-emilu', schemaVersion: 2, version: 3, revisionId: 'revision-3' })
    expect(profile?.identity).toMatchObject({ preferredLanguage: 'ru-RU', role: 'исследовательница' })
    expect(profile?.temperament).toMatchObject({ warmth: 0.8, fearfulness: 0.65 })
    expect(profile?.emotionalDynamics).toMatchObject({ conflictStyle: 'direct', triggers: { fear: ['темнота'] } })
    expect(profile?.structuredBackstory.episodes[0]).toMatchObject({ id: 'lighthouse', content: 'Нашла карту.', emotionalValence: 0.4 })
    expect(profile?.evolutionPolicy).toMatchObject({ reflectionMode: 'disabled', reflectionCooldownMinutes: 90, reflectionMaxTokens: 1800, reflectionMaxDurationSeconds: 45, reflectionMaxEvidence: 6 })
  })

  it('validates owner-controlled identity fields', () => {
    expect(validateAgentDraft({ ...defaultAgentDraft, name: ' ' })).toBe('Укажите имя агента.')
    expect(validateAgentDraft({ ...defaultAgentDraft, age: 201 })).toContain('от 1 до 200')
    expect(validateAgentDraft({ ...defaultAgentDraft, backstory: 'я'.repeat(AGENT_BACKSTORY_MAX_LENGTH + 1) })).toContain('12001')
    expect(validateAgentDraft(defaultAgentDraft)).toBeUndefined()
  })

  it('keeps the mock roster and active selection coherent', async () => {
    resetYuriClientForTests()
    const client = createYuriClient()
    const first = await client.createAgent({ ...defaultAgentDraft, name: 'Yuri', backstory: 'Жила у моря.' })
    const second = await client.createAgent({ ...defaultAgentDraft, name: 'Sora' })

    expect((await client.listAgents()).map((agent) => ({ name: agent.name, active: agent.active }))).toEqual([
      { name: 'Yuri', active: false },
      { name: 'Sora', active: true },
    ])

    await expect(client.getActiveAgent()).resolves.toMatchObject({ id: second.id, name: 'Sora', backstory: '', active: true })
    await expect(client.setActiveAgent(first.id)).resolves.toMatchObject({ id: first.id, name: 'Yuri', active: true })
    await expect(client.getActiveAgent()).resolves.toMatchObject({ id: first.id, backstory: 'Жила у моря.' })
    expect((await client.listAgents()).map((agent) => agent.active)).toEqual([true, false])
  })
})
