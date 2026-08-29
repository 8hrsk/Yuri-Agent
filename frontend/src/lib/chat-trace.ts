import type {
  ApprovalRequest,
  ApprovalTraceStatus,
  ApprovalTraceStep,
  ChatEvent,
  CompletionTraceStep,
  RunStatus,
  RunTrace,
  RunTraceStatus,
  RunTraceStep,
  StatusTraceStep,
  ThinkingTraceStep,
  ToolCall,
  ToolRisk,
  ToolStatus,
  ToolTraceStep,
} from './contracts'

type UnknownRecord = Record<string, unknown>

const runStatusLabels: Record<RunStatus, string> = {
  idle: 'Готово',
  thinking: 'Думает…',
  tool_running: 'Выполняет действие…',
  waiting_approval: 'Ожидается разрешение',
  speaking: 'Формирует ответ…',
  cancelled: 'Запуск остановлен',
  error: 'Запуск завершился ошибкой',
}

const toolStatusLabels: Record<ToolStatus, string> = {
  pending: 'Подготовлено',
  running: 'Выполняется',
  completed: 'Завершено',
  failed: 'Ошибка',
  cancelled: 'Остановлено',
  denied: 'Отклонено',
}

const approvalStatusLabels: Record<ApprovalTraceStatus, string> = {
  waiting: 'Ожидает решения',
  approved: 'Разрешено',
  denied: 'Отклонено',
  expired: 'Истёк срок решения',
}

function nowIso(): string {
  return new Date().toISOString()
}

function isRecord(value: unknown): value is UnknownRecord {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value))
}

function stringValue(value: unknown): string | undefined {
  if (typeof value === 'string' || typeof value === 'number') {
    const result = String(value).trim()
    return result || undefined
  }
  return undefined
}

function optionalString(source: UnknownRecord, ...keys: string[]): string | undefined {
  for (const key of keys) {
    const result = stringValue(source[key])
    if (result) return result
  }
  return undefined
}

function normalizeRisk(value: unknown): ToolRisk {
  const risk = String(value ?? '').toLowerCase()
  if (risk === 'medium' || risk === 'high' || risk === 'critical') return risk
  return 'low'
}

function normalizeToolStatus(value: unknown, fallback: ToolStatus): ToolStatus {
  const status = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (status === 'pending' || status === 'queued' || status === 'prepared') return 'pending'
  if (status === 'running' || status === 'started' || status === 'in_progress') return 'running'
  if (status === 'completed' || status === 'complete' || status === 'succeeded' || status === 'success') return 'completed'
  if (status === 'failed' || status === 'error') return 'failed'
  if (status === 'cancelled' || status === 'canceled' || status === 'stopped') return 'cancelled'
  if (status === 'denied' || status === 'rejected' || status === 'forbidden') return 'denied'
  return fallback
}

function cloneArgs(args: Record<string, unknown>): Record<string, unknown> {
  return { ...args }
}

function parseArgs(value: unknown): Record<string, unknown> {
  if (isRecord(value)) return cloneArgs(value)
  if (typeof value === 'string') {
    try {
      const parsed: unknown = JSON.parse(value)
      if (isRecord(parsed)) return cloneArgs(parsed)
    } catch {
      // Redacted/unparseable arguments are represented as an empty object.
    }
  }
  return {}
}

/** Normalize a tool payload from either the mock or a Wails JSON shape. */
export function normalizeToolCall(value: unknown, fallbackStatus: ToolStatus = 'running'): ToolCall | undefined {
  if (!isRecord(value)) return undefined
  const id = optionalString(value, 'id', 'toolCallId', 'tool_call_id', 'callId', 'call_id')
  const name = optionalString(value, 'name', 'toolName', 'tool_name', 'toolId', 'tool_id')
  if (!id || !name) return undefined
  const args = value.args ?? value.arguments ?? value.argumentsJson ?? value.arguments_json ?? value.argsRedacted ?? value.args_redacted ?? {}
  const resultValue = value.result ?? value.output ?? value.content ?? value.error
  return {
    id,
    name,
    label: optionalString(value, 'label', 'displayName', 'display_name') ?? name,
    risk: normalizeRisk(value.risk),
    status: normalizeToolStatus(value.status, fallbackStatus),
    args: parseArgs(args),
    result: resultValue === undefined || resultValue === null ? undefined : String(resultValue),
    startedAt: optionalString(value, 'startedAt', 'started_at', 'createdAt', 'created_at'),
    finishedAt: optionalString(value, 'finishedAt', 'finished_at', 'completedAt', 'completed_at'),
  }
}

