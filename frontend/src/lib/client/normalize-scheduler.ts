import type {
  DeliveryChannel,
  JobRun,
  JobRunStatus,
  MisfirePolicy,
  Schedule,
  ScheduleBudget,
  ScheduleInput,
  ScheduleStatus,
  ScheduleType,
} from '../contracts'
import { optionalNumber, optionalString } from './primitives'
import type { UnknownRecord } from './primitives'

function normalizeScheduleType(value: unknown): ScheduleType {
  const type = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (type === 'once' || type === 'one_shot' || type === 'oneoff' || type === 'one_time') return 'once'
  if (type === 'interval' || type === 'repeating' || type === 'periodic') return 'interval'
  if (type === 'cron' || type === 'crontab') return 'cron'
  return 'once'
}

function normalizeScheduleStatus(value: unknown, enabled: boolean): ScheduleStatus {
  const status = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (!value) return enabled ? 'active' : 'paused'
  if (status === 'paused' || status === 'disabled' || status === 'off') return 'paused'
  if (status === 'completed' || status === 'complete' || status === 'finished') return 'completed'
  if (status === 'error' || status === 'failed' || status === 'failure') return 'error'
  if (status === 'active' || status === 'enabled' || status === 'running' || enabled) return 'active'
  return 'unknown'
}

function normalizeMisfirePolicy(value: unknown): MisfirePolicy {
  const policy = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  return policy === 'run_once' || policy === 'runonce' || policy === 'fire_once' || policy === 'execute_once'
    ? 'run_once'
    : 'skip'
}

function normalizeDeliveryChannel(value: unknown): DeliveryChannel {
  const channel = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  return channel === 'notification' || channel === 'local_notification' || channel === 'desktop' ? 'notification' : 'in_app'
}

function normalizeBudget(value: unknown): ScheduleBudget | undefined {
  if (!value || typeof value !== 'object') return undefined
  const source = value as UnknownRecord
  const maxDurationSeconds = optionalNumber(source, 'maxDurationSeconds', 'max_duration_seconds', 'timeoutSeconds', 'timeout_seconds')
  const maxTokens = optionalNumber(source, 'maxTokens', 'max_tokens', 'tokenLimit', 'token_limit')
  const maxToolCalls = optionalNumber(source, 'maxToolCalls', 'max_tool_calls', 'toolLimit', 'tool_limit')
  if (maxDurationSeconds === undefined && maxTokens === undefined && maxToolCalls === undefined) return undefined
  return {
    maxDurationSeconds,
    maxTokens,
    maxToolCalls,
  }
}

