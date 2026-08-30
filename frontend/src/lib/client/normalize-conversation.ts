import type { ChatAttachment, ChatHistoryPage, Conversation, ConversationTitleSource, PeerDialogue, RunTrace } from '../contracts'
import { normalizeRunTrace, normalizeToolCall, sortRunTraces } from '../chat-trace'
import { normalizeBoolean, nowIso, optionalString } from './primitives'
import type { UnknownRecord } from './primitives'

function cloneTrace(trace: RunTrace): RunTrace {
  return {
    ...trace,
    toolCalls: trace.toolCalls?.map((toolCall) => ({ ...toolCall, args: { ...toolCall.args } })),
    steps: trace.steps.map((step) => {
      if (step.kind === 'tool') return { ...step, toolCall: { ...step.toolCall, args: { ...step.toolCall.args } } }
      if (step.kind === 'approval') return { ...step, approval: { ...step.approval } }
      return { ...step }
    }),
  }
}

function normalizeMessages(rawMessages: unknown): Conversation['messages'] {
  return Array.isArray(rawMessages)
    ? rawMessages.flatMap((item): Conversation['messages'] => {
        if (!item || typeof item !== 'object') return []
        const message = item as UnknownRecord
        const messageId = optionalString(message, 'id', 'messageId', 'message_id')
        if (!messageId) return []
        const roleValue = String(message.role ?? '').toLowerCase()
        const role = roleValue === 'user' || roleValue === 'assistant' || roleValue === 'tool' ? roleValue : 'assistant'
        const statusValue = String(message.status ?? '').toLowerCase()
        const status = statusValue === 'streaming' || statusValue === 'cancelled' || statusValue === 'error' ? statusValue : 'complete'
        const toolCall = normalizeToolCall(message.toolCall ?? message.tool_call, status === 'error' ? 'failed' : 'completed')
        const rawAttachments = message.attachments ?? message.files
        const attachments: ChatAttachment[] = Array.isArray(rawAttachments)
          ? rawAttachments.flatMap((value) => {
              if (!value || typeof value !== 'object') return []
              const attachment = value as UnknownRecord
              const id = optionalString(attachment, 'id', 'attachmentId', 'attachment_id')
              const name = optionalString(attachment, 'name', 'fileName', 'file_name')
              if (!id || !name) return []
              return [{
                id,
                name,
                kind: String(attachment.kind).toLowerCase() === 'image' ? 'image' as const : 'text' as const,
                mediaType: optionalString(attachment, 'mediaType', 'media_type', 'mimeType', 'mime_type') ?? 'application/octet-stream',
                sizeBytes: Math.max(0, Number(attachment.sizeBytes ?? attachment.size_bytes ?? 0) || 0),
              }]
            })
          : []
        return [{
          id: messageId,
          role,
          content: String(message.content ?? message.text ?? ''),
          status,
          createdAt: optionalString(message, 'createdAt', 'created_at', 'timestamp') ?? nowIso(),
          runId: optionalString(message, 'runId', 'run_id'),
          toolCall,
          attachments: attachments.length > 0 ? attachments : undefined,
        }]
      })
    : []
}

function normalizeTraces(value: unknown): RunTrace[] {
  return Array.isArray(value)
    ? value.map((trace, index) => normalizeRunTrace(trace, index)).filter((trace): trace is RunTrace => Boolean(trace))
    : []
}

function normalizeConversationTitleSource(value: unknown): ConversationTitleSource {
  const source = String(value ?? '').trim().toLowerCase()
  return source === 'generated' || source === 'user' ? source : 'default'
}

function normalizeConversation(value: unknown): Conversation | undefined {
  if (!value || typeof value !== 'object') return undefined
  const source = value as UnknownRecord
  const id = optionalString(source, 'id', 'conversationId', 'conversation_id')
  if (!id) return undefined
  const messages = normalizeMessages(source.messages ?? source.items ?? [])
  const traces = normalizeTraces(source.traces ?? source.executionTraces ?? source.execution_traces ?? [])
  return {
    id,
    title: optionalString(source, 'title', 'name') ?? 'Новый диалог',
    titleSource: normalizeConversationTitleSource(source.titleSource ?? source.title_source),
    preview: optionalString(source, 'preview', 'summary') ?? messages.at(-1)?.content ?? 'Пока нет сообщений',
    updatedAt: optionalString(source, 'updatedAt', 'updated_at', 'createdAt', 'created_at') ?? nowIso(),
    messages,
    traces: traces.length > 0 ? sortRunTraces(traces.map(cloneTrace)) : undefined,
    // Absent means "the backend did not page this", which the transcript reads
    // as "there is nothing older to fetch".
    hasMoreMessages: 'hasMoreMessages' in source || 'has_more_messages' in source
      ? normalizeBoolean(source.hasMoreMessages ?? source.has_more_messages, false)
      : undefined,
  }
}

/**
 * A "show earlier" page. A bridge that predates paging returns nothing at all,
 * which normalizes to an empty page that reports no further history, so the
 * transcript stops asking instead of looping.
 */
function normalizeChatHistoryPage(value: unknown, conversationId: string): ChatHistoryPage {
  if (!value || typeof value !== 'object') {
    return { conversationId, messages: [], traces: [], hasMore: false }
  }
  const source = value as UnknownRecord
  const traces = normalizeTraces(source.traces ?? source.executionTraces ?? source.execution_traces ?? [])
  return {
    conversationId: optionalString(source, 'conversationId', 'conversation_id') ?? conversationId,
    messages: normalizeMessages(source.messages ?? source.items ?? []),
    traces: traces.length > 0 ? sortRunTraces(traces.map(cloneTrace)) : [],
    hasMore: normalizeBoolean(source.hasMore ?? source.has_more, false),
  }
}

function normalizeConversationList(value: unknown): Conversation[] {
  const rawItems = Array.isArray(value)
    ? value
    : value && typeof value === 'object'
      ? ((value as UnknownRecord).conversations ?? (value as UnknownRecord).items ?? (value as UnknownRecord).results)
      : undefined
  return Array.isArray(rawItems)
    ? rawItems.map((item) => normalizeConversation(item)).filter((conversation): conversation is Conversation => Boolean(conversation))
    : []
}

function cloneConversation(conversation: Conversation): Conversation {
  return {
    ...conversation,
    messages: conversation.messages.map((message) => ({
      ...message,
      toolCall: message.toolCall ? { ...message.toolCall, args: { ...message.toolCall.args } } : undefined,
      attachments: message.attachments?.map((attachment) => ({ ...attachment })),
    })),
    traces: conversation.traces?.map(cloneTrace),
  }
}

function clonePeerDialogue(dialogue: PeerDialogue): PeerDialogue {
  return {
    ...dialogue,
    messages: dialogue.messages.map((message) => ({ ...message })),
  }
}

export { cloneConversation, clonePeerDialogue, normalizeChatHistoryPage, normalizeConversation, normalizeConversationList, normalizeConversationTitleSource }
