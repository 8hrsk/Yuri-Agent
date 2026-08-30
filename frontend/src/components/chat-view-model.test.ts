import { describe, expect, it } from 'vitest'

import type { Conversation } from '../lib/contracts'
import { applyConversationTitleUpdate, committedStreamIdLimit, rememberCommittedStreamId, retireEarlierHistory } from './chat-view-model'

function conversation(overrides: Partial<Conversation> = {}): Conversation {
  return {
    id: 'conv-1',
    title: 'Диалог',
    preview: '',
    updatedAt: '2026-08-29T10:00:00.000Z',
    messages: [],
    ...overrides,
  }
}

/**
 * N-20. The set of flushed streaming ids used to grow for the whole lifetime of
 * the mounted view, and since H-9/M-37 that lifetime is the whole session:
 * `ChatView` is mounted once and hidden on a tab switch rather than unmounted.
 */
describe('the flushed-stream-id set is bounded (N-20)', () => {
  it('never grows past the bound, however many answers are flushed', () => {
    const committed = new Set<string>()
    for (let index = 0; index < committedStreamIdLimit * 4; index += 1) {
      rememberCommittedStreamId(committed, `msg-${index}`)
    }

    expect(committed.size).toBe(committedStreamIdLimit)
  })

  it('evicts the oldest ids and keeps the recent ones, which are the ones a late delta can name', () => {
    const committed = new Set<string>()
    const total = committedStreamIdLimit + 10
    for (let index = 0; index < total; index += 1) {
      rememberCommittedStreamId(committed, `msg-${index}`)
    }

    // The 10 oldest are gone; every id since is still there, so the window a
    // late delta can arrive in is covered many times over.
    expect(committed.has('msg-0')).toBe(false)
    expect(committed.has('msg-9')).toBe(false)
    expect(committed.has('msg-10')).toBe(true)
    expect(committed.has(`msg-${total - 1}`)).toBe(true)
  })

  it('is a no-op for an id it already holds', () => {
    const committed = new Set<string>(['msg-1'])
    rememberCommittedStreamId(committed, 'msg-1')

    expect([...committed]).toEqual(['msg-1'])
  })
})

describe('conversation title updates', () => {
  it('does not let a stale generated title roll back an owner rename', () => {
    const renamed = conversation({ title: 'Моё название', titleSource: 'user' })
    expect(applyConversationTitleUpdate(renamed, {
      title: 'Сгенерированное название',
      titleSource: 'generated',
    })).toBe(renamed)
  })

  it('does not let an older same-source event roll back a newer rename', () => {
    const renamed = conversation({ title: 'Новейшее название', titleSource: 'user', updatedAt: '2026-08-30T10:02:00.000Z' })
    expect(applyConversationTitleUpdate(renamed, {
      title: 'Старое название',
      titleSource: 'user',
      updatedAt: '2026-08-30T10:01:00.000Z',
    })).toBe(renamed)
  })

  it('applies generated title events to default conversations', () => {
    expect(applyConversationTitleUpdate(conversation({ title: 'Новый диалог', titleSource: 'default' }), {
      title: 'Разобраться с памятью',
      titleSource: 'generated',
      updatedAt: '2026-08-30T10:00:00.000Z',
    })).toMatchObject({ title: 'Разобраться с памятью', titleSource: 'generated', updatedAt: '2026-08-30T10:00:00.000Z' })
  })
})

/**
 * N-18. The control must not stay armed after a page that added nothing.
 */
describe('retiring “показать более ранние”', () => {
  it('clears the flag on a conversation that still offers the control', () => {
    const retired = retireEarlierHistory(conversation({ hasMoreMessages: true }))

    expect(retired.hasMoreMessages).toBe(false)
  })

  it('returns the same object when there is nothing to retire, so memoized children are not invalidated', () => {
    const already = conversation({ hasMoreMessages: false })
    const never = conversation()

    expect(retireEarlierHistory(already)).toBe(already)
    expect(retireEarlierHistory(never)).toBe(never)
  })
})
