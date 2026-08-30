// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { BackendConnection } from '../lib/backend'
import type { ChatEvent, ChatMessage, ChatRequest, Conversation, RunResult, YuriClient } from '../lib/contracts'
import { ChatView } from './ChatView'

/**
 * M-44. The chat feed used to be one big `aria-live="polite" role="log"`
 * container wrapped around the streaming answer, so every token mutation was a
 * fresh announcement. These tests pin the announcement contract itself — what a
 * screen reader is asked to say and how many times — rather than render counts.
 */

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
    title: 'Диалог',
    preview: '',
    updatedAt: '2026-08-29T10:00:00.000Z',
    messages: Array.from({ length: messageCount }, (_, index) => historyMessage(index)),
    traces: [],
  }
}

function createHarness(messageCount: number) {
  let handle: { emit: EventSink; settle: (result: RunResult) => void } | undefined
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

/** The feed container the review found wrapped in a live region. */
function feed(): HTMLElement {
  const node = document.querySelector('.messages')
  if (!node) throw new Error('the message feed is not rendered')
  return node as HTMLElement
}

/**
 * Everything a polite screen reader would be asked to speak, excluding the
 * run-state indicator, which is asserted separately.
 */
function politeRegions(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[aria-live="polite"], [role="log"], [role="status"], [role="alert"]'))
}

function announcedText(): string {
  return politeRegions()
    .filter((node) => !node.classList.contains('run-state'))
    .map((node) => node.textContent?.trim() ?? '')
    .filter(Boolean)
    .join(' | ')
}

function runState(): HTMLElement {
  return screen.getByRole('status')
}

async function openChat(messageCount: number) {
  const user = userEvent.setup()
  const harness = createHarness(messageCount)
  render(<ChatView agentName="Yuri" backend={backend} onOpenSettings={vi.fn()} />)
  await screen.findByRole('textbox', { name: 'Сообщение Yuri' })
  return { harness, user }
}

async function startRun(messageCount: number) {
  const { harness, user } = await openChat(messageCount)
  const composer = screen.getByRole('textbox', { name: 'Сообщение Yuri' })
  await user.type(composer, 'Продолжай')
  await user.click(screen.getByRole('button', { name: 'Отправить сообщение' }))
  await waitFor(() => expect(harness.sendMessage).toHaveBeenCalledTimes(1))
  act(() => {
    harness.run().emit({ type: 'run.started', runId: 'run-1' })
  })
  return { harness, user }
}

const originalRaf = globalThis.requestAnimationFrame

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  // The autoscroll is coalesced into an animation frame that jsdom never runs.
  // Mirrors ChatView.streaming.test.tsx so the committed render path under test
  // is the same one the streaming suite exercises.
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

describe('the chat feed is not a live region (M-44)', () => {
  it('does not ask a screen reader to speak the transcript', async () => {
    await openChat(6)

    const container = feed()
    // `role="log"` carries an *implicit* `aria-live="polite"`, so dropping the
    // attribute alone would leave the container announcing. Both must go.
    expect(container.getAttribute('role')).not.toBe('log')
    const live = container.getAttribute('aria-live')
    expect(live === null || live === 'off').toBe(true)
    expect(screen.queryByRole('log')).not.toBeInTheDocument()

    // No ancestor may reinstate it either.
    for (let node = container.parentElement; node; node = node.parentElement) {
      expect(node.getAttribute('role')).not.toBe('log')
      const inherited = node.getAttribute('aria-live')
      expect(inherited === null || inherited === 'off').toBe(true)
    }
  })

  it('says nothing when a conversation with history is opened', async () => {
    await openChat(8)

    // The last answer already on screen is not news; reading it on load was the
    // other half of the noise.
    expect(announcedText()).toBe('')
    expect(screen.getByText('Реплика 7')).toBeInTheDocument()
  })
})

describe('streaming deltas are silent, the finished answer is spoken once (M-44)', () => {
  it('announces nothing while the answer is still arriving', async () => {
    const { harness } = await startRun(4)

    act(() => {
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: 'Начало' })
    })
    expect(announcedText()).toBe('')

    act(() => {
      for (const delta of [' первое', ' второе', ' третье']) {
        harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta })
      }
    })
    // The partial text is on screen but was never handed to a live region.
    expect(screen.getByText('Начало первое второе третье')).toBeInTheDocument()
    expect(announcedText()).toBe('')
  })

  it('announces the completed answer exactly once, in full', async () => {
    const { harness } = await startRun(4)

    act(() => {
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: 'Готовый' })
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: ' ответ' })
    })
    expect(announcedText()).toBe('')

    act(() => {
      harness.run().emit({ type: 'assistant.completed', runId: 'run-1', messageId: 'msg-1' })
    })

    const regions = politeRegions().filter((node) => (node.textContent ?? '').includes('Готовый ответ'))
    // Exactly one live region carries it — not the bubble, and not twice.
    expect(regions).toHaveLength(1)
    expect(regions[0]).not.toBe(feed())
    expect(feed().contains(regions[0])).toBe(false)

    const spoken = announcedText()
    // Named speaker: outside the transcript the utterance has to identify who
    // answered. It also keeps the announcement a distinct text node from the
    // bubble, which is what `getAllByText` based "the buffer moved, it did not
    // fork" assertions elsewhere in the suite depend on.
    expect(spoken).toContain('Yuri: Готовый ответ')
    expect(screen.getAllByText('Готовый ответ')).toHaveLength(1)

    // Terminating the run must not repeat the same answer.
    act(() => {
      harness.run().emit({ type: 'run.completed', runId: 'run-1', status: 'complete' })
    })
    expect(announcedText()).toBe(spoken)
  })

  it('replaces the previous announcement instead of stacking answers', async () => {
    const { harness } = await startRun(2)

    act(() => {
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: 'Первый ответ' })
      harness.run().emit({ type: 'assistant.completed', runId: 'run-1', messageId: 'msg-1' })
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-2', delta: 'Второй ответ' })
      harness.run().emit({ type: 'assistant.completed', runId: 'run-1', messageId: 'msg-2' })
    })

    const spoken = announcedText()
    expect(spoken).toContain('Второй ответ')
    expect(spoken).not.toContain('Первый ответ')
  })
})

