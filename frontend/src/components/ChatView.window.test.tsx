// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { BackendConnection } from '../lib/backend'
import type { ChatEvent, ChatHistoryPage, ChatMessage, ChatRequest, Conversation, RunResult, YuriClient } from '../lib/contracts'
import { ChatView } from './ChatView'

/**
 * H-18: the transcript had no bound of any kind. Every message of a
 * conversation kept for weeks was mounted the moment the tab opened, and every
 * one of those nodes then took part in reconciliation on each streamed token.
 *
 * These tests assert what the user sees — which part of the history is on
 * screen, what "показать более ранние" uncovers, and where the viewport ends up
 * afterwards — rather than counting renders.
 */

type EventSink = (event: ChatEvent) => void

let clientStub: YuriClient

vi.mock('../lib/client', () => ({
  createYuriClient: () => clientStub,
}))

const backend: BackendConnection = { status: 'connected', label: 'Backend connected', detail: 'Wails runtime is ready' }

/** Matches `timelineWindowSize` / `timelineWindowStep` in ChatView. */
const windowSize = 40

function historyMessage(index: number): ChatMessage {
  const minute = String(Math.floor(index / 60)).padStart(2, '0')
  const second = String(index % 60).padStart(2, '0')
  return {
    id: `history-${index}`,
    role: index % 2 === 0 ? 'user' : 'assistant',
    content: `Реплика ${index}`,
    status: 'complete',
    createdAt: `2026-08-29T09:${minute}:${second}.000Z`,
  }
}

function conversationFixture(id: string, title: string, messageCount: number): Conversation {
  return {
    id,
    title,
    preview: '',
    updatedAt: '2026-08-29T10:00:00.000Z',
    messages: Array.from({ length: messageCount }, (_, index) => historyMessage(index)),
    traces: [],
  }
}

type RunHandle = { emit: EventSink; settle: (result: RunResult) => void }

type ListMessages = (conversationId: string, limit: number, before?: string) => Promise<ChatHistoryPage>

const noHistory: ListMessages = async (conversationId) => ({ conversationId, messages: [], traces: [], hasMore: false })

function createHarness(conversations: Conversation[], listMessagesImpl: ListMessages = noHistory) {
  let handle: RunHandle | undefined
  const sendMessage = vi.fn((_request: ChatRequest, onEvent: EventSink) => new Promise<RunResult>((resolve) => {
    handle = { emit: onEvent, settle: resolve }
  }))
  const listMessages = vi.fn(listMessagesImpl)
  clientStub = {
    mode: 'mock',
    listConversations: async () => conversations,
    listMessages,
    createConversation: async () => conversationFixture('conv-new', 'Новый диалог', 0),
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
    listMessages,
    run: () => {
      if (!handle) throw new Error('the run has not started yet')
      return handle
    },
  }
}

/** Text of every message bubble currently mounted, top to bottom. */
function renderedMessages(): string[] {
  return Array.from(document.querySelectorAll('.messages .message__content')).map((node) => node.textContent ?? '')
}

async function openChat(conversations: Conversation[], listMessagesImpl?: ListMessages) {
  const harness = createHarness(conversations, listMessagesImpl)
  const user = userEvent.setup()
  render(<ChatView agentName="Yuri" backend={backend} onOpenSettings={vi.fn()} />)
  await screen.findByRole('textbox', { name: 'Сообщение Yuri' })
  await waitFor(() => expect(renderedMessages().length).toBeGreaterThan(0))
  return { harness, user }
}

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  Object.defineProperty(window, 'speechSynthesis', {
    configurable: true,
    value: { cancel: vi.fn(), speak: vi.fn() },
  })
})