/** Normalize an approval payload while keeping its scope explicit. */
export function normalizeApproval(value: unknown): ApprovalRequest | undefined {
  if (!isRecord(value)) return undefined
  const id = optionalString(value, 'id', 'approvalId', 'approval_id')
  const toolCallId = optionalString(value, 'toolCallId', 'tool_call_id', 'callId', 'call_id')
  if (!id || !toolCallId) return undefined
  return {
    id,
    toolCallId,
    title: optionalString(value, 'title', 'subject') ?? 'Требуется подтверждение',
    explanation: optionalString(value, 'explanation', 'reason', 'detail') ?? '',
    risk: normalizeRisk(value.risk),
    scope: optionalString(value, 'scope', 'action', 'target') ?? '',
    expiresAt: optionalString(value, 'expiresAt', 'expires_at'),
  }
}

function eventTime(event: ChatEvent, fallback: string): string {
  return event.createdAt ?? event.timestamp ?? fallback
}

function traceStatusForRunStatus(status: RunStatus): RunTraceStatus {
  if (status === 'waiting_approval') return 'waiting_approval'
  if (status === 'cancelled') return 'cancelled'
  if (status === 'error') return 'error'
  return 'running'
}

function stepTime(step: RunTraceStep): number {
  const parsed = Date.parse(step.createdAt)
  return Number.isFinite(parsed) ? parsed : 0
}

function sortSteps(steps: RunTraceStep[]): RunTraceStep[] {
  return steps
    .map((step, index) => ({ step, index }))
    .sort((left, right) => stepTime(left.step) - stepTime(right.step) || left.index - right.index)
    .map(({ step }) => step)
}

function cloneToolCall(toolCall: ToolCall): ToolCall {
  return {
    ...toolCall,
    args: cloneArgs(toolCall.args),
  }
}

function cloneStep(step: RunTraceStep): RunTraceStep {
  switch (step.kind) {
    case 'thinking':
      return { ...step, label: step.status === 'running' ? 'Обрабатывает запрос…' : 'Обработка завершена' }
    case 'tool':
      return { ...step, toolCall: cloneToolCall(step.toolCall) }
    case 'approval':
      return { ...step, approval: { ...step.approval } }
    default:
      return { ...step }
  }
}

function upsertStep(steps: RunTraceStep[], next: RunTraceStep): RunTraceStep[] {
  const index = steps.findIndex((step) => step.id === next.id)
  if (index === -1) return sortSteps([...steps, cloneStep(next)])
  return sortSteps(steps.map((step, candidateIndex) => candidateIndex === index ? cloneStep(next) : step))
}

function withUpdatedTrace(trace: RunTrace, at: string, patch: Partial<RunTrace>): RunTrace {
  const steps = patch.steps ?? trace.steps
  return {
    ...trace,
    ...patch,
    updatedAt: at,
    steps: steps.map(cloneStep),
  }
}

function thinkingStep(runId: string, at: string, label = 'Обрабатывает запрос…', status: ThinkingTraceStep['status'] = 'running'): ThinkingTraceStep {
  return {
    id: `${runId}:thinking`,
    kind: 'thinking',
    status,
    label,
    createdAt: at,
    finishedAt: status === 'running' ? undefined : at,
  }
}

function completeThinking(trace: RunTrace, at: string, status: ThinkingTraceStep['status'] = 'completed'): RunTrace {
  const steps = trace.steps.map((step) => step.kind === 'thinking' && step.status === 'running'
    ? { ...step, status, finishedAt: at }
    : step)
  return { ...trace, steps }
}

