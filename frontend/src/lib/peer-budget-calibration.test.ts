import { describe, expect, it } from 'vitest'

import type { PeerDialogue } from './contracts'
import { buildPeerBudgetCalibration, peerBudgetRecommendationVerdict } from './peer-budget-calibration'

function dialogue(overrides: Partial<PeerDialogue> = {}): PeerDialogue {
  return {
    id: 'dialogue-1', initiatorAgentId: 'a', initiatorName: 'A', initiatorProviderId: 'codex', initiatorModel: 'luna',
    peerAgentId: 'b', peerName: 'B', peerProviderId: 'openrouter', peerModel: 'free/model',
    triggerKind: 'agent_tool', triggerReason: 'owner', purpose: 'review', status: 'completed',
    turnCount: 2, minTurns: 2, maxTurns: 4, tokensUsed: 3000, maxTokens: 8000,
    maxDurationSeconds: 90, durationUsedSeconds: 40, cooldownSeconds: 300,
    budgetOrigin: 'owner_recommendation', completionReason: 'semantic',
    recommendation: { minTurns: 2, maxTurns: 4, maxTokens: 8000, maxDurationSeconds: 90, basis: 'purpose_only', sampleCount: 0, confidence: 'low' },
    createdAt: '2026-09-01T10:00:00Z', finishedAt: '2026-09-01T10:00:40Z', messages: [],
    ...overrides,
  }
}

describe('peer budget calibration policy', () => {
  it('keeps a small route sample in collecting state and excludes non-recommended runs', () => {
    const groups = buildPeerBudgetCalibration([
      dialogue(),
      dialogue({ id: 'custom', budgetOrigin: 'owner_custom', recommendation: undefined }),
      dialogue({ id: 'running', status: 'running', finishedAt: undefined }),
    ])
    expect(groups).toEqual([expect.objectContaining({ samples: 1, requiredSamples: 5, hardStops: 0, status: 'collecting' })])
  })

  it('uses historical message routes and marks repeated hard stops only after five samples', () => {
    const dialogues = Array.from({ length: 5 }, (_, index) => dialogue({
      id: `tight-${index}`,
      completionReason: index === 0 ? 'max_tokens' : 'semantic',
      messages: [{
        id: `message-${index}`, sequence: 1, senderAgentId: 'b', senderName: 'B', recipientAgentId: 'a', recipientName: 'A',
        content: 'response', providerId: 'openrouter', model: 'historic/model', createdAt: '2026-09-01T10:00:20Z',
      }],
    }))
    const [group] = buildPeerBudgetCalibration(dialogues)
    expect(group).toMatchObject({ samples: 5, hardStops: 1, status: 'tight' })
    expect(group.route).toBe('codex · luna ↔ openrouter · historic/model')
  })

  it('describes one underused run without claiming that the route should be retuned', () => {
    expect(peerBudgetRecommendationVerdict(dialogue({ turnCount: 1, tokensUsed: 1000, durationUsedSeconds: 20 }))).toBe(
      'В этом запуске использована не более чем половина рекомендованного запаса.',
    )
  })
})
