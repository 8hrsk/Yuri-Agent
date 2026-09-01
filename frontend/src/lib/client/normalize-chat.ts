import type { ChatEvent, ChatTool, RunFailureKind } from '../contracts'
import { normalizeApproval, normalizeRunFailureKind, normalizeRunFallback, normalizeRunTraceStep, normalizeToolCall } from '../chat-trace'
import { normalizeStringList } from './normalize-plugins'
import { normalizeBoolean, nowIso, optionalNumber, optionalString } from './primitives'
import type { UnknownRecord } from './primitives'

function normalizeChatEvent(value: unknown): ChatEvent | undefined {
  if (!value || typeof value !== 'object') return undefined
  const source = value as UnknownRecord
  const nested = source.data && typeof source.data === 'object' ? source.data as UnknownRecord : source
  const rawType = String(nested.type ?? nested.eventType ?? nested.event_type ?? '').toLowerCase().replace(/[-\s]/g, '_')
  const type = rawType === 'run_start' || rawType === 'run_started' ? 'run.started'
    : rawType === 'run_finish' || rawType === 'run_finished' || rawType === 'run_complete' || rawType === 'run_completed' ? 'run.completed'
      : rawType === 'tool_start' || rawType === 'tool_started' ? 'tool.started'
        : rawType === 'tool_update' || rawType === 'tool_updated' ? 'tool.updated'
          : rawType === 'tool_complete' || rawType === 'tool_completed' || rawType === 'tool_result' || rawType === 'tool_finished' ? 'tool.updated'
              : rawType === 'approval_waiting' || rawType === 'approval_required' ? 'approval.required'
          : rawType === 'run.fallback' || rawType === 'run_fallback' || rawType === 'fallback' ? 'run.fallback'
          : rawType === 'run_step' || rawType === 'trace_step' ? 'trace.step'
            : rawType === 'status' ? 'run.status'
              : rawType === 'thinking' || rawType === 'thinking_started' ? 'run.status'
                : rawType
  const runId = String(nested.runId ?? nested.run_id ?? '')
  if (!type || !runId) return undefined
  const base: { runId: string; conversationId?: string; createdAt?: string; timestamp?: string; runKind?: string; parentRunId?: string; providerId?: string; model?: string; inputTokens?: number; outputTokens?: number; totalTokens?: number; maxSteps?: number; maxTokens?: number; maxToolCalls?: number; maxDurationSeconds?: number; failureKind?: RunFailureKind; retryable?: boolean; retryAfterSeconds?: number } = { runId }
  const conversationId = optionalString(nested, 'conversationId', 'conversation_id')
  const createdAt = optionalString(nested, 'createdAt', 'created_at')
  const timestamp = optionalString(nested, 'timestamp', 'at')
  const runKind = optionalString(nested, 'runKind', 'run_kind', 'kind')
  const parentRunId = optionalString(nested, 'parentRunId', 'parent_run_id')
  const providerId = optionalString(nested, 'providerId', 'provider_id')
  const model = optionalString(nested, 'model')
  const inputTokens = optionalNumber(nested, 'inputTokens', 'input_tokens')
  const outputTokens = optionalNumber(nested, 'outputTokens', 'output_tokens')
  const totalTokens = optionalNumber(nested, 'totalTokens', 'total_tokens')
  const maxSteps = optionalNumber(nested, 'maxSteps', 'max_steps')
  const maxTokens = optionalNumber(nested, 'maxTokens', 'max_tokens')
  const maxToolCalls = optionalNumber(nested, 'maxToolCalls', 'max_tool_calls')
  const maxDurationSeconds = optionalNumber(nested, 'maxDurationSeconds', 'max_duration_seconds')
  const failureKind = normalizeRunFailureKind(nested.failureKind ?? nested.failure_kind)
  const retryableValue = nested.retryable ?? nested.failureRetryable ?? nested.failure_retryable
  const retryAfterSeconds = optionalNumber(nested, 'retryAfterSeconds', 'retry_after_seconds', 'failureRetryAfterSeconds', 'failure_retry_after_seconds')
  if (conversationId) base.conversationId = conversationId
  if (createdAt) base.createdAt = createdAt
  if (timestamp) base.timestamp = timestamp
  if (runKind) base.runKind = runKind
  if (parentRunId) base.parentRunId = parentRunId
  if (providerId) base.providerId = providerId
  if (model) base.model = model
  if (inputTokens !== undefined && inputTokens >= 0) base.inputTokens = Math.round(inputTokens)
  if (outputTokens !== undefined && outputTokens >= 0) base.outputTokens = Math.round(outputTokens)
  if (totalTokens !== undefined && totalTokens >= 0) base.totalTokens = Math.round(totalTokens)
  if (maxSteps !== undefined && maxSteps >= 0) base.maxSteps = Math.round(maxSteps)
  if (maxTokens !== undefined && maxTokens >= 0) base.maxTokens = Math.round(maxTokens)
  if (maxToolCalls !== undefined && maxToolCalls >= 0) base.maxToolCalls = Math.round(maxToolCalls)
  if (maxDurationSeconds !== undefined && maxDurationSeconds >= 0) base.maxDurationSeconds = Math.round(maxDurationSeconds)
  if (failureKind) base.failureKind = failureKind
  if (typeof retryableValue === 'boolean') base.retryable = retryableValue
  if (retryAfterSeconds !== undefined && retryAfterSeconds >= 0) base.retryAfterSeconds = Math.round(retryAfterSeconds)
  switch (type) {
    case 'run.started':
      return { type, ...base }
    case 'assistant.delta':
      return { type, ...base, messageId: String(nested.messageId ?? nested.message_id ?? ''), delta: String(nested.delta ?? nested.text ?? '') }
    case 'assistant.completed':
      return { type, ...base, messageId: String(nested.messageId ?? nested.message_id ?? '') }
    case 'tool.started':
    case 'tool.updated': {
      const rawTool = nested.toolCall ?? nested.tool_call ?? nested.call ?? nested
      const toolCall = normalizeToolCall(rawTool, type === 'tool.updated' ? 'completed' : 'running')
      if (!toolCall) return undefined
      return { type, ...base, toolCall }
    }
    case 'approval.required': {
      const approval = normalizeApproval(nested.approval ?? nested)
      return approval ? { type, ...base, approval } : undefined
    }
    case 'run.status': {
      const requestedStatus = type === 'run.status' && (rawType === 'thinking' || rawType === 'thinking_started') ? 'thinking' : nested.status
      const status = requestedStatus
      if (status !== 'thinking' && status !== 'tool_running' && status !== 'waiting_approval' && status !== 'speaking' && status !== 'idle' && status !== 'cancelled' && status !== 'error') return undefined
      return { type, ...base, status, label: status === 'thinking' ? 'Обрабатывает запрос…' : String(nested.label ?? '') }
    }
    case 'run.completed': {
      const rawStatus = String(nested.status ?? '').toLowerCase()
      const status = rawStatus === 'cancelled' || rawStatus === 'canceled'
        ? 'cancelled'
        : rawStatus === 'error' || rawStatus === 'failed'
          ? 'error'
          : 'complete'
      return { type, ...base, status, error: nested.error ? String(nested.error) : undefined }
    }
    case 'run.fallback': {
      const fallback = normalizeRunFallback(nested)
      return fallback ? { type, ...base, ...fallback } : undefined
    }
    case 'trace.step': {
      const step = normalizeRunTraceStep(nested.step, 0, runId, createdAt ?? timestamp ?? nowIso())
      return step ? { type, ...base, step } : undefined
    }
    default:
      return undefined
  }
}