describe('a long conversation opens windowed (H-18)', () => {
  it('mounts only the tail of the history', async () => {
    await openChat([conversationFixture('conv-1', 'Длинный диалог', 200)])

    const rendered = renderedMessages()
    expect(rendered).toHaveLength(windowSize)
    expect(rendered[0]).toBe('Реплика 160')
    expect(rendered.at(-1)).toBe('Реплика 199')
    // The 160 older bubbles are genuinely absent, not merely hidden by CSS.
    expect(screen.queryByText('Реплика 0')).not.toBeInTheDocument()
    expect(screen.queryByText('Реплика 159')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Показать более ранние \(160\)/ })).toBeInTheDocument()
  })

  it('shows the whole of a conversation that fits in one window, with no reveal control', async () => {
    await openChat([conversationFixture('conv-1', 'Короткий диалог', 12)])

    expect(renderedMessages()).toHaveLength(12)
    expect(screen.getByText('Реплика 0')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Показать более ранние/ })).not.toBeInTheDocument()
  })

  it('uncovers the previous window on demand, one step at a time', async () => {
    const { user } = await openChat([conversationFixture('conv-1', 'Длинный диалог', 200)])

    await user.click(screen.getByRole('button', { name: /Показать более ранние/ }))

    const afterFirst = renderedMessages()
    expect(afterFirst).toHaveLength(windowSize * 2)
    expect(afterFirst[0]).toBe('Реплика 120')
    // The step is exactly one window: 119 is still behind the button.
    expect(screen.queryByText('Реплика 119')).not.toBeInTheDocument()
    // …and nothing that was already on screen was dropped to make room.
    expect(screen.getByText('Реплика 160')).toBeInTheDocument()
    expect(screen.getByText('Реплика 199')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Показать более ранние \(120\)/ })).toBeInTheDocument()

    for (let step = 0; step < 3; step += 1) {
      await user.click(screen.getByRole('button', { name: /Показать более ранние/ }))
    }

    expect(renderedMessages()).toHaveLength(200)
    expect(screen.getByText('Реплика 0')).toBeInTheDocument()
    // Fully uncovered, the control retires instead of sitting there doing
    // nothing.
    expect(screen.queryByRole('button', { name: /Показать более ранние/ })).not.toBeInTheDocument()
  })

  it('re-windows when another conversation is opened', async () => {
    const { user } = await openChat([
      conversationFixture('conv-1', 'Длинный диалог', 200),
      conversationFixture('conv-2', 'Второй диалог', 90),
    ])

    await user.click(screen.getByRole('button', { name: /Показать более ранние/ }))
    expect(renderedMessages()).toHaveLength(windowSize * 2)

    await user.click(screen.getByText('Второй диалог', { selector: '.conversation-item__copy strong' }))
    // The second conversation opens at its own tail, not at the width the
    // previous one happened to be expanded to.
    await waitFor(() => expect(renderedMessages()).toHaveLength(windowSize))
    expect(screen.getByRole('button', { name: /Показать более ранние \(50\)/ })).toBeInTheDocument()

    await user.click(screen.getByText('Длинный диалог', { selector: '.conversation-item__copy strong' }))
    await waitFor(() => expect(renderedMessages()).toHaveLength(windowSize))
    expect(screen.getByRole('button', { name: /Показать более ранние \(160\)/ })).toBeInTheDocument()
  })
})

describe('revealing history keeps the viewport where the reader left it (H-18)', () => {
  const originalRaf = globalThis.requestAnimationFrame

  beforeEach(() => {
    // The autoscroll is coalesced into an animation frame, so without a
    // synchronous scheduler "no scroll happened" would pass for the wrong
    // reason: the frame simply had not run yet by the time of the assertion.
    globalThis.requestAnimationFrame = ((callback: FrameRequestCallback) => {
      callback(0)
      return 1
    }) as typeof globalThis.requestAnimationFrame
  })

  afterEach(() => {
    globalThis.requestAnimationFrame = originalRaf
  })

  /**
   * jsdom has no layout, so the scroll container is given a height that tracks
   * the bubbles actually mounted. That is the only property the anchoring
   * depends on: uncovering older messages grows the content upwards.
   */
  function stubScrollMetrics(container: Element, scrollTop: number) {
    Object.defineProperty(container, 'scrollHeight', {
      configurable: true,
      get: () => document.querySelectorAll('.messages .message').length * 100,
    })
    Object.defineProperty(container, 'clientHeight', { configurable: true, value: 600 })
    Object.defineProperty(container, 'scrollTop', { configurable: true, value: scrollTop, writable: true })
  }

  it('pins the entry under the reader instead of jumping to either end', async () => {
    const { user } = await openChat([conversationFixture('conv-1', 'Длинный диалог', 200)])
    const container = document.querySelector('.messages')
    expect(container).not.toBeNull()
    stubScrollMetrics(container!, 250)

    const scrollIntoView = Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>
    scrollIntoView.mockClear()

    // 40 bubbles → 4000px of content before the reveal, 80 → 8000px after it.
    await user.click(screen.getByRole('button', { name: /Показать более ранние/ }))

    // The reader stays on the same entry: the 4000px that appeared above them
    // is added to their offset.
    expect(container!.scrollTop).toBe(4250)
    // And revealing history is not new output, so it must not drag the view
    // down to the newest message.
    expect(scrollIntoView).not.toHaveBeenCalled()
  })
})

