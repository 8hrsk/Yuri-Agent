// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { BackendConnection } from '../lib/backend'
import type { ChatEvent, ChatMessage, ChatRequest, Conversation, RunResult, YuriClient } from '../lib/contracts'
import { ChatView } from './ChatView'

/**
 * `formatClock` is called exactly once per rendered message bubble and once per
 * rendered conversation row, so counting the calls between two streaming
 * deltas counts the components React actually re-rendered. That is the direct
 * regression guard for C-1: before the fix every delta rebuilt the timeline and
 * re-rendered the whole history, so the count grew with the conversation.
 */
const { clockCalls } = vi.hoisted(() => ({ clockCalls: [] as string[] }))

vi.mock('../lib/datetime', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/datetime')>()
  return {
    ...actual,
    formatClock: (date: Date) => {
      clockCalls.push(date.toISOString())
      return actual.formatClock(date)
    },
  }
})

type EventSink = (event: ChatEvent) => void

let clientStub: YuriClient

vi.mock('../lib/client', () => ({
  createYuriClient: () => clientStub,
}))

const backend: BackendConnection = { status: 'connected', label: 'Backend connected', detail: 'Wails runtime is ready' }

function historyMessage(index: number): ChatMessage {
  const minute = String(index % 60).padStart(2, '0')
  return {
    id: `history-${index}`,
    role: index % 2 === 0 ? 'user' : 'assistant',
    content: `Реплика ${index}`,
    status: 'complete',
    createdAt: `2026-08-29T09:${minute}:00.000Z`,
  }
}

function conversationFixture(messageCount: number): Conversation {
  return {
    id: 'conv-1',
    title: 'Длинный диалог',
    preview: '',
    updatedAt: '2026-08-29T10:00:00.000Z',
    messages: Array.from({ length: messageCount }, (_, index) => historyMessage(index)),
    traces: [],
  }
}

type RunHandle = { emit: EventSink; settle: (result: RunResult) => void }

function createHarness(messageCount: number) {
  let handle: RunHandle | undefined
  const sendMessage = vi.fn((_request: ChatRequest, onEvent: EventSink) => new Promise<RunResult>((resolve) => {
    handle = { emit: onEvent, settle: resolve }
  }))
  clientStub = {
    mode: 'mock',
    listConversations: async () => [conversationFixture(messageCount)],
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

async function startRun(messageCount: number) {
  const user = userEvent.setup()
  const harness = createHarness(messageCount)
  render(<ChatView agentName="Yuri" backend={backend} onOpenSettings={vi.fn()} />)
  const composer = await screen.findByRole('textbox', { name: 'Сообщение Yuri' })
  await user.type(composer, 'Продолжай')
  await user.click(screen.getByRole('button', { name: 'Отправить сообщение' }))
  await waitFor(() => expect(harness.sendMessage).toHaveBeenCalledTimes(1))
  act(() => {
    harness.run().emit({ type: 'run.started', runId: 'run-1' })
    harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: 'Начало' })
  })
  return { harness, user }
}

/** Renders caused by one more delta on an already streaming answer. */
function rendersPerDelta(harness: ReturnType<typeof createHarness>, delta: string): number {
  clockCalls.length = 0
  act(() => {
    harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta })
  })
  return clockCalls.length
}

beforeEach(() => {
  clockCalls.length = 0
  Element.prototype.scrollIntoView = vi.fn()
  Object.defineProperty(window, 'speechSynthesis', {
    configurable: true,
    value: { cancel: vi.fn(), speak: vi.fn() },
  })
})

