import type { CSSProperties } from 'react'

import type { ChatHistoryPage, ChatMessage, Conversation, ConversationTitleSource, RunStatus } from '../lib/contracts'
import { sortRunTraces } from '../lib/chat-trace'

/**
 * `.chat-view--workspace` sets `display: grid`, which outranks the user-agent
 * rule behind the `hidden` attribute — the inline style is what actually takes
 * the surface out of the layout.
 */
export const offscreenStyle: CSSProperties = { display: 'none' }

/**
 * Messages of the run that is streaming right now.
 *
 * They deliberately live outside `conversations`: appending a token to a
 * conversation replaced the conversation object, which invalidated the timeline
 * memo and re-rendered every message and trace block in the history (C-1). The
 * buffer is merged back into the durable state when the answer, or the run,
 * ends. Nothing here assumes one delta per token — the backend is free to batch
 * them into wider windows, which only makes the buffer cheaper.
 */
export type StreamBuffer = {
  conversationId: string
  messages: ChatMessage[]
}

export const emptyStreamMessages: ChatMessage[] = []
export const emptyStream: StreamBuffer = { conversationId: '', messages: emptyStreamMessages }

/** How close to the bottom still counts as "following the answer". */
export const stickToBottomThresholdPx = 48

/**
 * Opening a conversation puts only its tail in the DOM.
 *
 * Nothing trimmed the transcript before, so a dialogue kept for weeks expanded
 * thousands of nodes on tab open — every message bubble and every trace block —
 * and all of them then took part in reconciliation (H-18). The window is a
 * render-side bound only: the state still holds the whole conversation, so
 * "показать более ранние" is instant and nothing has to be re-fetched.
 */
export const timelineWindowSize = 40
/** How much one click of "показать более ранние" uncovers. */
export const timelineWindowStep = 40

/**
 * Conversations asked for per sidebar page.
 *
 * It matches the bridge's own clamp, so the first paint is the page the sidebar
 * always drew and "показать ещё" only ever adds what used to be unreachable.
 * The bridge clamps whatever it is handed either way — this is a request, not
 * an assumption about the backend.
 */
export const conversationPageSize = 200

/**
 * Newest messages fetched when a conversation is opened.
 *
 * The list carries metadata only, so this is where a transcript actually comes
 * from. Deliberately wider than `timelineWindowSize`, so the first
 * "показать более ранние" click is served from memory rather than from the
 * backend — the same reason the list's tail used to be wider than the window.
 */
export const conversationTailSize = 60

/**
 * Folds a fetched page of transcript into a conversation.
 *
 * The page is always older than whatever is already held, so it is prepended:
 * anything in state was produced after the request went out — an optimistic
 * user bubble, a streamed answer — and must stay at the end. Ids already held
 * are dropped, so a page that overlaps what the reader has cannot double a
 * bubble.
 *
 * `hasMore` is taken from the page because the page is the only thing that
 * knows: a conversation list that carries no transcript cannot say whether one
 * continues. Note that this is the *non-empty* case of "показать более ранние"
 * as well — the empty case must not write `hasMore` back, and is handled at the
 * call site (N-18).
 */
export function hydrateConversation(conversation: Conversation, page: ChatHistoryPage): Conversation {
  const known = new Set(conversation.messages.map((message) => message.id))
  const fresh = page.messages.filter((message) => !known.has(message.id))
  const knownTraces = new Set((conversation.traces ?? []).map((trace) => trace.id))
  const freshTraces = page.traces.filter((trace) => !knownTraces.has(trace.id))
  return {
    ...conversation,
    messages: fresh.length > 0 ? [...fresh, ...conversation.messages] : conversation.messages,
    traces: freshTraces.length > 0 ? sortRunTraces([...freshTraces, ...(conversation.traces ?? [])]) : conversation.traces,
    hasMoreMessages: page.hasMore,
  }
}

/**
 * Appends a page of conversations, dropping any the sidebar already holds.
 *
 * Paging by offset over a list ordered by `updatedAt` can hand back a
 * conversation that moved across the page boundary while the reader was
 * reading; without the filter it would appear in the sidebar twice.
 */
export function appendConversations(existing: Conversation[], page: Conversation[]): Conversation[] {
  const known = new Set(existing.map((conversation) => conversation.id))
  const fresh = page.filter((conversation) => !known.has(conversation.id))
  return fresh.length === 0 ? existing : [...existing, ...fresh]
}

