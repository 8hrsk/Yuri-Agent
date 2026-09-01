import type {
  ActivityEvent,
  ActivityStatus,
  ActivityType,
  PeerDialogueCompletionReason,
  PeerDialogue,
  AppliedPeerBudgetRecommendation,
  PeerDialogueMessage,
  PeerDialogueStatus,
  PeerDialogueTriggerKind,
  PeerRelationship,
  PeerRelationshipDetail,
  PeerRelationshipOperation,
  PeerRelationshipVersion,
} from '../contracts'
import { normalizeRunFailureKind } from '../chat-trace'
import { clampPersonaValue, normalizePersonaEvidence, normalizeSubjectiveOpinions } from '../personality'
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

function normalizePeerDialogueTriggerKind(value: unknown): PeerDialogueTriggerKind {
  const kind = String(value ?? '').trim().toLowerCase()
  if (kind === 'agent_tool' || kind === 'tool' || kind === 'manual') return 'agent_tool'
  if (kind === 'autonomous' || kind === 'automatic' || kind === 'background') return 'autonomous'
  return 'unknown'
}

function normalizePeerDialogueCompletionReason(value: unknown): PeerDialogueCompletionReason | undefined {
  if (value === undefined || value === null || String(value).trim() === '') return undefined
  const reason = String(value).trim().toLowerCase().replace(/[-\s]/g, '_')
  if (reason === 'semantic' || reason === 'semantic_completion' || reason === 'agent_complete' || reason === 'model_complete') return 'semantic'
  if (reason === 'implicit' || reason === 'implicit_completion' || reason === 'minimum_turns' || reason === 'min_turns') return 'implicit'
  if (reason === 'max_turns' || reason === 'turn_limit' || reason === 'turns_limit' || reason === 'turn_budget') return 'max_turns'
  if (reason === 'max_tokens' || reason === 'token_limit' || reason === 'tokens_limit' || reason === 'token_budget') return 'max_tokens'
  if (reason === 'max_duration' || reason === 'max_duration_seconds' || reason === 'duration_limit' || reason === 'deadline' || reason === 'timeout' || reason === 'timed_out') return 'max_duration'
  if (reason === 'cancelled' || reason === 'canceled' || reason === 'aborted') return 'cancelled'
  if (reason === 'failed' || reason === 'failure' || reason === 'error') return 'failed'
  return 'unknown'
}

function normalizeAppliedRecommendation(value: unknown): AppliedPeerBudgetRecommendation | undefined {
  if (!value || typeof value !== 'object') return undefined
  const source = value as UnknownRecord
  const basis = source.basis === 'similar_history' || source.basis === 'pair_history' ? source.basis : source.basis === 'purpose_only' ? 'purpose_only' : undefined
  if (!basis) return undefined
  const maxTurns = Math.max(1, Math.round(optionalNumber(source, 'maxTurns', 'max_turns') ?? 0))
  const minTurns = Math.min(maxTurns, Math.max(1, Math.round(optionalNumber(source, 'minTurns', 'min_turns') ?? 1)))
  const confidence = source.confidence === 'high' || source.confidence === 'medium' ? source.confidence : 'low'
  return {
    minTurns, maxTurns,
    maxTokens: Math.max(1, Math.round(optionalNumber(source, 'maxTokens', 'max_tokens') ?? 0)),
    maxDurationSeconds: Math.max(5, Math.round(optionalNumber(source, 'maxDurationSeconds', 'max_duration_seconds') ?? 0)),
    basis, confidence,
    sampleCount: Math.max(0, Math.round(optionalNumber(source, 'sampleCount', 'sample_count') ?? 0)),
  }
}

