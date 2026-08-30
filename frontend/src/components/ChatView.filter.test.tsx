// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { BackendConnection } from '../lib/backend'
import type { ChatEvent, ChatRequest, Conversation, RunResult, YuriClient } from '../lib/contracts'
import { ChatView } from './ChatView'

/**
 * M-41. The sidebar filter used to run in the render body and normalize the
 * query inside the predicate, so every streamed token cost two
 * `toLocaleLowerCase` calls and one concatenation per conversation on a list
 * that had not changed.
 *
 * The C-1 render-count guard does not catch this: `formatClock` is called by
 * the memoized *row*, whose props stay identical, so the rows are skipped while
 * the filter still runs. The wasted work is therefore measured directly.
 */

type EventSink = (event: ChatEvent) => void

let clientStub: YuriClient

vi.mock('../lib/client', () => ({
  createYuriClient: () => clientStub,
}))

const backend: BackendConnection = { status: 'connected', label: 'Backend connected', detail: 'Wails runtime is ready' }

function conversationFixture(index: number): Conversation {
  const minute = String(index % 60).padStart(2, '0')
  return {
    id: `conv-${index}`,
    title: index === 0 ? 'Отчёт по проекту' : `Диалог ${index}`,
    preview: index === 1 ? 'ПЛАНЁРКА в среду' : `Превью ${index}`,
    updatedAt: `2026-08-29T10:${minute}:00.000Z`,
    messages: [],
    traces: [],
  }
}

function createHarness(conversationCount: number) {
  let handle: { emit: EventSink; settle: (result: RunResult) => void } | undefined
  const sendMessage = vi.fn((_request: ChatRequest, onEvent: EventSink) => new Promise<RunResult>((resolve) => {
    handle = { emit: onEvent, settle: resolve }
  }))
  clientStub = {
    mode: 'mock',
    listConversations: async () => Array.from({ length: conversationCount }, (_, index) => conversationFixture(index)),
    // The conversation list carries metadata only, so opening a conversation
    // fetches its transcript. These fixtures ship their own, so the fetch is
    // never reached; the stub is here because the client interface has it.
    listMessages: async (conversationId: string) => ({ conversationId, messages: [], traces: [], hasMore: false }),
    createConversation: async () => conversationFixture(0),
    listChatTools: async () => [],
    getAllowedDirectories: async () => [],
    transcribeAudio: async () => '',
    sendMessage,
    retryLast: sendMessage,
    cancelRun: vi.fn(async () => {}),
    approve: vi.fn(async () => {}),
  } as unknown as YuriClient
  return {
    sendMessage,
    run: () => {
      if (!handle) throw new Error('the run has not started yet')
      return handle
    },
  }
}

/**
 * Counts locale-aware lowercasing, which is what the filter does per candidate.
 * Installed around a single action so the count is attributable to that action.
 */
function countLocaleLowercase(action: () => void): number {
  const original = String.prototype.toLocaleLowerCase
  let calls = 0
  String.prototype.toLocaleLowerCase = function patched(this: string, ...args: unknown[]) {
    calls += 1
    return original.apply(this, args as Parameters<typeof original>)
  } as typeof original
  try {
    action()
  } finally {
    String.prototype.toLocaleLowerCase = original
  }
  return calls
}

const originalRaf = globalThis.requestAnimationFrame

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  // Without this the coalesced autoscroll never runs and the committed render
  // path differs from the one the streaming suite exercises.
  globalThis.requestAnimationFrame = ((callback: FrameRequestCallback) => {
    callback(0)
    return 1
  }) as typeof globalThis.requestAnimationFrame
  Object.defineProperty(window, 'speechSynthesis', {
    configurable: true,
    value: { cancel: vi.fn(), speak: vi.fn() },
  })
})

afterEach(() => {
  globalThis.requestAnimationFrame = originalRaf
})