describe('streaming does not widen the window (H-18)', () => {
  it('keeps the trimmed history out while an answer streams in', async () => {
    const { harness, user } = await openChat([conversationFixture('conv-1', 'Длинный диалог', 200)])

    const composer = screen.getByRole('textbox', { name: 'Сообщение Yuri' })
    await user.type(composer, 'Продолжай')
    await user.click(screen.getByRole('button', { name: 'Отправить сообщение' }))
    await waitFor(() => expect(harness.sendMessage).toHaveBeenCalledTimes(1))

    act(() => {
      harness.run().emit({ type: 'run.started', runId: 'run-1' })
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: 'Начало' })
    })
    act(() => {
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: ' ответа' })
    })

    const rendered = renderedMessages()
    // The window still starts where the reader opened it: the two new entries
    // widened it at the bottom rather than sliding it and dropping the oldest
    // visible bubble.
    expect(rendered[0]).toBe('Реплика 160')
    expect(rendered).toHaveLength(windowSize + 2)
    expect(rendered.at(-1)).toBe('Начало ответа')
    // A token must never drag the trimmed history back into the DOM.
    expect(screen.queryByText('Реплика 0')).not.toBeInTheDocument()
    expect(screen.queryByText('Реплика 159')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Показать более ранние \(160\)/ })).toBeInTheDocument()

    act(() => {
      harness.run().emit({ type: 'assistant.completed', runId: 'run-1', messageId: 'msg-1' })
      harness.run().emit({ type: 'run.completed', runId: 'run-1', status: 'complete' })
    })

    // Committing the answer keeps the same window rather than re-expanding the
    // conversation.
    expect(renderedMessages()).toHaveLength(windowSize + 2)
    expect(screen.queryByText('Реплика 0')).not.toBeInTheDocument()
  })
})

/**
 * M-35: `ListConversations` returns only the newest slice of a transcript, so
 * once the reader has uncovered every locally held entry the same control has
 * to fetch the previous page. These tests cover the seam between the two
 * sources — the cursor, the anchoring, and what must not be duplicated or lost
 * across it.
 */
