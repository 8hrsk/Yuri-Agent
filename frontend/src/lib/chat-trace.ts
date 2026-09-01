import type {
  ApprovalRequest,
  ApprovalTraceStatus,
  ApprovalTraceStep,
  ChatEvent,
  ChatMessage,
  CompletionTraceStep,
  RunStatus,
  RunFailureKind,
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

function nonNegativeInteger(source: UnknownRecord, ...keys: string[]): number | undefined {
  for (const key of keys) {
    const numeric = Number(source[key])
    if (source[key] !== undefined && source[key] !== null && source[key] !== '' && Number.isFinite(numeric) && numeric >= 0) {
      return Math.round(numeric)
    }
  }
  return undefined
}

export function normalizeRunFailureKind(value: unknown): RunFailureKind | undefined {
  const kind = String(value ?? '').trim().toLowerCase()
  if (kind === 'unknown' || kind === 'authentication' || kind === 'rate_limit' || kind === 'quota_exhausted' ||
      kind === 'context_limit' || kind === 'model_unavailable' || kind === 'timeout' || kind === 'transient' ||
      kind === 'invalid_request' || kind === 'budget_exceeded') return kind
  return undefined
}

function optionalBoolean(source: UnknownRecord, ...keys: string[]): boolean | undefined {
  for (const key of keys) {
    const value = source[key]
    if (typeof value === 'boolean') return value
    if (value === 1 || value === '1' || value === 'true') return true
    if (value === 0 || value === '0' || value === 'false') return false
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
    kind: optionalString(value, 'kind') === 'filesystem_access' ? 'filesystem_access' : 'action',
    path: optionalString(value, 'path'),
    permissionRoot: optionalString(value, 'permissionRoot', 'permission_root'),
    canRemember: value.canRemember === true || value.can_remember === true,
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

// Steps and traces are treated as immutable throughout this module, so their
// parsed timestamps can be memoized by object identity. Without this the
// comparators re-ran `Date.parse` O(n log n) times per event (M-43).
const stepTimes = new WeakMap<RunTraceStep, number>()
const traceTimes = new WeakMap<RunTrace, number>()

function stepTime(step: RunTraceStep): number {
  const cached = stepTimes.get(step)
  if (cached !== undefined) return cached
  const parsed = Date.parse(step.createdAt)
  const value = Number.isFinite(parsed) ? parsed : 0
  stepTimes.set(step, value)
  return value
}

function sortSteps(steps: RunTraceStep[]): RunTraceStep[] {
  return steps
    .map((step, index) => ({ step, index }))
    .sort((left, right) => stepTime(left.step) - stepTime(right.step) || left.index - right.index)
    .map(({ step }) => step)
}

/** True when `sortSteps` would return the array unchanged. */
function stepsOrdered(steps: RunTraceStep[]): boolean {
  for (let index = 1; index < steps.length; index += 1) {
    if (stepTime(steps[index - 1]) > stepTime(steps[index])) return false
  }
  return true
}

/** True when `sortRunTraces` would return the array unchanged. */
function tracesOrdered(traces: RunTrace[]): boolean {
  for (let index = 1; index < traces.length; index += 1) {
    if (traceTime(traces[index - 1]) > traceTime(traces[index])) return false
  }
  return true
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

/**
 * Re-apply the label invariant a `cloneStep` pass used to enforce, without the
 * deep copy. Steps are never mutated in place, so an already-correct step can
 * be shared instead of duplicated — which is also what keeps the rendered
 * `RunTrace` fragments referentially stable for `React.memo`.
 */
function normalizeStep(step: RunTraceStep): RunTraceStep {
  if (step.kind !== 'thinking') return step
  const label = step.status === 'running' ? 'Обрабатывает запрос…' : 'Обработка завершена'
  return step.label === label ? step : { ...step, label }
}

function normalizeSteps(steps: RunTraceStep[]): RunTraceStep[] {
  let changed = false
  const next = steps.map((step) => {
    const normalized = normalizeStep(step)
    if (normalized !== step) changed = true
    return normalized
  })
  return changed ? next : steps
}

function upsertStep(steps: RunTraceStep[], next: RunTraceStep): RunTraceStep[] {
  const index = steps.findIndex((step) => step.id === next.id)
  const candidate = cloneStep(next)
  const merged = index === -1
    ? [...steps, candidate]
    : steps.map((step, candidateIndex) => candidateIndex === index ? candidate : step)
  // `sortSteps` is stable on the very index order `merged` already carries, so
  // for an ordered array it is an expensive identity function. Events arrive in
  // time order, which makes that the normal case.
  return stepsOrdered(merged) ? merged : sortSteps(merged)
}

function withUpdatedTrace(trace: RunTrace, at: string, patch: Partial<RunTrace>): RunTrace {
  const steps = patch.steps ?? trace.steps
  return {
    ...trace,
    ...patch,
    updatedAt: at,
    steps: normalizeSteps(steps),
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
  let changed = false
  const steps = trace.steps.map((step) => {
    if (step.kind !== 'thinking' || step.status !== 'running') return step
    changed = true
    return { ...step, status, finishedAt: at }
  })
  return changed ? { ...trace, steps } : trace
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
  let next = withUpdatedTrace(trace, at, {
    providerId: event.providerId ?? trace.providerId,
    model: event.model ?? trace.model,
    inputTokens: event.inputTokens ?? trace.inputTokens,
    outputTokens: event.outputTokens ?? trace.outputTokens,
    totalTokens: event.totalTokens ?? trace.totalTokens,
    failureKind: event.failureKind ?? trace.failureKind,
    retryable: event.retryable ?? trace.retryable,
    retryAfterSeconds: event.retryAfterSeconds ?? trace.retryAfterSeconds,
  })
  switch (event.type) {
    case 'run.started':
      return withUpdatedTrace(next, at, {
        status: 'running',
        startedAt: trace.startedAt || at,
        kind: event.runKind ?? trace.kind,
        parentRunId: event.parentRunId ?? trace.parentRunId,
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
  // Unchanged traces keep their identity here, which is what lets the renderer
  // skip every trace block that this event did not touch.
  return tracesOrdered(next) ? next : sortRunTraces(next)
}

function traceTime(trace: RunTrace): number {
  const cached = traceTimes.get(trace)
  if (cached !== undefined) return cached
  const parsed = Date.parse(trace.startedAt || trace.updatedAt || trace.finishedAt || '')
  const value = Number.isFinite(parsed) ? parsed : 0
  traceTimes.set(trace, value)
  return value
}

/** Stable chronological ordering for historical and live traces. */
export function sortRunTraces(traces: RunTrace[]): RunTrace[] {
  return traces
    .map((trace, index) => ({ trace, index }))
    .sort((left, right) => traceTime(left.trace) - traceTime(right.trace) || left.index - right.index)
    .map(({ trace }) => trace)
}

/**
 * Turn one run-wide audit trace into the small chronological blocks rendered
 * between assistant response segments. Every tool call gets its own block;
 * the terminal marker is omitted because the final response already conveys
 * completion in the conversation flow.
 *
 * The result is memoized by trace identity: a conversation re-renders many
 * times per run, but `aggregateChatEvent` only replaces the trace the event
 * belongs to, so every other trace keeps both its fragments and their object
 * identity. Traces are immutable here, and the fragments are render-only, so
 * the cache is never observable through the returned values.
 */
const timelineFragments = new WeakMap<RunTrace, RunTrace[]>()

export function splitRunTraceForTimeline(trace: RunTrace): RunTrace[] {
  const cached = timelineFragments.get(trace)
  if (cached) return cached
  const fragments: RunTrace[] = []
  const initialThinking = trace.steps.find((step): step is ThinkingTraceStep => step.kind === 'thinking')
  if (initialThinking) {
    fragments.push({
      ...trace,
      id: `${trace.id}:thinking`,
      startedAt: initialThinking.createdAt,
      updatedAt: initialThinking.finishedAt ?? trace.updatedAt,
      finishedAt: initialThinking.finishedAt,
      status: initialThinking.status === 'running'
        ? 'running'
        : initialThinking.status === 'failed'
          ? 'error'
          : initialThinking.status === 'cancelled'
            ? 'cancelled'
            : 'complete',
      toolCalls: undefined,
      steps: [normalizeStep(initialThinking)],
    })
  }
  const toolSteps = trace.steps.filter((step): step is ToolTraceStep => step.kind === 'tool')
  for (const tool of toolSteps) {
    const approvals = trace.steps.filter((step): step is ApprovalTraceStep => step.kind === 'approval' && step.approval.toolCallId === tool.toolCall.id)
    const waiting = approvals.some((step) => step.status === 'waiting')
    const status: RunTraceStatus = waiting
      ? 'waiting_approval'
      : tool.status === 'running' || tool.status === 'pending'
        ? 'running'
        : tool.status === 'cancelled' || tool.status === 'denied'
          ? 'cancelled'
          : tool.status === 'failed'
            ? 'error'
            : 'complete'
    const preparation: ThinkingTraceStep = {
      id: `${trace.runId}:thinking:${tool.toolCall.id}`,
      kind: 'thinking',
      status: 'completed',
      label: 'Обработка завершена',
      createdAt: tool.createdAt,
      finishedAt: tool.createdAt,
    }
    fragments.push({
      ...trace,
      id: `${trace.id}:tool:${tool.toolCall.id}`,
      startedAt: tool.createdAt,
      updatedAt: tool.finishedAt ?? trace.updatedAt,
      finishedAt: tool.finishedAt,
      status,
      toolCalls: [tool.toolCall],
      steps: [preparation, tool, ...approvals],
    })
  }
  const ordered = tracesOrdered(fragments) ? fragments : sortRunTraces(fragments)
  timelineFragments.set(trace, ordered)
  return ordered
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
    parentRunId: optionalString(value, 'parentRunId', 'parent_run_id'),
    providerId: optionalString(value, 'providerId', 'provider_id'),
    model: optionalString(value, 'model'),
    inputTokens: nonNegativeInteger(value, 'inputTokens', 'input_tokens'),
    outputTokens: nonNegativeInteger(value, 'outputTokens', 'output_tokens'),
    totalTokens: nonNegativeInteger(value, 'totalTokens', 'total_tokens'),
    failureKind: normalizeRunFailureKind(value.failureKind ?? value.failure_kind),
    retryable: optionalBoolean(value, 'retryable', 'failureRetryable', 'failure_retryable'),
    retryAfterSeconds: nonNegativeInteger(value, 'retryAfterSeconds', 'retry_after_seconds', 'failureRetryAfterSeconds', 'failure_retry_after_seconds'),
    failure,
    toolCalls: toolCalls.length > 0 ? toolCalls : undefined,
    steps: sortSteps(steps),
  }
}

/**
 * One rendered row of the conversation: either a message bubble or one of the
 * execution-trace blocks a run was split into.
 *
 * `time` and `priority` are the sort keys, resolved once when the entry is
 * built. The renderer used to recompute them inside the comparator on every
 * pass, which meant `Date.parse` ran O(n log n) times per streaming token.
 */
export type ChatTimelineEntry =
  | { kind: 'message'; key: string; time: number; priority: number; message: ChatMessage }
  | { kind: 'trace'; key: string; time: number; priority: number; trace: RunTrace; showRecovery?: boolean; recoveryMessageId?: string }

function timelineTime(value: string | undefined): number {
  const parsed = Date.parse(value ?? '')
  return Number.isFinite(parsed) ? parsed : 0
}

function messageTimelineEntry(message: ChatMessage): ChatTimelineEntry {
  return {
    kind: 'message',
    key: message.id,
    time: timelineTime(message.createdAt),
    // A user message opens the exchange, the trace blocks describe the work,
    // and the answer closes it; ties in time keep that reading order.
    priority: message.role === 'user' ? 0 : 2,
    message,
  }
}

function traceTimelineEntry(trace: RunTrace): ChatTimelineEntry {
  return { kind: 'trace', key: `trace-${trace.id}`, time: timelineTime(trace.startedAt), priority: 1, trace }
}

function compareTimelineEntries(left: ChatTimelineEntry, right: ChatTimelineEntry): number {
  return left.time - right.time || left.priority - right.priority
}

/** Build the chronological timeline of one conversation's durable state. */
export function buildChatTimeline(messages: ChatMessage[], traces?: RunTrace[]): ChatTimelineEntry[] {
  const entries: ChatTimelineEntry[] = []
  for (const message of messages) {
    if (message.role !== 'tool') entries.push(messageTimelineEntry(message))
  }
  for (const trace of traces ?? []) {
    for (const fragment of splitRunTraceForTimeline(trace)) entries.push(traceTimelineEntry(fragment))
  }
  entries.sort(compareTimelineEntries)
  const lastTraceKeyByRun = new Map<string, string>()
  const recoveryMessageByRun = new Map<string, string>()
  const latestUserMessageId = [...entries].reverse().find((entry) => entry.kind === 'message' && entry.message.role === 'user')
  let previousUserMessageId: string | undefined
  for (const entry of entries) {
    if (entry.kind === 'message' && entry.message.role === 'user') {
      previousUserMessageId = entry.message.id
      continue
    }
    if (entry.kind === 'trace') {
      lastTraceKeyByRun.set(entry.trace.runId, entry.key)
      if (previousUserMessageId && !recoveryMessageByRun.has(entry.trace.runId)) {
        recoveryMessageByRun.set(entry.trace.runId, previousUserMessageId)
      }
    }
  }
  return entries.map((entry) => {
    if (entry.kind !== 'trace' || lastTraceKeyByRun.get(entry.trace.runId) !== entry.key) return entry
    const anchor = recoveryMessageByRun.get(entry.trace.runId)
    return {
      ...entry,
      showRecovery: true,
      // retryLast must never reinterpret an older branch against the newer
      // transcript. Route-repair actions remain useful on historical errors,
      // but retry is offered only for the conversation's latest user turn.
      recoveryMessageId: latestUserMessageId?.kind === 'message' && anchor === latestUserMessageId.message.id ? anchor : undefined,
    }
  })
}

/**
 * Splice the messages of the run currently streaming into an already ordered
 * timeline. The live buffer is kept out of the conversation state so a token
 * cannot invalidate `buildChatTimeline`; this is the cheap per-token step that
 * replaces rebuilding and re-sorting the whole conversation (C-1).
 */
export function mergeStreamingMessages(entries: ChatTimelineEntry[], streaming: ChatMessage[]): ChatTimelineEntry[] {
  if (streaming.length === 0) return entries
  const merged = entries.slice()
  for (const message of streaming) {
    if (message.role === 'tool') continue
    const entry = messageTimelineEntry(message)
    // Insert after every entry that already sorts before or with it, which is
    // exactly where a stable sort of the concatenation would have placed it.
    let index = merged.length
    while (index > 0 && compareTimelineEntries(merged[index - 1], entry) > 0) index -= 1
    merged.splice(index, 0, entry)
  }
  return merged
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
