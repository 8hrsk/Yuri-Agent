// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from './App'
import { cloneAgentDraft, defaultAgentDraft } from './lib/agents'
import type {
  AgentProfile,
  ApprovalRequest,
  ChatEvent,
  ChatRequest,
  Conversation,
  RunResult,
  YuriClient,
} from './lib/contracts'

type EventSink = (event: ChatEvent) => void

let clientStub: YuriClient

vi.mock('./lib/client', () => ({
  createYuriClient: () => clientStub,
  subscribeNotifications: () => () => undefined,
  canUseNativeNotification: () => false,
  requestBrowserNotificationPermission: async () => undefined,
  subscribeMemoryUpdates: () => () => undefined,
  subscribePersonaUpdates: () => () => undefined,
}))

vi.mock('./components/PersonaRelationshipView', () => ({
  PersonaRelationshipView: ({ onModelRouteDirtyChange }: { onModelRouteDirtyChange?: (dirty: boolean) => void }) => (
    <button onClick={() => onModelRouteDirtyChange?.(true)} type="button">Изменить маршрут без сохранения</button>
  ),
}))

const agent: AgentProfile = {
  id: 'agent-1',
  name: 'Юри',
  gender: 'female',
  preferences: '',
  backstory: '',
  traits: {},
  active: true,
  createdAt: '2026-08-29T09:00:00.000Z',
  updatedAt: '2026-08-29T09:00:00.000Z',
}

const peerAgent: AgentProfile = {
  ...agent,
  id: 'agent-2',
  name: 'Мира',
  active: false,
}

const approval: ApprovalRequest = {
  id: 'approval-1',
  toolCallId: 'call-1',
  title: 'Записать файл в Documents',
  explanation: 'Yuri хочет создать файл notes.md.',
  risk: 'high',
  scope: 'filesystem.write ~/Documents/notes.md',
}

type Deferred<T> = { promise: Promise<T>; resolve: (value: T) => void }

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((settle) => { resolve = settle })
  return { promise, resolve }
}

type RunHandle = { emit: EventSink; settle: (result: RunResult) => void }

/**
 * A bridge that answers everything the shell asks for on a cold start, with the
 * two calls that matter under manual control: the active agent (M-37 hinges on
 * it resolving *after* the first render) and the conversation bootstrap.
 */
function createHarness(options: { agents?: AgentProfile[]; conversations?: Conversation[] } = {}) {
  const stored: Conversation[] = [...(options.conversations ?? [])]
  const activeAgent = deferred<AgentProfile | undefined>()
  const createGate = deferred<void>()
  let handle: RunHandle | undefined

  const listConversations = vi.fn(async () => [...stored])
  const createConversation = vi.fn(async (title: string) => {
    await createGate.promise
    const conversation: Conversation = {
      id: `conv-${stored.length + 1}`,
      title,
      preview: '',
      updatedAt: '2026-08-29T10:00:00.000Z',
      messages: [],
      traces: [],
    }
    stored.push(conversation)
    return conversation
  })
  // The conversation list carries metadata only, so opening a conversation is
  // what fetches its transcript. Nothing this harness stores has one.
  const listMessages = vi.fn(async (conversationId: string) => ({ conversationId, messages: [], traces: [], hasMore: false }))
  const listChatTools = vi.fn(async () => [])
  const sendMessage = vi.fn((_request: ChatRequest, onEvent: EventSink) => new Promise<RunResult>((resolve) => {
    handle = { emit: onEvent, settle: resolve }
  }))

  clientStub = {
    mode: 'mock',
    getOnboardingState: async () => ({ completed: true, providerTested: true, agentConfigured: true }),
    listAgents: async () => options.agents ?? [agent],
    getActiveAgent: () => activeAgent.promise,
    listConversations,
    listMessages,
    createConversation,
    listChatTools,
    getAllowedDirectories: async () => [],
    transcribeAudio: async () => '',
    sendMessage,
    retryLast: sendMessage,
    cancelRun: vi.fn(async () => {}),
    approve: vi.fn(async () => {}),
    listSchedules: async () => [],
    listJobRuns: async () => [],
  } as unknown as YuriClient

  return {
    activeAgent,
    createGate,
    client: () => clientStub,
    createConversation,
    listChatTools,
    listConversations,
    listMessages,
    sendMessage,
    run: () => {
      if (!handle) throw new Error('the run has not started yet')
      return handle
    },
  }
}

