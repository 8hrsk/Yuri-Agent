export type ScheduleType = 'once' | 'interval' | 'cron'

export type ScheduleStatus = 'active' | 'paused' | 'completed' | 'error' | 'unknown'

export type MisfirePolicy = 'skip' | 'run_once'

export type DeliveryChannel = 'in_app' | 'notification'

export interface ScheduleBudget {
  maxDurationSeconds?: number
  maxTokens?: number
  maxToolCalls?: number
}

export interface Schedule {
  id: string
  title: string
  prompt: string
  type: ScheduleType
  /** ISO instant used by one-shot schedules. */
  runAt?: string
  /** Interval duration in seconds. */
  intervalSeconds?: number
  /** Standard five-field cron expression. */
  expression?: string
  timezone: string
  misfirePolicy: MisfirePolicy
  enabled: boolean
  status: ScheduleStatus
  nextRunAt?: string
  lastRunAt?: string
  deliveryChannel: DeliveryChannel
  budget?: ScheduleBudget
  createdAt?: string
  updatedAt?: string
  lastError?: string
}

export interface ScheduleInput {
  id?: string
  title: string
  prompt: string
  type: ScheduleType
  runAt?: string
  intervalSeconds?: number
  expression?: string
  timezone: string
  misfirePolicy: MisfirePolicy
  enabled?: boolean
  deliveryChannel?: DeliveryChannel
  budget?: ScheduleBudget
}

export type JobRunStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled' | 'skipped' | 'unknown'

export interface JobRun {
  id: string
  scheduleId: string
  scheduleTitle?: string
  status: JobRunStatus
  attempt: number
  startedAt?: string
  finishedAt?: string
  durationMs?: number
  error?: string
  summary?: string
  triggeredBy?: 'schedule' | 'manual' | 'recovery' | 'unknown'
}

export interface JobRunListOptions {
  scheduleId?: string
  limit?: number
}
