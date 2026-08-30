import type {
  ActivityEvent,
  ActivityStatus,
  ActivityType,
  PeerDialogue,
  PeerDialogueMessage,
  PeerDialogueStatus,
} from '../contracts'
import { nowIso, optionalNumber, optionalString } from './primitives'
import type { UnknownRecord } from './primitives'

function normalizePeerDialogueStatus(value: unknown): PeerDialogueStatus {
  const status = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (status === 'queued' || status === 'pending') return 'queued'
  if (status === 'running' || status === 'started' || status === 'active') return 'running'
  if (status === 'cancelling' || status === 'canceling' || status === 'stopping') return 'cancelling'
  if (status === 'completed' || status === 'complete' || status === 'success' || status === 'succeeded') return 'completed'
  if (status === 'failed' || status === 'error' || status === 'failure') return 'failed'
  if (status === 'cancelled' || status === 'canceled' || status === 'aborted') return 'cancelled'
  if (status === 'expired' || status === 'timeout' || status === 'timed_out') return 'expired'
  return 'unknown'
}

function normalizePeerDialogueMessage(value: unknown): PeerDialogueMessage | undefined {
  if (!value || typeof value !== 'object') return undefined
  const raw = value as UnknownRecord
  const id = optionalString(raw, 'id', 'messageId', 'message_id')
  if (!id) return undefined
  return {
    id,
    sequence: Math.max(0, Math.round(optionalNumber(raw, 'sequence', 'index', 'turn') ?? 0)),
    senderAgentId: optionalString(raw, 'senderAgentId', 'sender_agent_id', 'senderId', 'sender_id') ?? '',
    senderName: optionalString(raw, 'senderName', 'sender_name') ?? 'Агент',
    recipientAgentId: optionalString(raw, 'recipientAgentId', 'recipient_agent_id', 'recipientId', 'recipient_id') ?? '',
    recipientName: optionalString(raw, 'recipientName', 'recipient_name') ?? 'Агент',
    content: String(raw.content ?? raw.text ?? raw.message ?? ''),
    createdAt: optionalString(raw, 'createdAt', 'created_at', 'timestamp') ?? nowIso(),
  }
}

function normalizePeerDialogue(value: unknown): PeerDialogue | undefined {
  if (!value || typeof value !== 'object') return undefined
  const rawValue = value as UnknownRecord
  const source = rawValue.dialogue && typeof rawValue.dialogue === 'object'
    ? rawValue.dialogue as UnknownRecord
    : rawValue
  const id = optionalString(source, 'id', 'dialogueId', 'dialogue_id')
  const initiatorAgentId = optionalString(source, 'initiatorAgentId', 'initiator_agent_id', 'initiatorId', 'initiator_id')
  const peerAgentId = optionalString(source, 'peerAgentId', 'peer_agent_id', 'peerId', 'peer_id')
  if (!id || !initiatorAgentId || !peerAgentId) return undefined
  const rawMessages = source.messages ?? source.transcript
  const messages = Array.isArray(rawMessages)
    ? rawMessages.map(normalizePeerDialogueMessage).filter((item): item is PeerDialogueMessage => Boolean(item)).sort((a, b) => a.sequence - b.sequence)
    : []
  return {
    id,
    initiatorAgentId,
    initiatorName: optionalString(source, 'initiatorName', 'initiator_name') ?? 'Агент',
    peerAgentId,
    peerName: optionalString(source, 'peerName', 'peer_name') ?? 'Агент',
    purpose: optionalString(source, 'purpose', 'goal', 'summary') ?? 'Внутренний диалог',
    status: normalizePeerDialogueStatus(source.status ?? source.state),
    turnCount: Math.max(0, Math.round(optionalNumber(source, 'turnCount', 'turn_count', 'turns') ?? messages.length)),
    maxTurns: Math.max(0, Math.round(optionalNumber(source, 'maxTurns', 'max_turns', 'turnLimit', 'turn_limit') ?? 0)),
    tokensUsed: Math.max(0, Math.round(optionalNumber(source, 'tokensUsed', 'tokens_used', 'usedTokens', 'used_tokens') ?? 0)),
    maxTokens: Math.max(0, Math.round(optionalNumber(source, 'maxTokens', 'max_tokens', 'tokenLimit', 'token_limit') ?? 0)),
    createdAt: optionalString(source, 'createdAt', 'created_at', 'timestamp') ?? nowIso(),
    finishedAt: optionalString(source, 'finishedAt', 'finished_at', 'completedAt', 'completed_at'),
    failure: optionalString(source, 'failure', 'error', 'lastError', 'last_error'),
    messages,
  }
}