/** Boots the shell all the way to a usable composer on the Chat tab. */
async function bootChat(harness: ReturnType<typeof createHarness>) {
  render(<App />)
  await act(async () => {
    harness.activeAgent.resolve(agent)
    harness.createGate.resolve()
  })
  return screen.findByRole('textbox', { name: 'Сообщение Юри' })
}

async function startRun(user: ReturnType<typeof userEvent.setup>, harness: ReturnType<typeof createHarness>) {
  const composer = await bootChat(harness)
  await user.type(composer, 'Создай заметку')
  await user.click(screen.getByRole('button', { name: 'Отправить сообщение' }))
  await waitFor(() => expect(harness.sendMessage).toHaveBeenCalledTimes(1))
  act(() => {
    harness.run().emit({ type: 'run.started', runId: 'run-1' })
    harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: 'Начало' })
  })
}

const goToTasks = (user: ReturnType<typeof userEvent.setup>) => user.click(screen.getByRole('button', { name: /Tasks/ }))
const goToChat = (user: ReturnType<typeof userEvent.setup>) => user.click(screen.getByRole('button', { name: /Chat/ }))

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  Object.defineProperty(window, 'speechSynthesis', {
    configurable: true,
    value: { cancel: vi.fn(), speak: vi.fn() },
  })
})

afterEach(() => vi.restoreAllMocks())

describe('cold start does not double-mount the chat (M-37)', () => {
  it('waits for the active agent before mounting the chat surface', async () => {
    const harness = createHarness()
    await bootChat(harness)

    // One mount, so one round of bootstrap calls instead of the two the
    // placeholder key used to cause.
    expect(harness.listConversations).toHaveBeenCalledTimes(1)
    expect(harness.listChatTools).toHaveBeenCalledTimes(1)
  })

  it('creates exactly one conversation on an empty first launch', async () => {
    const harness = createHarness()
    render(<App />)

    // The first bootstrap is still in flight when the agent arrives: this is the
    // window in which the remount used to open a second "Новый диалог".
    await act(async () => { harness.activeAgent.resolve(agent) })
    await act(async () => { harness.createGate.resolve() })

    await screen.findByRole('textbox', { name: 'Сообщение Юри' })
    expect(harness.createConversation).toHaveBeenCalledTimes(1)
    expect(screen.getAllByText('Новый диалог', { selector: '.conversation-item strong' })).toHaveLength(1)
  })
})

describe('application shell cleanup', () => {
  it('uses the application artwork and hides placeholder header controls and stage labels', async () => {
    const harness = createHarness()
    await bootChat(harness)

    expect(document.querySelector('.brand-mark img')).toBeInTheDocument()
    expect(screen.queryByText('User')).not.toBeInTheDocument()
    expect(screen.queryByText('⌘K')).not.toBeInTheDocument()
    expect(screen.queryByText(/ЭТАП 8 · AGENT PROFILES/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/Yuri stage 8 · agent profiles/i)).not.toBeInTheDocument()
  })
})