function statusStep(runId: string, eventStatus: RunStatus, at: string, label?: string): StatusTraceStep {
  return {
    id: `${runId}:status:${eventStatus}`,
    kind: 'status',
    status: eventStatus,
    label: label?.trim() || runStatusLabels[eventStatus],
    createdAt: at,
    finishedAt: at,
  }
}

function toolStep(runId: string, toolCall: ToolCall, at: string, originalCreatedAt?: string): ToolTraceStep {
  const createdAt = originalCreatedAt ?? toolCall.startedAt ?? at
  return {
    id: `${runId}:tool:${toolCall.id}`,
    kind: 'tool',
    status: toolCall.status,
    toolCall: cloneToolCall(toolCall),
    createdAt,
    finishedAt: toolCall.finishedAt ?? (toolCall.status === 'running' || toolCall.status === 'pending' ? undefined : at),
  }
}

function approvalStep(runId: string, approval: ApprovalRequest, at: string, status: ApprovalTraceStatus = 'waiting'): ApprovalTraceStep {
  return {
    id: `${runId}:approval:${approval.id}`,
    kind: 'approval',
    status,
    approval: { ...approval },
    createdAt: at,
    finishedAt: status === 'waiting' ? undefined : at,
  }
}

function completionStep(runId: string, status: CompletionTraceStep['status'], at: string, error?: string): CompletionTraceStep {
  const label = status === 'complete'
    ? 'Запуск завершён'
    : status === 'cancelled'
      ? 'Запуск остановлен'
      : error || 'Запуск завершился ошибкой'
  return {
    id: `${runId}:completion`,
    kind: 'completion',
    status,
    label,
    error,
    createdAt: at,
    finishedAt: at,
  }
}

function applyToolUpdate(trace: RunTrace, toolCall: ToolCall, at: string): RunTrace {
  let next = completeThinking(trace, at)
  const previousToolStep = next.steps.find((step): step is ToolTraceStep => step.kind === 'tool' && step.toolCall.id === toolCall.id)
  const toolCalls = [...(next.toolCalls ?? [])]
  const toolCallIndex = toolCalls.findIndex((candidate) => candidate.id === toolCall.id)
  const nextToolCall = cloneToolCall(toolCall)
  if (toolCallIndex === -1) toolCalls.push(nextToolCall)
  else toolCalls[toolCallIndex] = nextToolCall
  next = withUpdatedTrace(next, at, {
    steps: upsertStep(next.steps, toolStep(trace.runId, toolCall, at, previousToolStep?.createdAt)),
    toolCalls,
  })
  const approvalIndex = next.steps.findIndex((step) => step.kind === 'approval' && step.approval.toolCallId === toolCall.id)
  if (approvalIndex !== -1 && toolCall.status !== 'running' && toolCall.status !== 'pending') {
    const approvalStepValue = next.steps[approvalIndex]
    if (approvalStepValue.kind === 'approval') {
      const approvalStatus: ApprovalTraceStatus = toolCall.status === 'completed' ? 'approved' : 'denied'
      const updatedSteps = next.steps.map((step, index): RunTraceStep => {
        if (index !== approvalIndex || step.kind !== 'approval') return step
        return { ...step, status: approvalStatus, finishedAt: at }
      })
      next = withUpdatedTrace(next, at, {
        steps: updatedSteps,
      })
    }
  }
  return next
}

/** Create an empty trace for an event stream that did not include run.started. */
export function createRunTrace(runId: string, startedAt = nowIso()): RunTrace {
  return {
    id: `trace:${runId}`,
    runId,
    status: 'running',
    startedAt,
    updatedAt: startedAt,
    steps: [],
  }
}

/**
 * Reduce one public lifecycle event into a trace. No assistant reasoning or
 * arbitrary event payload is copied into the returned object.
 */
