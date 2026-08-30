import { afterEach, describe, expect, it } from 'vitest'

import {
  normalizeConversationTitleUpdate,
  subscribeConversationUpdates,
} from './events'

type RuntimeListener = (value: unknown) => void

function installRuntime() {
  const listeners: RuntimeListener[] = []
  const previousWindow = (globalThis as { window?: unknown }).window
  const runtime = {
    EventsOn: (_name: string, callback: RuntimeListener) => {
      listeners.push(callback)
      return () => {
        const index = listeners.indexOf(callback)
        if (index >= 0) listeners.splice(index, 1)
      }
    },
  }
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { runtime } })
  return {
    emit: (value: unknown) => listeners.slice().forEach((listener) => listener(value)),
    restore: () => {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
    },
  }
}

afterEach(() => {
  delete (globalThis as { window?: unknown }).window
})

describe('conversation title events', () => {
  it('normalizes direct, snake_case and nested Wails payloads', () => {
    expect(normalizeConversationTitleUpdate({
      type: 'conversation.title.updated',
      conversation_id: 'conversation-1',
      title: 'Проверить документы',
      title_source: 'generated',
      updated_at: '2026-08-30T10:00:00.000Z',
    })).toEqual({
      type: 'conversation.title.updated',
      conversationId: 'conversation-1',
      title: 'Проверить документы',
      titleSource: 'generated',
      updatedAt: '2026-08-30T10:00:00.000Z',
    })

    expect(normalizeConversationTitleUpdate({
      data: {
        event_type: 'conversation.updated',
        conversation: { id: 'conversation-2', name: 'Релиз Yuri', title_source: 'user' },
      },
    })).toMatchObject({
      type: 'conversation.updated',
      conversationId: 'conversation-2',
      title: 'Релиз Yuri',
      titleSource: 'user',
    })
  })

  it('rejects incomplete payloads and delivers only the yuri:conversation channel', () => {
    const bus = installRuntime()
    const received: string[] = []
    const unsubscribe = subscribeConversationUpdates((update) => received.push(update.title))

    bus.emit({ type: 'conversation.title.updated', conversationId: 'conversation-1', title: 'Первый' })
    bus.emit({ type: 'conversation.title.updated', conversationId: 'conversation-1' })
    bus.emit({ type: 'conversation.title.updated', conversationId: 'conversation-1', title: 'Второй', titleSource: 'user' })

    expect(received).toEqual(['Первый', 'Второй'])
    unsubscribe()
    bus.emit({ conversationId: 'conversation-1', title: 'После cleanup' })
    expect(received).toEqual(['Первый', 'Второй'])
    bus.restore()
  })
})
