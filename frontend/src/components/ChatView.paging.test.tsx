// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { BackendConnection } from '../lib/backend'
import type { ChatHistoryPage, ChatMessage, Conversation, ConversationPageOptions, YuriClient } from '../lib/contracts'
import { ChatView } from './ChatView'

/**
 * M-10. Two defects, both of them silent.
 *
 * The sidebar called the conversation list with no offset, so an owner with
 * more conversations than one page saw the newest page and nothing said the
 * rest existed. And the list dragged a slice of every conversation's transcript
 * behind it to draw a sidebar that renders a title, a one-line preview and a
 * timestamp — so the renderer received a page of transcripts in order to show
 * one.
 *
 * These tests assert what the reader can reach and what the client is asked
 * for, not render counts.
 */

let clientStub: YuriClient

vi.mock('../lib/client', () => ({
  createYuriClient: () => clientStub,
}))

const backend: BackendConnection = { status: 'connected', label: 'Backend connected', detail: 'Wails runtime is ready' }

/** Matches the sidebar's page size and the bridge's clamp. */
const pageSize = 200
/** Matches `timelineWindowSize` in the transcript (H-18). */
const windowSize = 40

function transcriptMessage(index: number): ChatMessage {
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

/** A conversation as the list now returns it: metadata, no transcript. */
function metadataOnly(index: number): Conversation {
  return {
    id: `conv-${index}`,
    title: `Диалог ${index}`,
    preview: `Последняя реплика ${index}`,
    updatedAt: '2026-08-29T10:00:00.000Z',
    messages: [],
    traces: [],
  }
}

type Harness = {
  listConversations: ReturnType<typeof vi.fn>
  listMessages: ReturnType<typeof vi.fn>
}

function createHarness(
  store: Conversation[],
  transcripts: Record<string, ChatHistoryPage> = {},
): Harness {
  const listConversations = vi.fn(async (options: ConversationPageOptions = {}) => {
    const offset = options.offset ?? 0
    const limit = options.limit && options.limit > 0 ? options.limit : store.length
    return store.slice(offset, offset + limit)
  })
  const listMessages = vi.fn(async (conversationId: string): Promise<ChatHistoryPage> =>
    transcripts[conversationId] ?? { conversationId, messages: [], traces: [], hasMore: false })
  clientStub = {
    mode: 'mock',
    listConversations,
    listMessages,
    createConversation: async () => metadataOnly(9999),
    listChatTools: async () => [],
    getAllowedDirectories: async () => [],
    transcribeAudio: async () => '',
    sendMessage: vi.fn(),
    retryLast: vi.fn(),
    cancelRun: vi.fn(async () => {}),
    approve: vi.fn(async () => {}),
  } as unknown as YuriClient
  return { listConversations, listMessages }
}

function sidebarTitles(): string[] {
  return Array.from(document.querySelectorAll('.conversation-item__copy strong')).map((node) => node.textContent ?? '')
}

/**
 * Clicks a sidebar row by its title. The row's accessible name is its whole
 * contents — title, preview and timestamp — so it is addressed by the title
 * element rather than by an accessible-name match that would have to restate
 * the rest of the row.
 */
async function selectConversation(user: ReturnType<typeof userEvent.setup>, title: string): Promise<void> {
  const heading = Array.from(document.querySelectorAll('.conversation-item__copy strong'))
    .find((node) => node.textContent === title)
  if (!heading) throw new Error(`the sidebar has no conversation titled ${title}`)
  const row = heading.closest('button')
  if (!row) throw new Error(`the row for ${title} is not clickable`)
  await user.click(row)
}

function renderedMessages(): string[] {
  return Array.from(document.querySelectorAll('.messages .message__content')).map((node) => node.textContent ?? '')
}

function announcerText(): string {
  return document.querySelector('[data-testid="chat-announcer"]')?.textContent?.trim() ?? ''
}

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  Object.defineProperty(window, 'speechSynthesis', {
    configurable: true,
    value: { speak: vi.fn(), cancel: vi.fn(), getVoices: () => [] },
  })
})