function normalizeSchedule(value: unknown): Schedule | undefined {
  if (!value || typeof value !== 'object') return undefined
  const rawValue = value as UnknownRecord
  const source = rawValue.schedule && typeof rawValue.schedule === 'object' ? rawValue.schedule as UnknownRecord : rawValue
  const id = optionalString(source, 'id', 'scheduleId', 'schedule_id')
  if (!id) return undefined
  const type = normalizeScheduleType(source.type ?? source.scheduleType ?? source.schedule_type ?? source.kind)
  const enabledValue = source.enabled ?? source.isEnabled ?? source.is_enabled
  const statusValue = source.status ?? source.state
  const status = normalizeScheduleStatus(statusValue, Boolean(enabledValue))
  const enabled = enabledValue === undefined ? status === 'active' : Boolean(enabledValue)
  const intervalSeconds = optionalNumber(source, 'intervalSeconds', 'interval_seconds', 'interval', 'everySeconds', 'every_seconds')
  const expression = optionalString(source, 'expression', 'cron', 'cronExpression', 'cron_expression')
  const runAt = optionalString(source, 'runAt', 'run_at', 'startAt', 'start_at', 'scheduledAt', 'scheduled_at', 'executeAt', 'execute_at')
  const budget = normalizeBudget(source.budget ?? source.limits)
  let payloadPrompt = ''
  const payload = source.payloadJson ?? source.payload_json
  if (typeof payload === 'string' && payload.trim() !== '') {
    try {
      const parsed = JSON.parse(payload) as unknown
      if (parsed && typeof parsed === 'object') {
        payloadPrompt = optionalString(parsed as UnknownRecord, 'prompt', 'instruction', 'task', 'description') ?? ''
      }
    } catch {
      // An opaque scheduler payload is allowed; the agent layer may decode it.
    }
  }
  return {
    id,
    title: optionalString(source, 'title', 'name', 'label') ?? 'Без названия',
    prompt: optionalString(source, 'prompt', 'instruction', 'task', 'description') ?? payloadPrompt,
    type,
    runAt,
    intervalSeconds,
    expression,
    timezone: optionalString(source, 'timezone', 'timeZone', 'time_zone') ?? (Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'),
    misfirePolicy: normalizeMisfirePolicy(source.misfirePolicy ?? source.misfire_policy ?? source.misfire),
    enabled,
    status,
    nextRunAt: optionalString(source, 'nextRunAt', 'next_run_at', 'nextRun'),
    lastRunAt: optionalString(source, 'lastRunAt', 'last_run_at', 'lastRun'),
    deliveryChannel: normalizeDeliveryChannel(source.deliveryChannel ?? source.delivery_channel ?? source.channel),
    budget,
    createdAt: optionalString(source, 'createdAt', 'created_at'),
    updatedAt: optionalString(source, 'updatedAt', 'updated_at'),
    lastError: optionalString(source, 'lastError', 'last_error', 'error'),
  }
}

function normalizeScheduleList(value: unknown): Schedule[] {
  const rawItems = Array.isArray(value)
    ? value
    : value && typeof value === 'object'
      ? ((value as UnknownRecord).items ?? (value as UnknownRecord).schedules ?? (value as UnknownRecord).results)
      : undefined
  return Array.isArray(rawItems)
    ? rawItems.map(normalizeSchedule).filter((item): item is Schedule => Boolean(item))
    : []
}

function normalizeJobRunStatus(value: unknown): JobRunStatus {
  const status = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (status === 'queued' || status === 'pending' || status === 'scheduled') return 'queued'
  if (status === 'running' || status === 'started' || status === 'active') return 'running'
  if (status === 'completed' || status === 'complete' || status === 'success' || status === 'succeeded') return 'completed'
  if (status === 'failed' || status === 'error' || status === 'failure') return 'failed'
  if (status === 'cancelled' || status === 'canceled' || status === 'aborted') return 'cancelled'
  if (status === 'skipped' || status === 'misfired') return 'skipped'
  return 'unknown'
}

function normalizeTriggeredBy(value: unknown): JobRun['triggeredBy'] {
  const trigger = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (trigger === 'schedule' || trigger === 'scheduled' || trigger === 'cron') return 'schedule'
  if (trigger === 'manual' || trigger === 'user') return 'manual'
  if (trigger === 'recovery' || trigger === 'restart' || trigger === 'misfire') return 'recovery'
  return 'unknown'
}

function normalizeJobRun(value: unknown): JobRun | undefined {
  if (!value || typeof value !== 'object') return undefined
  const rawValue = value as UnknownRecord
  const source = rawValue.run && typeof rawValue.run === 'object' ? rawValue.run as UnknownRecord : rawValue
  const id = optionalString(source, 'id', 'runId', 'run_id', 'jobRunId', 'job_run_id')
  const scheduleId = optionalString(source, 'scheduleId', 'schedule_id')
  if (!id || !scheduleId) return undefined
  return {
    id,
    scheduleId,
    scheduleTitle: optionalString(source, 'scheduleTitle', 'schedule_title', 'title'),
    status: normalizeJobRunStatus(source.status ?? source.state),
    attempt: Math.max(1, Math.round(optionalNumber(source, 'attempt', 'attemptNumber', 'attempt_number') ?? 1)),
    startedAt: optionalString(source, 'startedAt', 'started_at'),
    finishedAt: optionalString(source, 'finishedAt', 'finished_at', 'completedAt', 'completed_at'),
    durationMs: optionalNumber(source, 'durationMs', 'duration_ms', 'elapsedMs', 'elapsed_ms'),
    error: optionalString(source, 'error', 'lastError', 'last_error'),
    summary: optionalString(source, 'summary', 'result', 'resultRef', 'result_ref', 'message'),
    triggeredBy: normalizeTriggeredBy(source.triggeredBy ?? source.triggered_by ?? source.trigger),
  }
}

function normalizeJobRunList(value: unknown): JobRun[] {
  const rawItems = Array.isArray(value)
    ? value
    : value && typeof value === 'object'
      ? ((value as UnknownRecord).items ?? (value as UnknownRecord).runs ?? (value as UnknownRecord).jobRuns ?? (value as UnknownRecord).job_runs ?? (value as UnknownRecord).results)
      : undefined
  return Array.isArray(rawItems)
    ? rawItems.map(normalizeJobRun).filter((item): item is JobRun => Boolean(item))
    : []
}

function scheduleWire(input: ScheduleInput): UnknownRecord {
  const payloadJson = JSON.stringify({ prompt: input.prompt })
  return {
    ...input,
    name: input.title,
    kind: input.type,
    startAt: input.runAt,
    start_at: input.runAt,
    payloadJson,
    payload_json: payloadJson,
    scheduleType: input.type,
    schedule_type: input.type,
    run_at: input.runAt,
    interval_seconds: input.intervalSeconds,
    cronExpression: input.expression,
    cron_expression: input.expression,
    misfire_policy: input.misfirePolicy,
    delivery_channel: input.deliveryChannel,
  }
}

export { normalizeJobRun, normalizeJobRunList, normalizeSchedule, normalizeScheduleList, scheduleWire }
