import { describe, expect, it } from 'vitest'

import { normalizeRunUsageStats } from './normalize-usage'

describe('normalizeRunUsageStats', () => {
  it('normalizes bridge casing and keeps failure/status buckets', () => {
    expect(normalizeRunUsageStats({
      from: '2026-08-01T00:00:00Z',
      to: '2026-09-01T00:00:00Z',
      groups: [{
        agent_id: 'agent-emily',
        agent_name: 'Emily',
        provider_id: 'openrouter',
        model: 'vendor/free',
        run_count: '3',
        status_counts: { completed: 2, failed: 1 },
        failure_kinds: { provider: 1 },
        input_tokens: '120',
        output_tokens: 80,
        total_tokens: 200,
      }],
    })).toEqual({
      from: '2026-08-01T00:00:00Z',
      to: '2026-09-01T00:00:00Z',
      groups: [{
        agentId: 'agent-emily',
        agentName: 'Emily',
        providerId: 'openrouter',
        model: 'vendor/free',
        runCount: 3,
        statusCounts: { completed: 2, failed: 1 },
        failureKinds: { provider: 1 },
        inputTokens: 120,
        outputTokens: 80,
        totalTokens: 200,
      }],
    })
  })

  it('returns a safe empty report for a missing bridge response', () => {
    expect(normalizeRunUsageStats(undefined, { from: 'from', to: 'to' })).toEqual({ from: 'from', to: 'to', groups: [] })
  })
})
