// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { BackendConnection } from '../lib/backend'
import type { ChatEvent, ChatMessage, ChatRequest, Conversation, RunResult, YuriClient } from '../lib/contracts'
import { ChatView } from './ChatView'

/**
 * M-42. The recording timer ticks ten times a second. It used to live in a hook
 * called at the top of `ChatView`, so every tick re-ran the whole chat surface
 * for a value only the composer's `<span>` reads.
 *
 * Two counters, because they answer different questions:
 *
 * - `mapAvatarState` runs exactly once per `ChatView` render, so it counts the
 *   root re-renders the timer causes. This is the counter M-42 is about.
 * - `formatClock` runs once per rendered bubble and per sidebar row. Since the
 *   C-1 work `ChatTimeline` is memoized behind stable props, so this counter
 *   stays flat *whether or not* the timer sits at the root — it is asserted to
 *   document that the blast radius today is the root body, not the transcript,
 *   not as the M-42 guard.
 */
const { avatarCalls, clockCalls } = vi.hoisted(() => ({ avatarCalls: [] as string[], clockCalls: [] as string[] }))

vi.mock('../lib/personality', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/personality')>()
  return {
    ...actual,
    mapAvatarState: (...args: Parameters<typeof actual.mapAvatarState>) => {
      avatarCalls.push(String(args[0]))
      return actual.mapAvatarState(...args)
    },
  }
})

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

/** The recorder timer callback, captured instead of scheduled. */
let recorderTick: (() => void) | undefined
let fakeNow = 0

const originalSetInterval = window.setInterval
const originalRaf = globalThis.requestAnimationFrame
const originalPerformanceNow = performance.now

class FakeMediaRecorder {
  static instances: FakeMediaRecorder[] = []
  state: 'inactive' | 'recording' = 'inactive'
  mimeType = 'audio/webm'
  onstop: (() => void) | null = null
  ondataavailable: ((event: { data: Blob }) => void) | null = null
  constructor(public stream: MediaStream) {
    FakeMediaRecorder.instances.push(this)
  }
  start() { this.state = 'recording' }
  stop() { this.state = 'inactive'; this.onstop?.() }
}

beforeEach(() => {
  avatarCalls.length = 0
  clockCalls.length = 0
  recorderTick = undefined
  fakeNow = 0
  FakeMediaRecorder.instances.length = 0

  Element.prototype.scrollIntoView = vi.fn()
  globalThis.requestAnimationFrame = ((callback: FrameRequestCallback) => {
    callback(0)
    return 1
  }) as typeof globalThis.requestAnimationFrame

  // A controllable clock so the rendered duration is deterministic.
  performance.now = () => fakeNow

  // Capture the 100 ms recorder interval rather than letting it run, so each
  // tick is an explicit, countable act() in the test.
  window.setInterval = ((handler: TimerHandler, timeout?: number, ...rest: unknown[]) => {
    if (timeout === 100 && typeof handler === 'function') {
      recorderTick = handler as () => void
      return 987654 as unknown as number
    }
    return (originalSetInterval as (...args: unknown[]) => number)(handler, timeout, ...rest)
  }) as typeof window.setInterval

  Object.defineProperty(window, 'speechSynthesis', {
    configurable: true,
    value: { cancel: vi.fn(), speak: vi.fn() },
  })
  Object.defineProperty(globalThis, 'MediaRecorder', { configurable: true, writable: true, value: FakeMediaRecorder })
  Object.defineProperty(navigator, 'mediaDevices', {
    configurable: true,
    value: {
      getUserMedia: vi.fn(async () => ({ getTracks: () => [{ stop: vi.fn() }] }) as unknown as MediaStream),
    },
  })

  const sendMessage = vi.fn((_request: ChatRequest, _onEvent: (event: ChatEvent) => void) => new Promise<RunResult>(() => {}))
  clientStub = {
    mode: 'mock',
    listConversations: async () => [conversationFixture(20)],
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
})

afterEach(() => {
  window.setInterval = originalSetInterval
  globalThis.requestAnimationFrame = originalRaf
  performance.now = originalPerformanceNow
})

async function startRecording() {
  render(<ChatView agentName="Yuri" backend={backend} onOpenSettings={vi.fn()} />)
  await screen.findByRole('textbox', { name: 'Сообщение Yuri' })
  await waitFor(() => expect(screen.getByText('Реплика 19')).toBeInTheDocument())

  await act(async () => {
    fireEvent.click(screen.getByRole('button', { name: 'Записать голосовое сообщение' }))
  })
  await waitFor(() => expect(screen.getByRole('button', { name: 'Остановить запись' })).toBeInTheDocument())
  // The hook must actually have opened a timer, or everything below is vacuous.
  expect(recorderTick).toBeTypeOf('function')
}

/** Advance the recorder clock and run one 100 ms tick. */
function tick(byMs: number) {
  fakeNow += byMs
  act(() => { recorderTick?.() })
}

describe('the recording timer does not re-render the chat (M-42)', () => {
  it('ticks the visible duration without re-running the chat surface', async () => {
    await startRecording()

    expect(screen.getByText(/00:00/)).toBeInTheDocument()

    avatarCalls.length = 0
    clockCalls.length = 0

    // Ten ticks — one second of dictation.
    for (let index = 0; index < 10; index += 1) tick(100)

    // The tick is real and reaches the composer: this is what makes the two
    // "stayed at zero" assertions below non-vacuous.
    expect(screen.getByText(/00:01/)).toBeInTheDocument()

    // ...and none of it re-ran ChatView, nor anything in the transcript.
    // Reported together so a failure shows the true blast radius: with the
    // hook back at the root `rootRenders` becomes 10 while `transcriptRenders`
    // stays 0, because `ChatTimeline` is memoized behind stable props.
    expect({ rootRenders: avatarCalls.length, transcriptRenders: clockCalls.length })
      .toEqual({ rootRenders: 0, transcriptRenders: 0 })

    for (let index = 0; index < 20; index += 1) tick(100)
    expect(screen.getByText(/00:03/)).toBeInTheDocument()
    expect({ rootRenders: avatarCalls.length, transcriptRenders: clockCalls.length })
      .toEqual({ rootRenders: 0, transcriptRenders: 0 })
  })

  it('still tells the chat when recording starts and stops', async () => {
    await startRecording()

    // The root does learn about the state change itself — once, not per tick.
    expect(avatarCalls.length).toBeGreaterThan(0)
    avatarCalls.length = 0

    for (let index = 0; index < 5; index += 1) tick(100)
    expect(avatarCalls).toHaveLength(0)

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Остановить запись' }))
    })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Записать голосовое сообщение' })).toBeInTheDocument())
    // Stopping is a state change the root must see, so the avatar can settle.
    expect(avatarCalls.length).toBeGreaterThan(0)
  })
})
