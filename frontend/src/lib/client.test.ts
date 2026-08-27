import { beforeEach, describe, expect, it } from 'vitest'

import { createYuriClient, resetYuriClientForTests } from './client'

describe('Yuri client contract', () => {
  beforeEach(() => {
    resetYuriClientForTests()
  })

  it('provides a usable local preview when Wails bindings are absent', async () => {
    const client = createYuriClient()
    const conversations = await client.listConversations()

    expect(client.mode).toBe('mock')
    expect(conversations[0]?.id).toBe('conversation-welcome')

    const events: string[] = []
    const result = await client.sendMessage(
      { conversationId: 'conversation-welcome', text: 'Привет, Yuri' },
      (event) => events.push(event.type),
    )

    expect(result.status).toBe('complete')
    expect(events).toContain('run.started')
    expect(events).toContain('assistant.delta')
    expect(events).toContain('assistant.completed')
    expect(events.at(-1)).toBe('run.completed')
  })

  it('holds a side effect at approval until the user resolves it', async () => {
    const client = createYuriClient()
    const events: string[] = []
    const resultPromise = client.sendMessage(
      { conversationId: 'conversation-welcome', text: 'Запиши заметку в Documents' },
      (event) => {
        events.push(event.type)
        if (event.type === 'approval.required') void client.approve(event.approval.id, 'deny')
      },
    )

    const result = await resultPromise

    expect(result.status).toBe('error')
    expect(events).toEqual(expect.arrayContaining(['tool.started', 'approval.required', 'tool.updated', 'run.completed']))
  })

  it('keeps the offline memory and archive preview data-free', async () => {
    const client = createYuriClient()

    expect(await client.listMemories({ lifecycleState: 'active' })).toEqual([])
    expect(await client.searchArchive({ query: 'проект', includeDormant: true })).toEqual({
      results: [],
      total: 0,
      query: 'проект',
    })
  })
})
