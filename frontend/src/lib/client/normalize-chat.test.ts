import { describe, expect, it } from 'vitest'

import { normalizeChatEvent } from './normalize-chat'

describe('chat event normalization', () => {
  it('normalizes run.fallback route metadata and drops provider secrets', () => {
    const event = normalizeChatEvent({
      data: {
        type: 'run_fallback',
        run_id: 'run-fallback',
        from_provider_id: 'primary',
        from_model: 'model/main',
        to_provider_id: 'backup',
        to_model: 'model/reserve',
        reason: 'Основной маршрут временно недоступен',
        apiKey: 'sk-event-secret',
        credentials: { token: 'event-secret' },
      },
    })

    expect(event).toEqual({
      type: 'run.fallback',
      runId: 'run-fallback',
      fromProviderId: 'primary',
      fromModel: 'model/main',
      toProviderId: 'backup',
      toModel: 'model/reserve',
      reason: 'Основной маршрут временно недоступен',
    })
    expect(JSON.stringify(event)).not.toContain('sk-event-secret')
    expect(JSON.stringify(event)).not.toContain('credentials')
  })

  it('accepts nested fallback metadata while using a safe default reason', () => {
    expect(normalizeChatEvent({
      type: 'run.fallback',
      runId: 'run-nested-fallback',
      fallback: {
        fromProviderId: 'primary',
        toProviderId: 'backup',
        toModel: 'model/reserve',
      },
    })).toMatchObject({
      type: 'run.fallback',
      runId: 'run-nested-fallback',
      fromProviderId: 'primary',
      toProviderId: 'backup',
      toModel: 'model/reserve',
      reason: 'Выбран резервный маршрут',
    })
  })
})