describe('the sidebar can reach past its first page (M-10)', () => {
  it('offers the rest of the store and reaches conversation 201 and beyond', async () => {
    const total = 250
    const store = Array.from({ length: total }, (_, index) => metadataOnly(index))
    const harness = createHarness(store)
    const user = userEvent.setup()
    render(<ChatView agentName="Yuri" backend={backend} onOpenSettings={vi.fn()} />)

    await waitFor(() => expect(sidebarTitles().length).toBe(pageSize))
    // The first page is exactly what the sidebar always drew — and it stops at
    // conversation 199, which is where the reader used to be stranded.
    expect(sidebarTitles()[0]).toBe('Диалог 0')
    expect(sidebarTitles()[pageSize - 1]).toBe('Диалог 199')
    expect(sidebarTitles()).not.toContain('Диалог 200')
    expect(harness.listConversations).toHaveBeenCalledWith({ limit: pageSize })

    const more = await screen.findByRole('button', { name: 'Показать ещё диалоги' })
    await user.click(more)

    await waitFor(() => expect(sidebarTitles().length).toBe(total))
    expect(harness.listConversations).toHaveBeenLastCalledWith({ limit: pageSize, offset: pageSize })
    // Every conversation that used to be unreachable, by name, in order.
    const titles = sidebarTitles()
    for (let index = pageSize; index < total; index += 1) {
      expect(titles[index]).toBe(`Диалог ${index}`)
    }
    // Including the very last one.
    expect(titles[total - 1]).toBe('Диалог 249')
    // No conversation appears twice after the pages are joined.
    expect(new Set(titles).size).toBe(total)
  }, 10_000)

  it('retires the control once a page adds nothing, so paging terminates', async () => {
    const total = 260
    const store = Array.from({ length: total }, (_, index) => metadataOnly(index))
    const harness = createHarness(store)
    const user = userEvent.setup()
    render(<ChatView agentName="Yuri" backend={backend} onOpenSettings={vi.fn()} />)
    await waitFor(() => expect(sidebarTitles().length).toBe(pageSize))

    await user.click(await screen.findByRole('button', { name: 'Показать ещё диалоги' }))
    await waitFor(() => expect(sidebarTitles().length).toBe(total))

    // A short page is the end of the store, so the control goes away rather
    // than staying armed for a click that could only re-fetch the same tail.
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Показать ещё диалоги' })).toBeNull())
    expect(harness.listConversations).toHaveBeenCalledTimes(2)
  }, 10_000)

  it('stays silent when the whole store fits on the first page', async () => {
    createHarness(Array.from({ length: 12 }, (_, index) => metadataOnly(index)))
    render(<ChatView agentName="Yuri" backend={backend} onOpenSettings={vi.fn()} />)
    await waitFor(() => expect(sidebarTitles().length).toBe(12))
    expect(screen.queryByRole('button', { name: 'Показать ещё диалоги' })).toBeNull()
  })
})