describe('“показать более ранние” pages the backend once local history runs out (M-35)', () => {
  const originalRaf = globalThis.requestAnimationFrame

  beforeEach(() => {
    // Without a synchronous scheduler "no scroll happened" passes for the wrong
    // reason: the coalesced autoscroll frame simply never runs in the test.
    globalThis.requestAnimationFrame = ((callback: FrameRequestCallback) => {
      callback(0)
      return 1
    }) as typeof globalThis.requestAnimationFrame
  })

  afterEach(() => {
    globalThis.requestAnimationFrame = originalRaf
  })

  /** A message older than every `historyMessage`, so ordering is unambiguous. */
  function olderMessage(index: number): ChatMessage {
    const minute = String(Math.floor(index / 60)).padStart(2, '0')
    const second = String(index % 60).padStart(2, '0')
    return {
      id: `older-${index}`,
      role: index % 2 === 0 ? 'user' : 'assistant',
      content: `Ранее ${index}`,
      status: 'complete',
      createdAt: `2026-08-29T08:${minute}:${second}.000Z`,
    }
  }

  /** A conversation the backend has already trimmed to its newest page. */
  function pagedFixture(messageCount: number): Conversation {
    return { ...conversationFixture('conv-1', 'Длинный диалог', messageCount), hasMoreMessages: true }
  }

  function stubScrollMetrics(container: Element, scrollTop: number) {
    Object.defineProperty(container, 'scrollHeight', {
      configurable: true,
      get: () => document.querySelectorAll('.messages .message').length * 100,
    })
    Object.defineProperty(container, 'clientHeight', { configurable: true, value: 600 })
    Object.defineProperty(container, 'scrollTop', { configurable: true, value: scrollTop, writable: true })
  }

  it('offers the control even with nothing left to uncover locally', async () => {
    // Exactly one window: nothing is hidden, so the only history left is the
    // backend's.
    await openChat([pagedFixture(windowSize)])

    expect(renderedMessages()).toHaveLength(windowSize)
    expect(screen.getByRole('button', { name: /Показать более ранние/ })).toBeInTheDocument()
  })

  it('fetches and prepends the previous page, anchored, with nothing duplicated or lost', async () => {
    const { harness, user } = await openChat(
      [pagedFixture(windowSize)],
      async (conversationId, _limit, _before) => ({
        conversationId,
        // Deliberately overlapping: the newest entry of the page repeats the
        // cursor message. A client that trusted the page blindly would render
        // "Реплика 0" twice.
        messages: [...Array.from({ length: windowSize }, (_, index) => olderMessage(index)), historyMessage(0)],
        traces: [],
        hasMore: true,
      }),
    )

    const container = document.querySelector('.messages')
    expect(container).not.toBeNull()
    stubScrollMetrics(container!, 250)
    const scrollIntoView = Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>
    scrollIntoView.mockClear()

    await user.click(screen.getByRole('button', { name: /Показать более ранние/ }))

    await waitFor(() => expect(renderedMessages()).toHaveLength(windowSize * 2))
    // The cursor is the oldest message actually held, so the backend can return
    // the page that abuts it exactly.
    expect(harness.listMessages).toHaveBeenCalledTimes(1)
    expect(harness.listMessages).toHaveBeenCalledWith('conv-1', windowSize, 'history-0')

    const rendered = renderedMessages()
    expect(rendered[0]).toBe('Ранее 0')
    expect(rendered[windowSize - 1]).toBe(`Ранее ${windowSize - 1}`)
    expect(rendered[windowSize]).toBe('Реплика 0')
    expect(rendered.at(-1)).toBe(`Реплика ${windowSize - 1}`)
    // Nothing was lost across the seam, and nothing came back twice.
    expect(new Set(rendered).size).toBe(rendered.length)

    // 40 bubbles → 4000px before the fetch, 80 → 8000px after it: the reader
    // stays on the same entry.
    expect(container!.scrollTop).toBe(4250)
    // Fetched history is not new output, so it must not drag the view down to
    // the newest message.
    expect(scrollIntoView).not.toHaveBeenCalled()
  })

  it('walks back page by page and retires the control at the start of the transcript', async () => {
    const cursors: (string | undefined)[] = []
    const { harness, user } = await openChat(
      [pagedFixture(windowSize)],
      async (conversationId, _limit, before) => {
        cursors.push(before)
        if (before === 'history-0') {
          return {
            conversationId,
            messages: Array.from({ length: windowSize }, (_, index) => olderMessage(windowSize + index)),
            traces: [],
            hasMore: true,
          }
        }
        return {
          conversationId,
          messages: Array.from({ length: windowSize }, (_, index) => olderMessage(index)),
          traces: [],
          hasMore: false,
        }
      },
    )

    await user.click(screen.getByRole('button', { name: /Показать более ранние/ }))
    await waitFor(() => expect(renderedMessages()).toHaveLength(windowSize * 2))

    await user.click(screen.getByRole('button', { name: /Показать более ранние/ }))
    await waitFor(() => expect(renderedMessages()).toHaveLength(windowSize * 3))

    // Each page is asked for from the oldest entry then held, so the pages abut.
    expect(cursors).toEqual(['history-0', `older-${windowSize}`])
    expect(harness.listMessages).toHaveBeenCalledTimes(2)

    const rendered = renderedMessages()
    expect(rendered[0]).toBe('Ранее 0')
    expect(rendered[windowSize]).toBe(`Ранее ${windowSize}`)
    expect(rendered[windowSize * 2]).toBe('Реплика 0')
    expect(new Set(rendered).size).toBe(rendered.length)

    // The backend said there is nothing older, so the control retires instead
    // of asking again forever.
    await waitFor(() => expect(screen.queryByRole('button', { name: /Показать более ранние/ })).not.toBeInTheDocument())
  })

  /**
   * N-18. The seam dedup and `hasMore` are two independent answers to "is there
   * more?", and they can disagree: a page whose every id the renderer already
   * holds contributes nothing, yet may still carry `hasMore: true`. Writing
   * that flag back left the control armed for a click that re-issued the very
   * same request — the cursor is the oldest held id, and a page that adds
   * nothing does not move it.
   *
   * The property under test is termination, not the behaviour of one click:
   * the control is clicked for as long as it is offered, under a budget.
   */
  it('stops offering the control when a page returns only messages already held (N-18)', async () => {
    const { harness, user } = await openChat(
      [pagedFixture(windowSize)],
      // Every id is one the renderer already has, so the dedup filter empties
      // the page — while the backend insists there is more behind it.
      async (conversationId) => ({
        conversationId,
        messages: Array.from({ length: windowSize }, (_, index) => historyMessage(index)),
        traces: [],
        hasMore: true,
      }),
    )

    const scrollIntoView = Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>
    scrollIntoView.mockClear()

    const clickBudget = 8
    let clicks = 0
    while (clicks < clickBudget) {
      const control = screen.queryByRole('button', { name: /Показать более ранние/ })
      if (!control) break
      await user.click(control)
      clicks += 1
      // Let the page settle before deciding whether the control came back:
      // otherwise the loop would exit on the render that is merely pending.
      await act(async () => { await Promise.resolve() })
    }

    // Before the fix this ran the budget out, one identical request per click.
    expect(clicks).toBeLessThan(clickBudget)
    expect(screen.queryByRole('button', { name: /Показать более ранние/ })).not.toBeInTheDocument()
    expect(harness.listMessages).toHaveBeenCalledTimes(1)
    expect(harness.listMessages).toHaveBeenCalledWith('conv-1', windowSize, 'history-0')
    // The transcript is untouched: nothing was prepended, nothing duplicated.
    expect(renderedMessages()).toHaveLength(windowSize)
    expect(new Set(renderedMessages()).size).toBe(windowSize)
    expect(scrollIntoView).not.toHaveBeenCalled()
  })

  it('keeps paging when a page is only partly known and its cursor still advances (N-18)', async () => {
    // The seam overlap is the normal case: the page repeats the cursor message
    // and adds real history. Retiring the control here would lose the rest of
    // the transcript, so `hasMore` is still honoured whenever anything is new.
    const { harness, user } = await openChat(
      [pagedFixture(windowSize)],
      async (conversationId, _limit, before) => ({
        conversationId,
        messages: before === 'history-0'
          ? [olderMessage(0), historyMessage(0)]
          : Array.from({ length: windowSize }, (_, index) => historyMessage(index)),
        traces: [],
        hasMore: true,
      }),
    )

    await user.click(screen.getByRole('button', { name: /Показать более ранние/ }))
    await waitFor(() => expect(renderedMessages()).toHaveLength(windowSize + 1))
    expect(screen.getByRole('button', { name: /Показать более ранние/ })).toBeInTheDocument()

    // Second page: cursor advanced to the message just prepended, and this time
    // the answer adds nothing, so the control retires rather than looping.
    await user.click(screen.getByRole('button', { name: /Показать более ранние/ }))
    await waitFor(() => expect(screen.queryByRole('button', { name: /Показать более ранние/ })).not.toBeInTheDocument())
    expect(harness.listMessages.mock.calls.map((call) => call[2])).toEqual(['history-0', 'older-0'])
  })

  it('uncovers locally held history before asking the backend for anything', async () => {
    const { harness, user } = await openChat([pagedFixture(windowSize * 2)])

    // 40 entries are hidden in front, so the first click is free.
    await user.click(screen.getByRole('button', { name: /Показать более ранние \(40\)/ }))
    expect(renderedMessages()).toHaveLength(windowSize * 2)
    expect(harness.listMessages).not.toHaveBeenCalled()

    // Only now, with nothing left in memory, does the control reach out.
    await user.click(screen.getByRole('button', { name: /Показать более ранние/ }))
    await waitFor(() => expect(harness.listMessages).toHaveBeenCalledTimes(1))
    expect(harness.listMessages).toHaveBeenCalledWith('conv-1', windowSize, 'history-0')
  })
})