export function reduceRunTrace(trace: RunTrace, event: ChatEvent, at = eventTime(event, nowIso())): RunTrace {
  let next = withUpdatedTrace(trace, at, {})
  switch (event.type) {
    case 'run.started':
      return withUpdatedTrace(next, at, {
        status: 'running',
        startedAt: trace.startedAt || at,
        steps: upsertStep(next.steps, thinkingStep(trace.runId, at)),
      })
    case 'run.status':
      next = withUpdatedTrace(next, at, { status: traceStatusForRunStatus(event.status) })
      if (event.status === 'thinking') {
        next = withUpdatedTrace(next, at, {
          steps: upsertStep(next.steps, thinkingStep(trace.runId, next.startedAt)),
        })
      } else {
        next = completeThinking(next, at)
        next = withUpdatedTrace(next, at, { steps: upsertStep(next.steps, statusStep(trace.runId, event.status, at, event.label)) })
      }
      return next
    case 'tool.started':
      return applyToolUpdate(next, { ...event.toolCall, status: event.toolCall.status || 'running' }, at)
    case 'tool.updated':
      return applyToolUpdate(next, event.toolCall, at)
    case 'approval.required':
      next = completeThinking(next, at)
      return withUpdatedTrace(next, at, {
        status: 'waiting_approval',
        steps: upsertStep(next.steps, approvalStep(trace.runId, event.approval, at)),
      })
    case 'assistant.completed':
      return completeThinking(next, at)
    case 'run.completed':
      next = completeThinking(next, at, event.status === 'complete' ? 'completed' : event.status === 'cancelled' ? 'cancelled' : 'failed')
      return withUpdatedTrace(next, at, {
        status: event.status === 'complete' ? 'complete' : event.status,
        finishedAt: at,
        steps: upsertStep(next.steps, completionStep(trace.runId, event.status, at, event.error)),
      })
    case 'trace.step': {
      const step = cloneStep(event.step)
      const traceStatus = step.kind === 'completion'
        ? step.status === 'complete' ? 'complete' : step.status
        : step.kind === 'approval' && step.status === 'waiting' ? 'waiting_approval' : next.status
      const toolCalls = step.kind === 'tool'
        ? [...(next.toolCalls ?? []).filter((toolCall) => toolCall.id !== step.toolCall.id), cloneToolCall(step.toolCall)]
        : next.toolCalls
      return withUpdatedTrace(next, at, {
        status: traceStatus,
        steps: upsertStep(next.steps, step),
        toolCalls,
        finishedAt: step.kind === 'completion' ? at : next.finishedAt,
      })
    }
  }
  return next
}

/** Aggregate a live event into chronologically ordered conversation traces. */
export function aggregateChatEvent(traces: RunTrace[] = [], event: ChatEvent, at = eventTime(event, nowIso())): RunTrace[] {
  const index = traces.findIndex((trace) => trace.runId === event.runId)
  const current = index === -1 ? createRunTrace(event.runId, at) : traces[index]
  const nextTrace = reduceRunTrace(current, event, at)
  const next = index === -1
    ? [...traces, nextTrace]
    : traces.map((trace, traceIndex) => traceIndex === index ? nextTrace : trace)
  return sortRunTraces(next)
}

function traceTime(trace: RunTrace): number {
  const parsed = Date.parse(trace.startedAt || trace.updatedAt || trace.finishedAt || '')
  return Number.isFinite(parsed) ? parsed : 0
}

/** Stable chronological ordering for historical and live traces. */
export function sortRunTraces(traces: RunTrace[]): RunTrace[] {
  return traces
    .map((trace, index) => ({ trace, index }))
    .sort((left, right) => traceTime(left.trace) - traceTime(right.trace) || left.index - right.index)
    .map(({ trace }) => trace)
}