describe('streaming does not re-render the conversation (C-1)', () => {
  it('re-renders only the streaming answer, never the rest of the history', async () => {
    const { harness } = await startRun(24)

    // 24 history bubbles + the user message + one conversation row all render
    // on the first pass, and none of them may render again for a token.
    expect(rendersPerDelta(harness, ' продолжения')).toBe(1)
    expect(rendersPerDelta(harness, ' и ещё')).toBe(1)

    expect(screen.getByText('Начало продолжения и ещё')).toBeInTheDocument()
    // The untouched history is still on screen — it was skipped, not dropped.
    expect(screen.getByText('Реплика 0')).toBeInTheDocument()
    expect(screen.getByText('Реплика 23')).toBeInTheDocument()
  })

  it('costs the same per token whatever the conversation length', async () => {
    const short = await startRun(4)
    const shortCost = rendersPerDelta(short.harness, ' ещё')
    cleanup()

    const long = await startRun(80)
    const longCost = rendersPerDelta(long.harness, ' ещё')

    // Linear degradation with history length was the whole of C-1.
    expect(shortCost).toBe(1)
    expect(longCost).toBe(shortCost)
  })

  it('commits the buffered answer into the conversation when the run completes', async () => {
    const { harness } = await startRun(2)

    act(() => {
      harness.run().emit({ type: 'assistant.completed', runId: 'run-1', messageId: 'msg-1' })
      harness.run().emit({ type: 'run.completed', runId: 'run-1', status: 'complete' })
    })

    expect(screen.queryByText('Печатает')).not.toBeInTheDocument()
    // Exactly one bubble carries the answer: the buffer moved, it did not fork.
    expect(screen.getAllByText('Начало')).toHaveLength(1)
    const bubble = screen.getByText('Начало').closest('.message')
    expect(bubble).toHaveClass('message--complete')
    expect(bubble?.querySelector('.message-action')).toHaveAccessibleName('Озвучить ответ')
  })

  /**
   * The job of `committedStreamIdsRef`: between the flush and the commit that
   * refreshes `latestRef`, the durable state is not yet visible to the event
   * handler, so a delta that arrives in that window would otherwise be treated
   * as a brand-new message and open a second bubble carrying the same answer.
   *
   * Pinned here because N-20 bounds that set — eviction must never reach far
   * enough back to re-open this hole.
   */
  it('grows the committed answer in place when a delta lands after the flush', async () => {
    const { harness } = await startRun(2)

    act(() => {
      harness.run().emit({ type: 'assistant.completed', runId: 'run-1', messageId: 'msg-1' })
    })
    act(() => {
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: ' и хвост' })
    })

    const rendered = Array.from(document.querySelectorAll('.message__content')).map((node) => node.textContent)
    expect(rendered).toEqual(['Реплика 0', 'Реплика 1', 'Продолжай', 'Начало и хвост'])
    expect(screen.getAllByText('Начало и хвост')).toHaveLength(1)
  })

  it('keeps the answer in reading order between the question and the next turn', async () => {
    const { harness } = await startRun(4)

    act(() => {
      harness.run().emit({ type: 'assistant.completed', runId: 'run-1', messageId: 'msg-1' })
    })

    const rendered = Array.from(document.querySelectorAll('.message__content')).map((node) => node.textContent)
    expect(rendered).toEqual(['Реплика 0', 'Реплика 1', 'Реплика 2', 'Реплика 3', 'Продолжай', 'Начало'])
  })
})

describe('autoscroll respects manual scrolling (M-39)', () => {
  const originalRaf = globalThis.requestAnimationFrame

  beforeEach(() => {
    // Deterministic frames: the coalescing is what is under test, not timing.
    globalThis.requestAnimationFrame = ((callback: FrameRequestCallback) => {
      callback(0)
      return 1
    }) as typeof globalThis.requestAnimationFrame
  })

  afterEach(() => {
    globalThis.requestAnimationFrame = originalRaf
  })

  function scrollAwayFromBottom(container: Element) {
    Object.defineProperty(container, 'scrollHeight', { configurable: true, value: 4000 })
    Object.defineProperty(container, 'clientHeight', { configurable: true, value: 600 })
    Object.defineProperty(container, 'scrollTop', { configurable: true, value: 200, writable: true })
    fireEvent.scroll(container)
  }

  function scrollBackToBottom(container: Element) {
    Object.defineProperty(container, 'scrollTop', { configurable: true, value: 3400, writable: true })
    fireEvent.scroll(container)
  }

  it('stops following the answer once the user scrolls up, and resumes at the bottom', async () => {
    const { harness } = await startRun(12)
    const container = document.querySelector('.messages')
    expect(container).not.toBeNull()

    const scrollIntoView = Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>
    scrollIntoView.mockClear()

    // Following: a delta pulls the view down.
    act(() => {
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: ' раз' })
    })
    expect(scrollIntoView).toHaveBeenCalledTimes(1)

    act(() => { scrollAwayFromBottom(container!) })
    scrollIntoView.mockClear()

    act(() => {
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: ' два' })
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: ' три' })
    })
    // Reading the history mid-run must be possible.
    expect(scrollIntoView).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: /К последним сообщениям/ })).toBeInTheDocument()

    act(() => { scrollBackToBottom(container!) })
    expect(screen.queryByRole('button', { name: /К последним сообщениям/ })).not.toBeInTheDocument()

    act(() => {
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: ' четыре' })
    })
    expect(scrollIntoView).toHaveBeenCalledTimes(1)
  })

  it('coalesces a burst of deltas into a single scroll', async () => {
    const { harness } = await startRun(6)
    const scrollIntoView = Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>
    scrollIntoView.mockClear()

    act(() => {
      for (let index = 0; index < 12; index += 1) {
        harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: `${index}` })
      }
    })

    // React batches the state updates inside one act(); the scroll must not be
    // restarted per delta the way the old smooth scroll was.
    expect(scrollIntoView).toHaveBeenCalledTimes(1)
    expect(scrollIntoView.mock.calls[0]?.[0]).toMatchObject({ behavior: 'auto' })
  })

  it('jumps back to the bottom on demand', async () => {
    const { harness, user } = await startRun(12)
    const container = document.querySelector('.messages')
    act(() => { scrollAwayFromBottom(container!) })

    const scrollIntoView = Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>
    scrollIntoView.mockClear()

    await user.click(screen.getByRole('button', { name: /К последним сообщениям/ }))
    expect(scrollIntoView).toHaveBeenCalledWith(expect.objectContaining({ behavior: 'smooth' }))

    scrollIntoView.mockClear()
    act(() => {
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: ' снова' })
    })
    expect(scrollIntoView).toHaveBeenCalledTimes(1)
  })
})
