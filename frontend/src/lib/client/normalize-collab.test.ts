import { describe, expect, it } from 'vitest'

import { normalizePeerDialogueList } from './normalize-collab'

describe('normalizePeerDialogueList', () => {
  it('keeps the camelCase peer policy and completion reason from the bridge', () => {
    const [dialogue] = normalizePeerDialogueList({
      items: [{
        id: 'peer-1',
        initiatorAgentId: 'agent-yuri',
        initiatorName: 'Юри',
        peerAgentId: 'agent-mira',
        peerName: 'Мира',
        triggerKind: 'autonomous',
        triggerReason: 'Нужен независимый взгляд.',
        purpose: 'Проверить план.',
        status: 'completed',
        turnCount: 3,
        minTurns: 2,
        maxTurns: 4,
        tokensUsed: 1500,
        maxTokens: 8000,
        maxDurationSeconds: 90,
        cooldownSeconds: 300,
        completionReason: 'semantic',
        createdAt: '2026-08-30T10:00:00.000Z',
      }],
    })

    expect(dialogue).toMatchObject({
      minTurns: 2,
      maxTurns: 4,
      maxDurationSeconds: 90,
      cooldownSeconds: 300,
      completionReason: 'semantic',
    })
  })

  it('supports legacy snake_case policy fields and hard-limit aliases', () => {
    const [dialogue] = normalizePeerDialogueList([{
      id: 'peer-legacy',
      initiator_agent_id: 'agent-yuri',
      peer_agent_id: 'agent-mira',
      trigger_kind: 'agent_tool',
      trigger_reason: 'Нужен совет.',
      purpose: 'Проверка.',
      state: 'completed',
      turn_count: 4,
      min_turns: 2,
      max_turns: 4,
      tokens_used: 7900,
      max_tokens: 8000,
      max_duration_seconds: 90,
      cooldown_seconds: 300,
      completion_reason: 'deadline',
      created_at: '2026-08-30T10:00:00.000Z',
    }])

    expect(dialogue).toMatchObject({
      minTurns: 2,
      maxTurns: 4,
      maxDurationSeconds: 90,
      cooldownSeconds: 300,
      completionReason: 'max_duration',
    })
  })

  it('falls back to the legacy minimum when older rows have only maxTurns', () => {
    const [dialogue] = normalizePeerDialogueList([{
      id: 'peer-old', initiator_agent_id: 'agent-yuri', peer_agent_id: 'agent-mira',
      trigger_kind: 'agent_tool', trigger_reason: 'Нужен совет.', purpose: 'Проверка.', state: 'running',
      turn_count: 0, max_turns: 1, tokens_used: 0, max_tokens: 1200,
      created_at: '2026-08-30T10:00:00.000Z',
    }])

    expect(dialogue).toMatchObject({ minTurns: 1, maxTurns: 1, maxDurationSeconds: 0, cooldownSeconds: 0 })
    expect(dialogue?.completionReason).toBeUndefined()
  })
})