function normalizePeerDialogueMessage(value: unknown): PeerDialogueMessage | undefined {
  if (!value || typeof value !== 'object') return undefined
  const raw = value as UnknownRecord
  const id = optionalString(raw, 'id', 'messageId', 'message_id')
  if (!id) return undefined
  const sourceRunId = optionalString(raw, 'sourceRunId', 'source_run_id')
  const providerId = optionalString(raw, 'providerId', 'provider_id')
  const model = optionalString(raw, 'model')
  const inputTokens = Math.max(0, Math.round(optionalNumber(raw, 'inputTokens', 'input_tokens') ?? 0)) || undefined
  const outputTokens = Math.max(0, Math.round(optionalNumber(raw, 'outputTokens', 'output_tokens') ?? 0)) || undefined
  const totalTokens = Math.max(0, Math.round(optionalNumber(raw, 'totalTokens', 'total_tokens') ?? 0)) || undefined
  return {
    id,
    sequence: Math.max(0, Math.round(optionalNumber(raw, 'sequence', 'index', 'turn') ?? 0)),
    ...(sourceRunId ? { sourceRunId } : {}),
    senderAgentId: optionalString(raw, 'senderAgentId', 'sender_agent_id', 'senderId', 'sender_id') ?? '',
    senderName: optionalString(raw, 'senderName', 'sender_name') ?? 'Агент',
    recipientAgentId: optionalString(raw, 'recipientAgentId', 'recipient_agent_id', 'recipientId', 'recipient_id') ?? '',
    recipientName: optionalString(raw, 'recipientName', 'recipient_name') ?? 'Агент',
    content: String(raw.content ?? raw.text ?? raw.message ?? ''),
    createdAt: optionalString(raw, 'createdAt', 'created_at', 'timestamp') ?? nowIso(),
    ...(providerId ? { providerId } : {}),
    ...(model ? { model } : {}),
    ...(inputTokens !== undefined ? { inputTokens } : {}),
    ...(outputTokens !== undefined ? { outputTokens } : {}),
    ...(totalTokens !== undefined ? { totalTokens } : {}),
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
  const initiatorProviderId = optionalString(source, 'initiatorProviderId', 'initiator_provider_id')
  const initiatorModel = optionalString(source, 'initiatorModel', 'initiator_model')
  const peerProviderId = optionalString(source, 'peerProviderId', 'peer_provider_id')
  const peerModel = optionalString(source, 'peerModel', 'peer_model')
  const budget = source.budget && typeof source.budget === 'object' && !Array.isArray(source.budget)
    ? source.budget as UnknownRecord
    : undefined
  const policy = source.policy && typeof source.policy === 'object' && !Array.isArray(source.policy)
    ? source.policy as UnknownRecord
    : undefined
  const budgetNumber = (...keys: string[]): number | undefined =>
    optionalNumber(source, ...keys) ?? optionalNumber(budget ?? {}, ...keys) ?? optionalNumber(policy ?? {}, ...keys)
  const failureKind = normalizeRunFailureKind(source.failureKind ?? source.failure_kind)
  const retryableValue = source.retryable ?? source.failureRetryable ?? source.failure_retryable
  const retryAfterSeconds = Math.max(0, Math.round(optionalNumber(source, 'retryAfterSeconds', 'retry_after_seconds', 'failureRetryAfterSeconds', 'failure_retry_after_seconds') ?? 0)) || undefined
  const maxTurns = Math.max(0, Math.round(budgetNumber('maxTurns', 'max_turns', 'turnLimit', 'turn_limit') ?? 0))
  const minTurns = Math.max(0, Math.round(budgetNumber('minTurns', 'min_turns', 'minimumTurns', 'minimum_turns', 'minTurnCount', 'min_turn_count') ?? (maxTurns > 0 ? Math.min(1, maxTurns) : 0)))
  const completionReason = normalizePeerDialogueCompletionReason(source.completionReason ?? source.completion_reason ?? source.stopReason ?? source.stop_reason ?? source.terminationReason ?? source.termination_reason)
  const rawBudgetOrigin = optionalString(source, 'budgetOrigin', 'budget_origin')
  const budgetOrigin = rawBudgetOrigin === 'owner_recommendation' || rawBudgetOrigin === 'owner_custom' ? rawBudgetOrigin : 'agent_default'
  const recommendation = normalizeAppliedRecommendation(source.recommendation)
  return {
    id,
    initiatorAgentId,
    initiatorName: optionalString(source, 'initiatorName', 'initiator_name') ?? 'Агент',
    ...(initiatorProviderId ? { initiatorProviderId } : {}),
    ...(initiatorModel ? { initiatorModel } : {}),
    peerAgentId,
    peerName: optionalString(source, 'peerName', 'peer_name') ?? 'Агент',
    ...(peerProviderId ? { peerProviderId } : {}),
    ...(peerModel ? { peerModel } : {}),
    triggerKind: normalizePeerDialogueTriggerKind(source.triggerKind ?? source.trigger_kind),
    triggerReason: optionalString(source, 'triggerReason', 'trigger_reason') ?? 'Причина запуска не указана.',
    purpose: optionalString(source, 'purpose', 'goal', 'summary') ?? 'Внутренний диалог',
    status: normalizePeerDialogueStatus(source.status ?? source.state),
    turnCount: Math.max(0, Math.round(optionalNumber(source, 'turnCount', 'turn_count', 'turns') ?? messages.length)),
    minTurns,
    maxTurns,
    tokensUsed: Math.max(0, Math.round(optionalNumber(source, 'tokensUsed', 'tokens_used', 'usedTokens', 'used_tokens') ?? 0)),
    maxTokens: Math.max(0, Math.round(budgetNumber('maxTokens', 'max_tokens', 'tokenLimit', 'token_limit') ?? 0)),
    maxDurationSeconds: Math.max(0, Math.round(budgetNumber('maxDurationSeconds', 'max_duration_seconds', 'durationLimit', 'duration_limit') ?? 0)),
    durationUsedSeconds: Math.max(0, Math.round(optionalNumber(source, 'durationUsedSeconds', 'duration_used_seconds') ?? 0)),
    cooldownSeconds: Math.max(0, Math.round(budgetNumber('cooldownSeconds', 'cooldown_seconds', 'cooldown') ?? 0)),
    budgetOrigin,
    ...(budgetOrigin === 'owner_recommendation' && recommendation ? { recommendation } : {}),
    ...(completionReason ? { completionReason } : {}),
    createdAt: optionalString(source, 'createdAt', 'created_at', 'timestamp') ?? nowIso(),
    finishedAt: optionalString(source, 'finishedAt', 'finished_at', 'completedAt', 'completed_at'),
    failure: optionalString(source, 'failure', 'error', 'lastError', 'last_error'),
    ...(failureKind ? { failureKind } : {}),
    ...(typeof retryableValue === 'boolean' ? { retryable: retryableValue } : {}),
    ...(retryAfterSeconds !== undefined ? { retryAfterSeconds } : {}),
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

function normalizeDimensions(value: unknown): Record<string, number> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  return Object.fromEntries(Object.entries(value as UnknownRecord).flatMap(([key, raw]) => {
    const numeric = Number(raw)
    return Number.isFinite(numeric) ? [[key, clampPersonaValue(numeric)]] : []
  }))
}

function normalizePeerRelationshipOperation(value: unknown): PeerRelationshipOperation {
  const operation = String(value ?? '').trim().toLowerCase()
  if (operation === 'create' || operation === 'update' || operation === 'rollback' || operation === 'reset') return operation
  return 'unknown'
}

function normalizePeerRelationship(value: unknown): PeerRelationship | undefined {
  if (!value || typeof value !== 'object') return undefined
  const rawValue = value as UnknownRecord
  const source = rawValue.relationship && typeof rawValue.relationship === 'object'
    ? rawValue.relationship as UnknownRecord
    : rawValue
  const observerAgentId = optionalString(source, 'observerAgentId', 'observer_agent_id')
  const peerAgentId = optionalString(source, 'peerAgentId', 'peer_agent_id')
  const relationshipId = optionalString(source, 'relationshipId', 'relationship_id', 'id')
  if (!observerAgentId || !peerAgentId || !relationshipId) return undefined
  return {
    observerAgentId,
    peerAgentId,
    peerName: optionalString(source, 'peerName', 'peer_name') ?? 'Агент',
    relationshipId,
    version: Math.max(1, Math.round(optionalNumber(source, 'version') ?? 1)),
    currentVersionId: optionalString(source, 'currentVersionId', 'current_version_id', 'revisionId', 'revision_id') ?? '',
    summary: optionalString(source, 'summary') ?? 'Мнение ещё не сформировано.',
    dimensions: normalizeDimensions(source.dimensions),
    opinions: normalizeSubjectiveOpinions(source.opinions),
    reason: optionalString(source, 'reason'),
    evidence: normalizePersonaEvidence(source.evidence),
    updatedAt: optionalString(source, 'updatedAt', 'updated_at') ?? nowIso(),
  }
}

function normalizePeerRelationshipVersion(value: unknown): PeerRelationshipVersion | undefined {
  if (!value || typeof value !== 'object') return undefined
  const source = value as UnknownRecord
  const id = optionalString(source, 'id', 'versionId', 'version_id', 'revisionId', 'revision_id')
  if (!id) return undefined
  return {
    id,
    version: Math.max(1, Math.round(optionalNumber(source, 'version') ?? 1)),
    parentId: optionalString(source, 'parentId', 'parent_id'),
    operation: normalizePeerRelationshipOperation(source.operation),
    summary: optionalString(source, 'summary') ?? 'Мнение ещё не сформировано.',
    dimensions: normalizeDimensions(source.dimensions),
    opinions: normalizeSubjectiveOpinions(source.opinions),
    reason: optionalString(source, 'reason') ?? 'Версия отношения',
    evidence: normalizePersonaEvidence(source.evidence),
    authorRunId: optionalString(source, 'authorRunId', 'author_run_id'),
    createdAt: optionalString(source, 'createdAt', 'created_at') ?? nowIso(),
  }
}

function normalizePeerRelationshipList(value: unknown): PeerRelationship[] {
  const rawItems = Array.isArray(value)
    ? value
    : value && typeof value === 'object'
      ? ((value as UnknownRecord).items ?? (value as UnknownRecord).relationships ?? (value as UnknownRecord).results)
      : undefined
  return Array.isArray(rawItems)
    ? rawItems.map(normalizePeerRelationship).filter((item): item is PeerRelationship => Boolean(item))
    : []
}

function normalizePeerRelationshipDetail(value: unknown): PeerRelationshipDetail | undefined {
  if (!value || typeof value !== 'object') return undefined
  const source = value as UnknownRecord
  const relationship = normalizePeerRelationship(source.relationship ?? source.current ?? source)
  if (!relationship) return undefined
  const rawVersions = source.versions ?? source.history
  const versions = Array.isArray(rawVersions)
    ? rawVersions.map(normalizePeerRelationshipVersion).filter((item): item is PeerRelationshipVersion => Boolean(item))
    : []
  return { relationship, versions }
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

function normalizeActivityLayer(value: unknown): ActivityEvent['layer'] {
  const layer = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (['owner_seed', 'mutable_persona', 'relationship', 'opinion', 'affect', 'memory', 'policy', 'task', 'system'].includes(layer)) return layer as NonNullable<ActivityEvent['layer']>
  return layer ? 'unknown' : undefined
}

function normalizeActivity(value: unknown): ActivityEvent | undefined {
  if (!value || typeof value !== 'object') return undefined
  const rawValue = value as UnknownRecord
  const source = rawValue.event && typeof rawValue.event === 'object' ? rawValue.event as UnknownRecord : rawValue
  const id = optionalString(source, 'id', 'eventId', 'event_id', 'auditId', 'audit_id')
  if (!id) return undefined
  const rawChanges = source.changes ?? source.diff ?? source.deltas
  const changes = Array.isArray(rawChanges) ? rawChanges.flatMap((item) => {
    if (!item || typeof item !== 'object') return []
    const change = item as UnknownRecord
    const key = optionalString(change, 'key', 'id', 'name')
    const delta = optionalNumber(change, 'delta', 'change', 'value')
    return key && delta !== undefined && Number.isFinite(delta) ? [{ key, delta }] : []
  }) : []
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
    layer: normalizeActivityLayer(source.layer ?? source.personalityLayer ?? source.personality_layer),
    operation: optionalString(source, 'operation', 'op'),
    version: optionalNumber(source, 'version', 'revisionVersion', 'revision_version'),
    evidenceCount: optionalNumber(source, 'evidenceCount', 'evidence_count'),
    changes,
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

export {
  normalizeActivityList,
  normalizePeerDialogueList,
  normalizePeerRelationshipDetail,
  normalizePeerRelationshipList,
}