function normalizeChatTool(value: unknown): ChatTool | undefined {
  if (!value || typeof value !== 'object') return undefined
  const raw = value as UnknownRecord
  const source = raw.tool && typeof raw.tool === 'object' ? raw.tool as UnknownRecord : raw
  const name = optionalString(source, 'name', 'toolName', 'tool_name', 'id', 'toolId', 'tool_id')
  if (!name) return undefined
  const riskValue = String(source.risk ?? '').toLowerCase()
  const risk = riskValue === 'medium' || riskValue === 'high' || riskValue === 'critical' ? riskValue : 'low'
  const availability = source.available ?? source.enabled ?? source.isAvailable ?? source.is_available
  const requiresApproval = source.requiresApproval ?? source.requires_approval
  return {
    id: optionalString(source, 'id', 'toolId', 'tool_id') ?? name,
    name,
    label: optionalString(source, 'label', 'displayName', 'display_name', 'title') ?? name,
    description: optionalString(source, 'description', 'detail'),
    risk,
    available: availability === undefined ? true : normalizeBoolean(availability, true),
    requiresApproval: requiresApproval === undefined ? risk === 'medium' || risk === 'high' || risk === 'critical' : normalizeBoolean(requiresApproval, false),
    capabilities: normalizeStringList(source.capabilities),
  }
}

function normalizeChatToolList(value: unknown): ChatTool[] {
  const rawItems = Array.isArray(value)
    ? value
    : value && typeof value === 'object'
      ? ((value as UnknownRecord).tools ?? (value as UnknownRecord).items ?? (value as UnknownRecord).results)
      : undefined
  return Array.isArray(rawItems)
    ? rawItems.map(normalizeChatTool).filter((tool): tool is ChatTool => Boolean(tool))
    : []
}

export { normalizeChatEvent, normalizeChatToolList }
