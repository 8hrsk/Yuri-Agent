// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { BackendConnection } from '../lib/backend'
import type { ApprovalRequest, ChatEvent, ChatRequest, Conversation, RunResult, YuriClient } from '../lib/contracts'
import { ChatView } from './ChatView'

type EventSink = (event: ChatEvent) => void

let clientStub: YuriClient

vi.mock('../lib/client', () => ({
  createYuriClient: () => clientStub,
}))

const backend: BackendConnection = { status: 'connected', label: 'Backend connected', detail: 'Wails runtime is ready' }

const approval: ApprovalRequest = {
  id: 'approval-1',
  toolCallId: 'call-1',
  title: 'Записать файл в Documents',
  explanation: 'Yuri хочет создать файл notes.md.',
  risk: 'high',
  scope: 'filesystem.write ~/Documents/notes.md',
}

function conversationFixture(): Conversation {
  return {
    id: 'conv-1',
    title: 'Новый диалог',
    preview: '',
    updatedAt: '2026-08-29T10:00:00.000Z',
    messages: [],
    traces: [],
  }
}

/** Captured hooks into the run that the fake bridge is currently serving. */
type RunHandle = {
  emit: EventSink
  settle: (result: RunResult) => void
  reject: (cause: unknown) => void
}

function createHarness(overrides: Partial<YuriClient> = {}) {
  let handle: RunHandle | undefined
  const sendMessage = vi.fn((_request: ChatRequest, onEvent: EventSink) => new Promise<RunResult>((resolve, reject) => {
    handle = { emit: onEvent, settle: resolve, reject }
  }))
  const stub = {
    mode: 'mock',
    listConversations: async () => [conversationFixture()],
    // The conversation list carries metadata only, so opening a conversation
    // fetches its transcript. These fixtures ship their own, so the fetch is
    // never reached; the stub is here because the client interface has it.
    listMessages: async (conversationId: string) => ({ conversationId, messages: [], traces: [], hasMore: false }),
    createConversation: async () => conversationFixture(),
    listChatTools: async () => [],
    getAllowedDirectories: async () => [],
    transcribeAudio: async () => '',
    sendMessage,
    retryLast: sendMessage,
    cancelRun: vi.fn(async () => {}),
    approve: vi.fn(async () => {}),
    ...overrides,
  } as unknown as YuriClient
  clientStub = stub
  return {
    client: stub,
    sendMessage,
    run: () => {
      if (!handle) throw new Error('the run has not started yet')
      return handle
    },
  }
}

async function startRun(user: ReturnType<typeof userEvent.setup>, sendMessage: ReturnType<typeof vi.fn>) {
  render(<ChatView agentName="Yuri" backend={backend} onOpenSettings={vi.fn()} />)
  const composer = await screen.findByRole('textbox', { name: 'Сообщение Yuri' })
  await user.type(composer, 'Создай заметку')
  await user.click(screen.getByRole('button', { name: 'Отправить сообщение' }))
  await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
}

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  Object.defineProperty(window, 'speechSynthesis', {
    configurable: true,
    value: { cancel: vi.fn(), speak: vi.fn() },
  })
})