export const starterPrompts = (agentName: string) => [
  `Познакомиться с ${agentName}`,
  'Проверить доступ к файлам',
  'Запиши заметку в Documents',
]

export const statusCopy = (agentName: string): Record<RunStatus, string> => ({
  idle: 'Жду задачу',
  thinking: `${agentName} думает…`,
  tool_running: 'Выполняю действие…',
  waiting_approval: 'Ожидается ваше разрешение',
  speaking: `${agentName} говорит…`,
  cancelled: 'Запуск остановлен',
  error: 'Нужна проверка запуска',
})

export function makeId(prefix: string): string {
  const suffix = typeof crypto !== 'undefined' && 'randomUUID' in crypto
    ? crypto.randomUUID()
    : Math.random().toString(36).slice(2)
  return `${prefix}-${suffix}`
}

export function updateConversation(conversations: Conversation[], id: string, update: (conversation: Conversation) => Conversation): Conversation[] {
  return conversations.map((conversation) => conversation.id === id ? update(conversation) : conversation)
}

export interface ConversationTitleUpdateLike {
  title: string
  titleSource: ConversationTitleSource
  updatedAt?: string
}

/**
 * Applies a title event without allowing a late generated result to overwrite
 * a title the owner explicitly chose. The backend also enforces this with a
 * compare-and-set update; keeping the same rule in the renderer prevents a
 * stale event already in the Wails queue from briefly reverting the UI.
 */
export function applyConversationTitleUpdate(conversation: Conversation, update: ConversationTitleUpdateLike): Conversation {
  const rank: Record<ConversationTitleSource, number> = { default: 0, generated: 1, user: 2 }
  const currentSource = conversation.titleSource ?? 'default'
  const incomingRank = rank[update.titleSource]
  const currentRank = rank[currentSource]
  if (incomingRank < currentRank) return conversation
  if (incomingRank === currentRank && update.updatedAt) {
    const incomingTime = Date.parse(update.updatedAt)
    const currentTime = Date.parse(conversation.updatedAt)
    if (Number.isFinite(incomingTime) && Number.isFinite(currentTime) && incomingTime < currentTime) return conversation
  }
  return {
    ...conversation,
    title: update.title,
    titleSource: update.titleSource,
    updatedAt: update.updatedAt ?? conversation.updatedAt,
  }
}

/**
 * Upper bound on the set of flushed streaming message ids (N-20).
 *
 * That set only has to answer one question — "did this id already leave the
 * stream buffer?" — during the window between a flush and the next commit,
 * after which `conversations` itself is authoritative and is consulted anyway.
 * The window is a single render, and a flush moves the messages of one run, so
 * the working set is one or two ids; 256 is a bound with several orders of
 * magnitude of headroom that still keeps the set from growing for the lifetime
 * of the session. `ChatView` is mounted once per session and hidden rather than
 * unmounted on a tab switch (H-9/M-37), so "for the lifetime of the view" is no
 * longer a short time.
 *
 * Clearing on conversation switch is deliberately *not* the bound: it would not
 * bound anything (a session spent in one conversation never clears) and it
 * would drop ids inside the very window they are kept for, which is how a
 * flushed answer forks into a second bubble.
 */
export const committedStreamIdLimit = 256

/**
 * Records a flushed id, evicting the oldest once the bound is reached. `Set`
 * iterates in insertion order, so the first entry is always the oldest.
 */
export function rememberCommittedStreamId(committed: Set<string>, id: string): Set<string> {
  committed.add(id)
  while (committed.size > committedStreamIdLimit) {
    const oldest = committed.values().next()
    if (oldest.done) break
    committed.delete(oldest.value)
  }
  return committed
}

/**
 * Takes "показать более ранние" out of service for one conversation.
 *
 * Used wherever a backend page turned out to add nothing: the control must not
 * stay armed for a click that would only repeat the identical request (N-18).
 * The conversation is returned unchanged when it is already retired, so the
 * memoized transcript is not invalidated for nothing.
 */
export function retireEarlierHistory(conversation: Conversation): Conversation {
  if (!conversation.hasMoreMessages) return conversation
  return { ...conversation, hasMoreMessages: false }
}

export function upsertMessage(messages: ChatMessage[], message: ChatMessage): ChatMessage[] {
  const existingIndex = messages.findIndex((candidate) => candidate.id === message.id)
  if (existingIndex === -1) return [...messages, message]
  return messages.map((candidate, index) => index === existingIndex ? message : candidate)
}