describe('agent deletion confirmation', () => {
  it('offers deletion on every roster row and requires the exact agent name', async () => {
    const user = userEvent.setup()
    const harness = createHarness({ agents: [agent, peerAgent] })
    const deleteAgent = vi.fn(async () => undefined)
    await bootChat(harness)
    ;(clientStub as unknown as { deleteAgent: typeof deleteAgent }).deleteAgent = deleteAgent
    ;(clientStub as unknown as { listAgents: () => Promise<AgentProfile[]> }).listAgents = async () => [agent]
    ;(clientStub as unknown as { getActiveAgent: () => Promise<AgentProfile | undefined> }).getActiveAgent = async () => agent

    await user.click(document.querySelector('.sidebar__profile') as HTMLElement)
    expect(screen.queryByRole('button', { name: 'Удалить активного агента' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Удалить агента Юри' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Удалить агента Мира' }))

    expect(screen.getByRole('heading', { name: 'Удалить агента «Мира»?' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Продолжить' }))
    const nameInput = screen.getByRole('textbox', { name: 'Введите имя агента Мира' })
    const deleteButton = screen.getByRole('button', { name: 'Удалить агента' })
    expect(deleteButton).toBeDisabled()
    await user.type(nameInput, 'Мира')
    expect(deleteButton).toBeEnabled()
    await user.click(deleteButton)

    await waitFor(() => expect(deleteAgent).toHaveBeenCalledWith('agent-2'))
  })

  it('does not delete an agent when the typed name differs', async () => {
    const user = userEvent.setup()
    const harness = createHarness()
    const deleteAgent = vi.fn(async () => undefined)
    await bootChat(harness)
    ;(clientStub as unknown as { deleteAgent: typeof deleteAgent }).deleteAgent = deleteAgent

    await user.click(document.querySelector('.sidebar__profile') as HTMLElement)
    await user.click(screen.getByRole('button', { name: 'Удалить агента Юри' }))
    await user.click(screen.getByRole('button', { name: 'Продолжить' }))
    await user.type(screen.getByRole('textbox', { name: 'Введите имя агента Юри' }), 'Мира')

    expect(deleteAgent).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Удалить агента' })).toBeDisabled()
  })
})

describe('the transcript is not re-fetched on every visit to Chat (M-35)', () => {
  it('keeps the loaded history across a tab round-trip, still windowed', async () => {
    const user = userEvent.setup()
    const harness = createHarness({
      conversations: [{
        id: 'conv-1',
        title: 'Длинный диалог',
        preview: '',
        updatedAt: '2026-08-29T10:00:00.000Z',
        messages: Array.from({ length: 200 }, (_, index) => ({
          id: `history-${index}`,
          role: index % 2 === 0 ? 'user' as const : 'assistant' as const,
          content: `Реплика ${index}`,
          status: 'complete' as const,
          createdAt: new Date(Date.UTC(2026, 7, 29, 9, 0, index)).toISOString(),
        })),
        traces: [],
      }],
    })
    await bootChat(harness)

    const windowed = () => document.querySelectorAll('.messages .message__content').length
    expect(windowed()).toBe(40)

    await goToTasks(user)
    await goToChat(user)

    // The chat surface is hidden rather than unmounted (H-9), so returning to
    // the tab re-reads nothing: no `ListConversations`, no re-normalization of
    // every trace, and no rebuild of the timeline.
    expect(harness.listConversations).toHaveBeenCalledTimes(1)
    expect(harness.listChatTools).toHaveBeenCalledTimes(1)
    // The window survives the round-trip too, rather than the whole history
    // being re-mounted.
    expect(windowed()).toBe(40)
    expect(screen.getByText('Реплика 199')).toBeInTheDocument()
    expect(screen.queryByText('Реплика 0')).not.toBeInTheDocument()
  })
})

describe('unsaved per-agent model route', () => {
  it('blocks navigation until the owner explicitly discards the draft', async () => {
    const user = userEvent.setup()
    const harness = createHarness()
    await bootChat(harness)
    await user.click(screen.getByRole('button', { name: /Personality/ }))
    await user.click(await screen.findByRole('button', { name: 'Изменить маршрут без сохранения' }))

    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true)
    const tasks = screen.getByRole('button', { name: /Tasks/ })
    await user.click(tasks)
    expect(tasks).not.toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('button', { name: 'Изменить маршрут без сохранения' })).toBeInTheDocument()

    await user.click(tasks)
    expect(tasks).toHaveAttribute('aria-current', 'page')
    expect(screen.queryByRole('button', { name: 'Изменить маршрут без сохранения' })).not.toBeInTheDocument()
    expect(confirm).toHaveBeenCalledTimes(2)
  })
})

describe('an active run survives leaving the Chat tab (H-9)', () => {
  it('keeps streaming while the user is away and shows the whole answer on return', async () => {
    const user = userEvent.setup()
    const harness = createHarness()
    await startRun(user, harness)

    await goToTasks(user)
    expect(screen.queryByRole('textbox', { name: 'Сообщение Юри' })).not.toBeInTheDocument()

    // Tokens that arrive while the chat is off-screen must still land.
    act(() => {
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: ' ответа' })
      harness.run().emit({ type: 'assistant.delta', runId: 'run-1', messageId: 'msg-1', delta: ' целиком' })
    })

    await goToChat(user)
    expect(screen.getByText('Начало ответа целиком')).toBeInTheDocument()
    expect(screen.getByText('Печатает')).toBeInTheDocument()

    act(() => {
      harness.run().emit({ type: 'assistant.completed', runId: 'run-1', messageId: 'msg-1' })
      harness.run().emit({ type: 'run.completed', runId: 'run-1', status: 'complete' })
    })

    expect(screen.queryByText('Печатает')).not.toBeInTheDocument()
    expect(screen.getAllByText('Начало ответа целиком')).toHaveLength(1)
  })

  it('still stops the run after a tab round-trip', async () => {
    const user = userEvent.setup()
    const harness = createHarness()
    await startRun(user, harness)

    await goToTasks(user)
    await goToChat(user)

    await user.click(screen.getByRole('button', { name: 'Остановить запуск' }))
    await waitFor(() => expect(harness.client().cancelRun).toHaveBeenCalledWith('run-1'))
  })

  it('presents an approval that arrives while the user is on another tab', async () => {
    const user = userEvent.setup()
    const harness = createHarness()
    await startRun(user, harness)

    await goToTasks(user)
    act(() => {
      harness.run().emit({ type: 'approval.required', runId: 'run-1', approval })
    })

    // The whole point of H-9: the decision reaches the user where they are,
    // instead of waiting for a return that may never happen.
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveTextContent('Записать файл в Documents')
    expect(screen.getByText(/Запуск идёт на вкладке «Чат»/)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Разрешить действие' }))
    await waitFor(() => expect(harness.client().approve).toHaveBeenCalledWith('approval-1', 'approve'))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('takes the user back to the chat when an undelivered decision is dismissed', async () => {
    const user = userEvent.setup()
    const approve = vi.fn(async () => { throw new Error('Мост недоступен') })
    const harness = createHarness()
    await startRun(user, harness)
    ;(clientStub as unknown as { approve: typeof approve }).approve = approve

    await goToTasks(user)
    act(() => {
      harness.run().emit({ type: 'approval.required', runId: 'run-1', approval })
    })
    await screen.findByRole('dialog')

    await user.click(screen.getByRole('button', { name: 'Разрешить действие' }))
    await waitFor(() => expect(approve).toHaveBeenCalled())
    await user.click(screen.getByRole('button', { name: 'Закрыть' }))

    // The explanation of what just failed lives in the chat, so the chat is
    // where the user has to end up.
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.getByText(/Решение по подтверждению не было передано агенту/)).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Сообщение Юри' })).toBeInTheDocument()
  })
})

describe('portable agent profiles', () => {
  it('opens a validated profile in the ordinary creation review without creating it', async () => {
    const user = userEvent.setup()
    const harness = createHarness()
    const importedDraft = cloneAgentDraft({ ...defaultAgentDraft, name: 'Эмили', creationMode: 'advanced', presetId: 'custom' })
    const openPortableAgentProfile = vi.fn(async () => ({ path: '/tmp/emily.json', exportedAt: '2026-08-31T12:00:00Z', sizeBytes: 2048, checksum: 'sha256:abc', profile: importedDraft }))
    const createAgent = vi.fn(async () => ({ ...agent, id: 'agent-imported', name: 'Эмили' }))
    ;(clientStub as unknown as { openPortableAgentProfile: typeof openPortableAgentProfile; createAgent: typeof createAgent }).openPortableAgentProfile = openPortableAgentProfile
    ;(clientStub as unknown as { createAgent: typeof createAgent }).createAgent = createAgent
    await bootChat(harness)

    await user.click(document.querySelector('.sidebar__profile') as HTMLElement)
    await user.click(screen.getByRole('button', { name: /Импорт/ }))

    expect(await screen.findByRole('heading', { name: 'Проверить импортируемого агента' })).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: /Имя агента/ })).toHaveValue('Эмили')
    expect(screen.getByText(/память, runtime histories, разрешения и secrets не импортируются/)).toBeInTheDocument()
    expect(openPortableAgentProfile).toHaveBeenCalledTimes(1)
    expect(createAgent).not.toHaveBeenCalled()
  })

  it('exports the active owner profile from the roster menu', async () => {
    const user = userEvent.setup()
    const harness = createHarness()
    const exportActiveAgentProfile = vi.fn(async () => ({ path: '/tmp/yuri.json', exportedAt: '2026-08-31T12:00:00Z', sizeBytes: 1024, checksum: 'sha256:def', profile: defaultAgentDraft }))
    ;(clientStub as unknown as { exportActiveAgentProfile: typeof exportActiveAgentProfile }).exportActiveAgentProfile = exportActiveAgentProfile
    await bootChat(harness)

    await user.click(document.querySelector('.sidebar__profile') as HTMLElement)
    await user.click(screen.getByRole('button', { name: /Экспорт/ }))

    await waitFor(() => expect(exportActiveAgentProfile).toHaveBeenCalledTimes(1))
    await user.click(document.querySelector('.sidebar__profile') as HTMLElement)
    expect(await screen.findByText(/Профиль Yuri экспортирован/)).toBeInTheDocument()
  })
})