function normalizePeerDialogueList(value: unknown): PeerDialogue[] {
  const rawItems = Array.isArray(value)
    ? value
    : value && typeof value === 'object'
      ? ((value as UnknownRecord).items
        ?? (value as UnknownRecord).dialogues
        ?? (value as UnknownRecord).peerDialogues
        ?? (value as UnknownRecord).peer_dialogues
        ?? (value as UnknownRecord).results)
      : undefined
  return Array.isArray(rawItems)
    ? rawItems.map(normalizePeerDialogue).filter((item): item is PeerDialogue => Boolean(item))
    : []
}

function normalizeActivityType(value: unknown): ActivityType {
  const type = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (type === 'job' || type === 'job_run' || type === 'schedule' || type === 'scheduler') return 'job'
  if (type === 'proactive' || type === 'proactivity' || type === 'notification') return 'proactive'
  if (type === 'reflection' || type === 'self_reflection') return 'reflection'
  if (type === 'memory' || type === 'memory_write') return 'memory'
  if (type === 'system' || type === 'audit') return 'system'
  return 'unknown'
}

function normalizeActivityStatus(value: unknown): ActivityStatus {
  const status = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (status === 'queued' || status === 'pending') return 'queued'
  if (status === 'running' || status === 'started' || status === 'active') return 'running'
  if (status === 'completed' || status === 'complete' || status === 'success' || status === 'succeeded') return 'completed'
  if (status === 'failed' || status === 'error' || status === 'failure') return 'failed'
  if (status === 'cancelled' || status === 'canceled' || status === 'aborted') return 'cancelled'
  if (status === 'skipped' || status === 'misfired') return 'skipped'
  if (status === 'blocked' || status === 'denied' || status === 'suppressed') return 'blocked'
  if (status === 'info' || status === 'informational') return 'info'
  return 'unknown'
}

function normalizeActivity(value: unknown): ActivityEvent | undefined {
  if (!value || typeof value !== 'object') return undefined
  const rawValue = value as UnknownRecord
  const source = rawValue.event && typeof rawValue.event === 'object' ? rawValue.event as UnknownRecord : rawValue
  const id = optionalString(source, 'id', 'eventId', 'event_id', 'auditId', 'audit_id')
  if (!id) return undefined
  return {
    id,
    type: normalizeActivityType(source.type ?? source.activityType ?? source.activity_type ?? source.category),
    status: normalizeActivityStatus(source.status ?? source.state ?? source.resultStatus ?? source.result_status),
    title: optionalString(source, 'title', 'name', 'action', 'summary') ?? 'Событие Yuri',
    detail: optionalString(source, 'detail', 'description', 'message', 'result'),
    source: optionalString(source, 'source', 'actor', 'origin'),
    scheduleId: optionalString(source, 'scheduleId', 'schedule_id'),
    runId: optionalString(source, 'runId', 'run_id', 'jobRunId', 'job_run_id'),
    createdAt: optionalString(source, 'createdAt', 'created_at', 'timestamp', 'occurredAt', 'occurred_at') ?? nowIso(),
    durationMs: optionalNumber(source, 'durationMs', 'duration_ms', 'elapsedMs', 'elapsed_ms'),
    reason: optionalString(source, 'reason', 'why'),
    provenance: optionalString(source, 'provenance', 'originDetail', 'origin_detail'),
  }
}

function normalizeActivityList(value: unknown): ActivityEvent[] {
  const rawItems = Array.isArray(value)
    ? value
    : value && typeof value === 'object'
      ? ((value as UnknownRecord).items ?? (value as UnknownRecord).events ?? (value as UnknownRecord).activities ?? (value as UnknownRecord).results)
      : undefined
  return Array.isArray(rawItems)
    ? rawItems.map(normalizeActivity).filter((item): item is ActivityEvent => Boolean(item))
    : []
}

export { normalizeActivityList, normalizePeerDialogueList }