describe('run status still reaches the reader (M-44)', () => {
  it('announces waiting_approval and the terminal state through a status region', async () => {
    const { harness } = await startRun(4)

    expect(runState()).toHaveTextContent('Yuri думает…')

    act(() => {
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: 'ищу файл' })
      harness.run().emit({ type: 'run.status', runId: 'run-1', status: 'waiting_approval', label: '' })
    })

    // This is the change the review said drowned in the token stream. The
    // status region is a sibling of the feed, so a delta cannot displace it.
    expect(runState()).toHaveTextContent('Ожидается ваше разрешение')
    expect(feed().contains(runState())).toBe(false)

    act(() => {
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: ' ещё' })
    })
    expect(runState()).toHaveTextContent('Ожидается ваше разрешение')

    act(() => {
      harness.run().emit({ type: 'run.completed', runId: 'run-1', status: 'cancelled' })
    })
    expect(runState()).toHaveTextContent('Запуск остановлен')
    // A cancelled partial answer is reported as a state, not read out as if it
    // were a finished response.
    expect(announcedText()).toBe('')
  })

  it('hands the reader to the approval dialog, which takes the tree over', async () => {
    const { harness } = await startRun(4)

    act(() => {
      harness.run().emit({
        type: 'approval.required',
        runId: 'run-1',
        approval: {
          id: 'approval-1',
          toolCallId: 'call-1',
          title: 'Записать файл',
          explanation: 'Yuri хочет создать notes.md.',
          risk: 'high',
          scope: 'filesystem.write ~/Documents/notes.md',
        },
      })
    })

    // `ModalShell` marks the rest of the app `inert`/`aria-hidden`, so the
    // dialog is the announcement here — the transcript behind it is silent by
    // construction, which is only safe because it is no longer a live region.
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveAccessibleName(/Записать файл/)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(feed().closest('[aria-hidden="true"]')).not.toBeNull()
  })
})