describe('a transcript is fetched when its conversation is opened (M-10)', () => {
  const transcript = (id: string, count: number, hasMore: boolean): ChatHistoryPage => ({
    conversationId: id,
    messages: Array.from({ length: count }, (_, index) => transcriptMessage(index)),
    traces: [],
    hasMore,
  })

  it('loads the opened conversation only, and does not ask for the others', async () => {
    const store = [metadataOnly(0), metadataOnly(1), metadataOnly(2)]
    const harness = createHarness(store, {
      'conv-0': transcript('conv-0', 6, false),
      'conv-1': transcript('conv-1', 4, false),
    })
    const user = userEvent.setup()
    render(<ChatView agentName="Yuri" backend={backend} onOpenSettings={vi.fn()} />)

    await waitFor(() => expect(renderedMessages().length).toBe(6))
    // One transcript for the conversation actually on screen — not three.
    expect(harness.listMessages).toHaveBeenCalledTimes(1)
    expect(harness.listMessages).toHaveBeenCalledWith('conv-0', 60, '')

    await selectConversation(user, 'Диалог 1')
    await waitFor(() => expect(renderedMessages().length).toBe(4))
    expect(harness.listMessages).toHaveBeenCalledTimes(2)

    // Going back is served from what is already held.
    await selectConversation(user, 'Диалог 0')
    await waitFor(() => expect(renderedMessages().length).toBe(6))
    expect(harness.listMessages).toHaveBeenCalledTimes(2)
  })

  it('leaves a conversation that arrived with its transcript alone', async () => {
    // A bridge built before the list was split still answers with transcripts.
    // Re-fetching one would overwrite what it sent.
    const carried: Conversation = {
      ...metadataOnly(0),
      messages: Array.from({ length: 5 }, (_, index) => transcriptMessage(index)),
    }
    const harness = createHarness([carried])
    render(<ChatView agentName="Yuri" backend={backend} onOpenSettings={vi.fn()} />)
    await waitFor(() => expect(renderedMessages().length).toBe(5))
    expect(harness.listMessages).not.toHaveBeenCalled()
  })

  it('keeps the transcript windowed after a hydrated backlog lands (H-18)', async () => {
    // The DOM budget is a render bound recomputed when the open conversation
    // changes. A transcript that arrives *after* its conversation is already on
    // screen does not change it, so without re-windowing the whole page would
    // mount at once — which is the bug H-18 closed.
    createHarness([metadataOnly(0)], { 'conv-0': transcript('conv-0', 60, true) })
    render(<ChatView agentName="Yuri" backend={backend} onOpenSettings={vi.fn()} />)

    await waitFor(() => expect(renderedMessages().length).toBeGreaterThan(0))
    await waitFor(() => expect(renderedMessages().length).toBe(windowSize))
    // The newest window, and the earlier history is offered rather than mounted.
    expect(renderedMessages()[windowSize - 1]).toBe('Реплика 59')
    expect(screen.getByRole('button', { name: /Показать более ранние/ })).toBeInTheDocument()
  })

  it('does not read a hydrated backlog out loud (M-44)', async () => {
    // Opening a conversation must not announce its history: the last answer is
    // already on screen, it is not news. That rule keyed on the moment the
    // conversation was selected, which is no longer the moment its transcript
    // appears.
    createHarness([metadataOnly(0)], { 'conv-0': transcript('conv-0', 8, false) })
    render(<ChatView agentName="Yuri" backend={backend} onOpenSettings={vi.fn()} />)

    await waitFor(() => expect(renderedMessages().length).toBe(8))
    // The newest entry is a completed assistant answer with content — exactly
    // what the announcer speaks when it is genuinely new.
    expect(renderedMessages()[7]).toBe('Реплика 7')
    expect(announcerText()).toBe('')
    // And it stays silent: nothing arrives later to change its mind.
    await waitFor(() => expect(announcerText()).toBe(''))
  })

  it('reports a failed transcript fetch and retries when the conversation is reopened', async () => {
    const store = [metadataOnly(0), metadataOnly(1)]
    const harness = createHarness(store, { 'conv-0': transcript('conv-0', 3, false) })
    harness.listMessages.mockRejectedValueOnce(new Error('bridge is down'))
    const user = userEvent.setup()
    render(<ChatView agentName="Yuri" backend={backend} onOpenSettings={vi.fn()} />)

    expect(await screen.findByRole('alert')).toHaveTextContent('Не удалось загрузить историю диалога.')
    // The claim is given back, so reopening is a real retry rather than a
    // conversation that is permanently blank but marked loaded.
    await selectConversation(user, 'Диалог 1')
    await selectConversation(user, 'Диалог 0')
    await waitFor(() => expect(renderedMessages().length).toBe(3))
  })
})