describe('ChatView approval flow', () => {
  it('reports a rejected approval inside the dialog instead of hanging (H-11/M-49)', async () => {
    const user = userEvent.setup()
    const approve = vi.fn(async () => { throw new Error('Мост недоступен') })
    const harness = createHarness({ approve })
    await startRun(user, harness.sendMessage)

    act(() => {
      harness.run().emit({ type: 'run.started', runId: 'run-1' })
      harness.run().emit({ type: 'approval.required', runId: 'run-1', approval })
    })

    const dialog = await screen.findByRole('dialog')
    await user.click(screen.getByRole('button', { name: 'Разрешить действие' }))

    await waitFor(() => expect(approve).toHaveBeenCalledWith('approval-1', 'approve'))
    // The dialog stays up, now carrying the failure, and both decisions are live
    // again instead of being frozen on "Сохраняю решение…".
    expect(dialog).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent('Мост недоступен')
    expect(screen.getByRole('button', { name: 'Разрешить действие' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Отклонить' })).toBeEnabled()

    // And it can still be dismissed, which is what the unhandled rejection used
    // to make impossible.
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.getByText(/Решение по подтверждению не было передано агенту/)).toBeInTheDocument()
  })

  it('closes the dialog once a decision reaches the runtime', async () => {
    const user = userEvent.setup()
    const harness = createHarness()
    await startRun(user, harness.sendMessage)

    act(() => {
      harness.run().emit({ type: 'run.started', runId: 'run-1' })
      harness.run().emit({ type: 'approval.required', runId: 'run-1', approval })
    })

    await screen.findByRole('dialog')
    await user.keyboard('{Escape}')

    await waitFor(() => expect(harness.client.approve).toHaveBeenCalledWith('approval-1', 'deny'))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})

describe('ChatView run finalization', () => {
  it('finalizes the partial answer when a run is cancelled (H-8)', async () => {
    const user = userEvent.setup()
    const harness = createHarness()
    await startRun(user, harness.sendMessage)

    act(() => {
      harness.run().emit({ type: 'run.started', runId: 'run-1' })
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: 'Начинаю' })
    })

    expect(screen.getByText('Печатает')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Остановить запуск' }))
    await waitFor(() => expect(harness.client.cancelRun).toHaveBeenCalledWith('run-1'))

    // The backend short-circuits `assistant.completed` on cancellation, so the
    // terminal run event is all the frontend gets.
    act(() => {
      harness.run().emit({ type: 'run.completed', runId: 'run-1', status: 'cancelled' })
    })

    expect(screen.queryByText('Печатает')).not.toBeInTheDocument()
    expect(screen.getByText('Остановлено', { selector: '.message__status' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Повторить ответ' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Озвучить ответ' })).toBeInTheDocument()
  })

  it('finalizes the partial answer even when no terminal event ever arrives', async () => {
    const user = userEvent.setup()
    const harness = createHarness()
    await startRun(user, harness.sendMessage)

    act(() => {
      harness.run().emit({ type: 'run.started', runId: 'run-1' })
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: 'Начинаю' })
    })
    expect(screen.getByText('Печатает')).toBeInTheDocument()

    // The bridge promise settling is the only signal here: no `run.completed`,
    // no `assistant.completed`.
    await act(async () => {
      harness.run().settle({ runId: 'run-1', status: 'cancelled' })
    })

    await waitFor(() => expect(screen.queryByText('Печатает')).not.toBeInTheDocument())
    expect(screen.getByText('Остановлено', { selector: '.message__status' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Повторить ответ' })).toBeInTheDocument()
    // The composer is usable again rather than staying locked on a dead run.
    expect(screen.getByRole('textbox', { name: 'Сообщение Yuri' })).toBeEnabled()
  })

  it('finalizes the partial answer and drops the approval when the bridge fails', async () => {
    const user = userEvent.setup()
    const harness = createHarness()
    await startRun(user, harness.sendMessage)

    act(() => {
      harness.run().emit({ type: 'run.started', runId: 'run-1' })
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: 'Начинаю' })
      harness.run().emit({ type: 'approval.required', runId: 'run-1', approval })
    })
    await screen.findByRole('dialog')

    await act(async () => {
      harness.run().reject(new Error('Мост оборвался'))
    })

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.queryByText('Печатает')).not.toBeInTheDocument()
    expect(screen.getByText('Ошибка', { selector: '.message__status' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Повторить ответ' })).toBeInTheDocument()
  })

  it('restores the run label when cancellation itself fails', async () => {
    const user = userEvent.setup()
    const cancelRun = vi.fn(async () => { throw new Error('Не удалось достучаться до рантайма') })
    const harness = createHarness({ cancelRun })
    await startRun(user, harness.sendMessage)

    act(() => {
      harness.run().emit({ type: 'run.started', runId: 'run-1' })
    })

    await user.click(screen.getByRole('button', { name: 'Остановить запуск' }))

    await waitFor(() => expect(cancelRun).toHaveBeenCalledWith('run-1'))
    // The composer must not stay stuck on "Останавливаем запуск…" forever.
    await waitFor(() => expect(screen.queryByText('Останавливаем запуск…')).not.toBeInTheDocument())
    expect(screen.getByRole('status')).toHaveTextContent('Yuri думает…')
    expect(screen.getByText('Не удалось достучаться до рантайма')).toBeInTheDocument()
  })
})