export function normalizeRunTraceStep(value: unknown, index: number, runId: string, fallbackAt: string): RunTraceStep | undefined {
  if (!isRecord(value)) return undefined
  const kindValue = String(value.kind ?? value.type ?? '').toLowerCase().replace(/[-\s]/g, '_')
  const id = optionalString(value, 'id', 'stepId', 'step_id') ?? `${runId}:step:${index}`
  const createdAt = optionalString(value, 'createdAt', 'created_at', 'startedAt', 'started_at', 'timestamp') ?? fallbackAt
  const finishedAt = optionalString(value, 'finishedAt', 'finished_at', 'completedAt', 'completed_at')
  if (kindValue === 'thinking' || kindValue === 'thought' || kindValue === 'reasoning' || kindValue === 'run_started') {
    const statusValue = String(value.status ?? '').toLowerCase()
    const status: ThinkingTraceStep['status'] = statusValue === 'failed' || statusValue === 'error'
      ? 'failed'
      : statusValue === 'cancelled' || statusValue === 'canceled'
        ? 'cancelled'
        : statusValue === 'completed' || statusValue === 'complete' || finishedAt
          ? 'completed'
          : 'running'
    return {
      id,
      kind: 'thinking',
      status,
      // Never persist/render a provider-supplied thought or explanation.
      label: status === 'running' ? 'Обрабатывает запрос…' : 'Обработка завершена',
      createdAt,
      finishedAt,
    }
  }
  if (kindValue === 'status' || kindValue === 'run_status' || kindValue === 'run.status') {
    const rawStatus = String(value.status ?? '').toLowerCase()
    const status: RunStatus = rawStatus === 'thinking' || rawStatus === 'tool_running' || rawStatus === 'waiting_approval' || rawStatus === 'speaking' || rawStatus === 'idle' || rawStatus === 'cancelled' || rawStatus === 'error'
      ? rawStatus
      : 'thinking'
    return { id, kind: 'status', status, label: optionalString(value, 'label', 'title') ?? runStatusLabels[status], createdAt, finishedAt }
  }
  if (kindValue === 'tool' || kindValue === 'tool_call' || kindValue === 'toolcall' || kindValue === 'tool_started' || kindValue === 'tool_updated' || kindValue === 'tool_completed' || kindValue === 'tool_result' || kindValue === 'tool.started' || kindValue === 'tool.updated' || kindValue === 'tool.completed' || kindValue === 'tool.result') {
    const completedKind = kindValue === 'tool_updated' || kindValue === 'tool_completed' || kindValue === 'tool_result' || kindValue === 'tool.updated' || kindValue === 'tool.completed' || kindValue === 'tool.result'
    const toolCall = normalizeToolCall(value.toolCall ?? value.tool_call ?? value.call ?? value, normalizeToolStatus(value.status, completedKind ? 'completed' : 'running'))
    if (!toolCall) return undefined
    return { id, kind: 'tool', status: toolCall.status, toolCall, createdAt, finishedAt: finishedAt ?? toolCall.finishedAt }
  }
  if (kindValue === 'approval' || kindValue === 'approval_required' || kindValue === 'approval.required') {
    const approval = normalizeApproval(value.approval ?? value)
    if (!approval) return undefined
    const rawStatus = String(value.status ?? '').toLowerCase()
    const status: ApprovalTraceStatus = rawStatus === 'approved' || rawStatus === 'allowed'
      ? 'approved'
      : rawStatus === 'denied' || rawStatus === 'rejected'
        ? 'denied'
        : rawStatus === 'expired'
          ? 'expired'
          : 'waiting'
    return { id, kind: 'approval', status, approval, createdAt, finishedAt }
  }
  if (kindValue === 'completion' || kindValue === 'completed' || kindValue === 'run_completed' || kindValue === 'run.completed' || kindValue === 'error') {
    const rawStatus = String(value.status ?? (kindValue === 'error' ? 'error' : 'complete')).toLowerCase()
    const status: CompletionTraceStep['status'] = rawStatus === 'cancelled' || rawStatus === 'canceled' ? 'cancelled' : rawStatus === 'error' || kindValue === 'error' ? 'error' : 'complete'
    const error = optionalString(value, 'error', 'message', 'detail')
    return { id, kind: 'completion', status, label: optionalString(value, 'label', 'title') ?? (status === 'error' ? error ?? 'Запуск завершился ошибкой' : status === 'cancelled' ? 'Запуск остановлен' : 'Запуск завершён'), error, createdAt, finishedAt: finishedAt ?? createdAt }
  }
  return undefined
}