async function startRun(conversationCount: number) {
  const user = userEvent.setup()
  const harness = createHarness(conversationCount)
  render(<ChatView agentName="Yuri" backend={backend} onOpenSettings={vi.fn()} />)
  const composer = await screen.findByRole('textbox', { name: 'Сообщение Yuri' })
  await waitFor(() => expect(screen.getByText('Диалог 1')).toBeInTheDocument())
  await user.type(composer, 'Привет')
  await user.click(screen.getByRole('button', { name: 'Отправить сообщение' }))
  await waitFor(() => expect(harness.sendMessage).toHaveBeenCalledTimes(1))
  act(() => {
    harness.run().emit({ type: 'run.started', runId: 'run-1' })
    harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: 'Начало' })
  })
  return { harness, user }
}

describe('the sidebar filter does not re-run while an answer streams (M-41)', () => {
  it('spends no locale-lowercasing on a token that cannot change the list', async () => {
    const { harness } = await startRun(60)

    const cost = countLocaleLowercase(() => {
      act(() => {
        harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: ' ещё' })
      })
    })

    // Neither the conversations nor the query changed, so the filter must not
    // run at all. Unmemoized this was 2 calls per conversation, per token.
    expect(cost).toBe(0)
    expect(screen.getByText('Диалог 1')).toBeInTheDocument()
  })

  it('costs the same per token whatever the sidebar holds', async () => {
    const few = await startRun(4)
    const fewCost = countLocaleLowercase(() => {
      act(() => { few.harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: ' x' }) })
    })
    cleanup()

    const many = await startRun(200)
    const manyCost = countLocaleLowercase(() => {
      act(() => { many.harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: ' x' }) })
    })

    // Growing with the conversation count was the whole of M-41.
    expect(fewCost).toBe(0)
    expect(manyCost).toBe(fewCost)
  })

  it('normalizes the query once per keystroke, not once per candidate', async () => {
    const { user } = await startRun(50)
    const search = screen.getByRole('textbox', { name: 'Поиск диалогов' })

    let cost = 0
    const original = String.prototype.toLocaleLowerCase
    let calls = 0
    String.prototype.toLocaleLowerCase = function patched(this: string, ...args: unknown[]) {
      calls += 1
      return original.apply(this, args as Parameters<typeof original>)
    } as typeof original
    try {
      await user.type(search, 'планёрка')
      cost = calls
    } finally {
      String.prototype.toLocaleLowerCase = original
    }

    // 8 keystrokes over 50 conversations. The ceiling is one filter pass per
    // keystroke: one normalization per candidate plus one for the query, so
    // 8 * 51 = 408. Hoisting the query back inside the predicate doubles the
    // per-candidate half to 816, and losing the memo multiplies it again by the
    // number of renders per keystroke.
    expect(cost).toBeLessThanOrEqual(8 * 51)
  })
})

describe('the sidebar filter still filters (M-41)', () => {
  it('matches title and preview case-insensitively, and trims the query', async () => {
    const { user } = await startRun(6)
    const search = screen.getByRole('textbox', { name: 'Поиск диалогов' })
    // The selected conversation's title is also the chat header, so the
    // assertions have to be scoped to the sidebar list.
    const list = () => within(document.querySelector('.conversation-list') as HTMLElement)

    await user.type(search, '  ОТЧЁТ  ')
    expect(list().getByText('Отчёт по проекту')).toBeInTheDocument()
    expect(list().queryByText('Диалог 2')).not.toBeInTheDocument()

    await user.clear(search)
    // The preview is searched too, not just the title.
    await user.type(search, 'планёрка')
    expect(list().getByText('Диалог 1')).toBeInTheDocument()
    expect(list().queryByText('Отчёт по проекту')).not.toBeInTheDocument()

    await user.clear(search)
    await user.type(search, 'нет такого')
    expect(list().getByText('Диалоги не найдены')).toBeInTheDocument()

    await user.clear(search)
    expect(list().getByText('Отчёт по проекту')).toBeInTheDocument()
    expect(list().getByText('Диалог 5')).toBeInTheDocument()
  })
})
