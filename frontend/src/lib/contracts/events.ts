import type { ApprovalRequest, RunFailureKind, RunStatus, RunTraceStep, ToolCall } from './chat'

interface ChatEventMeta {
  /** Optional on the Wails event bus; useful when several runs are active. */
  conversationId?: string
  /** Event time is advisory and falls back to renderer receipt time. */
  createdAt?: string
  timestamp?: string
  /** Operational provenance for nested anonymous runs. */
  runKind?: string
  parentRunId?: string
  /** Immutable inference attribution; usage is populated on terminal events. */
  providerId?: string
  model?: string
  inputTokens?: number
  outputTokens?: number
  totalTokens?: number
  failureKind?: RunFailureKind
  retryable?: boolean
  retryAfterSeconds?: number
}

export type ChatEvent =
  | ({ type: 'run.started'; runId: string } & ChatEventMeta)
  | ({ type: 'assistant.delta'; runId: string; messageId: string; delta: string } & ChatEventMeta)
  | ({ type: 'assistant.completed'; runId: string; messageId: string } & ChatEventMeta)
  | ({ type: 'tool.started'; runId: string; toolCall: ToolCall } & ChatEventMeta)
  | ({ type: 'approval.required'; runId: string; approval: ApprovalRequest } & ChatEventMeta)
  | ({ type: 'tool.updated'; runId: string; toolCall: ToolCall } & ChatEventMeta)
  | ({ type: 'run.status'; runId: string; status: RunStatus; label: string } & ChatEventMeta)
  | ({ type: 'run.completed'; runId: string; status: 'complete' | 'cancelled' | 'error'; error?: string } & ChatEventMeta)
  /** Future backends may send a lifecycle-only trace step in one envelope. */
  | ({ type: 'trace.step'; runId: string; step: RunTraceStep } & ChatEventMeta)

export interface RunResult {
  runId: string
  status: 'complete' | 'cancelled' | 'error'
}