/** Decode persisted traces without ever retaining hidden reasoning fields. */
export function normalizeRunTrace(value: unknown, fallbackIndex = 0): RunTrace | undefined {
  if (!isRecord(value)) return undefined
  const runId = optionalString(value, 'runId', 'run_id', 'id')
  if (!runId) return undefined
  const startedAt = optionalString(value, 'startedAt', 'started_at', 'createdAt', 'created_at') ?? nowIso()
  const rawStatus = String(value.status ?? '').toLowerCase().replace(/[-\s]/g, '_')
  const status: RunTraceStatus = rawStatus === 'queued' || rawStatus === 'pending' ? 'queued' : rawStatus === 'waiting_approval' || rawStatus === 'waiting' || rawStatus === 'approval_required' ? 'waiting_approval' : rawStatus === 'complete' || rawStatus === 'completed' || rawStatus === 'idle' ? 'complete' : rawStatus === 'cancelled' || rawStatus === 'canceled' ? 'cancelled' : rawStatus === 'error' || rawStatus === 'failed' ? 'error' : 'running'
  const finishedAt = optionalString(value, 'finishedAt', 'finished_at', 'completedAt', 'completed_at')
  const failure = optionalString(value, 'failure', 'error', 'lastError', 'last_error')
  const rawSteps = value.steps ?? value.events ?? value.items
  let steps = Array.isArray(rawSteps)
    ? rawSteps.map((step, index) => normalizeRunTraceStep(step, index, runId, startedAt)).filter((step): step is RunTraceStep => Boolean(step))
    : []
  const rawToolCallsValue = value.toolCalls ?? value.tool_calls
  const rawToolCalls: unknown[] = Array.isArray(rawToolCallsValue) ? rawToolCallsValue : []
  const toolCalls = rawToolCalls
    .map((toolCall) => normalizeToolCall(toolCall, status === 'error' ? 'failed' : status === 'complete' ? 'completed' : 'running'))
    .filter((toolCall): toolCall is ToolCall => Boolean(toolCall))
  // The first durable backend shape stores a compact trace with toolCalls but
  // no nested steps. Expand it into the renderer contract so old and new
  // history render identically.
  if (steps.length === 0) {
    const terminalStatus: ThinkingTraceStep['status'] = status === 'complete'
      ? 'completed'
      : status === 'cancelled'
        ? 'cancelled'
        : status === 'error'
          ? 'failed'
          : status === 'waiting_approval'
            ? 'completed'
            : 'running'
    steps = [thinkingStep(runId, startedAt, terminalStatus === 'running' ? 'Обрабатывает запрос…' : 'Обработал запрос', terminalStatus)]
    for (const toolCall of toolCalls) steps.push(toolStep(runId, toolCall, toolCall.startedAt ?? startedAt))
    if (status === 'complete' || status === 'cancelled' || status === 'error') {
      steps.push(completionStep(runId, status, finishedAt ?? startedAt, failure))
    }
  }
  return {
    id: optionalString(value, 'id', 'traceId', 'trace_id') ?? `trace:${runId}:${fallbackIndex}`,
    runId,
    status,
    startedAt,
    updatedAt: optionalString(value, 'updatedAt', 'updated_at', 'timestamp'),
    finishedAt,
    kind: optionalString(value, 'kind', 'runKind', 'run_kind'),
    failure,
    toolCalls: toolCalls.length > 0 ? toolCalls : undefined,
    steps: sortSteps(steps),
  }
}

export function toolStatusLabel(status: ToolStatus): string {
  return toolStatusLabels[status]
}

export function approvalStatusLabel(status: ApprovalTraceStatus): string {
  return approvalStatusLabels[status]
}

export function runStatusLabel(status: RunStatus): string {
  return runStatusLabels[status]
}
