export type MessageRole = 'user' | 'assistant' | 'tool'

export type MessageStatus = 'complete' | 'streaming' | 'cancelled' | 'error'

export type RunStatus = 'idle' | 'thinking' | 'tool_running' | 'waiting_approval' | 'speaking' | 'cancelled' | 'error'

export type ToolRisk = 'low' | 'medium' | 'high' | 'critical'

export type ToolStatus = 'pending' | 'running' | 'completed' | 'failed' | 'cancelled' | 'denied'

/**
 * A tool exposed by the local runtime. This is discovery metadata only: the
 * model still has to request a call and the runtime applies policy immediately
 * before execution.
 */
export interface ChatTool {
  id: string
  name: string
  label: string
  description?: string
  risk: ToolRisk
  available: boolean
  requiresApproval?: boolean
  capabilities?: string[]
}

/** Alias kept for callers that use the agent/runtime vocabulary. */
export type ChatToolDescriptor = ChatTool

export interface ToolCall {
  id: string
  name: string
  label: string
  risk: ToolRisk
  status: ToolStatus
  args: Record<string, unknown>
  result?: string
  startedAt?: string
  finishedAt?: string
}

export interface ApprovalRequest {
  id: string
  toolCallId: string
  title: string
  explanation: string
  risk: ToolRisk
  scope: string
  expiresAt?: string
  /** Filesystem access requests can be granted for this call or persisted. */
  kind?: 'action' | 'filesystem_access'
  path?: string
  permissionRoot?: string
  canRemember?: boolean
}

export type ApprovalDecision = 'approve' | 'allow_once' | 'allow_always' | 'deny'

export type RunTraceStatus = 'queued' | 'running' | 'waiting_approval' | 'complete' | 'cancelled' | 'error'

export type RunFailureKind = 'unknown' | 'authentication' | 'rate_limit' | 'quota_exhausted' | 'context_limit' | 'model_unavailable' | 'timeout' | 'transient' | 'invalid_request' | 'budget_exceeded'

export type RunTraceStepStatus = 'pending' | 'running' | 'waiting' | 'completed' | 'failed' | 'cancelled' | 'denied'

interface RunTraceStepBase {
  id: string
  createdAt: string
  finishedAt?: string
}

/**
 * Thinking is deliberately a lifecycle marker, never a model reasoning
 * payload. The renderer must not receive or display hidden chain-of-thought.
 */
export interface ThinkingTraceStep extends RunTraceStepBase {
  kind: 'thinking'
  status: Exclude<RunTraceStepStatus, 'pending' | 'waiting' | 'denied'>
  label: string
}

export interface StatusTraceStep extends RunTraceStepBase {
  kind: 'status'
  status: RunStatus
  label: string
}

export interface ToolTraceStep extends RunTraceStepBase {
  kind: 'tool'
  status: ToolStatus
  toolCall: ToolCall
}

export type ApprovalTraceStatus = 'waiting' | 'approved' | 'denied' | 'expired'

export interface ApprovalTraceStep extends RunTraceStepBase {
  kind: 'approval'
  status: ApprovalTraceStatus
  approval: ApprovalRequest
}

export interface CompletionTraceStep extends RunTraceStepBase {
  kind: 'completion'
  status: 'complete' | 'cancelled' | 'error'
  label: string
  error?: string
}

/** Safe route metadata carried by a provider hand-off event. */
export interface RunFallback {
  fromProviderId?: string
  fromModel?: string
  toProviderId?: string
  toModel?: string
  reason: string
}

/**
 * A provider/model hand-off recorded before visible output or side effects.
 * Only route identifiers and the provider-neutral reason are retained; this
 * contract deliberately has no room for upstream errors, credentials, or
 * request payloads.
 */
export interface FallbackTraceStep extends RunTraceStepBase, RunFallback {
  kind: 'fallback'
  status: 'completed'
  label: string
}

export type RunTraceStep =
  | ThinkingTraceStep
  | StatusTraceStep
  | ToolTraceStep
  | ApprovalTraceStep
  | CompletionTraceStep
  | FallbackTraceStep

/**
 * A persisted/live execution timeline. Only operational lifecycle, tool
 * intents/results, and approval state belong here; hidden reasoning does not.
 */
export interface RunTrace {
  id: string
  runId: string
  status: RunTraceStatus
  startedAt: string
  updatedAt?: string
  finishedAt?: string
  /** Optional persistence metadata retained for future trace screens. */
  kind?: string
  parentRunId?: string
  /** Immutable, non-secret inference route captured when the run started. */
  providerId?: string
  model?: string
  inputTokens?: number
  outputTokens?: number
  totalTokens?: number
  maxSteps?: number
  maxTokens?: number
  maxToolCalls?: number
  maxDurationSeconds?: number
  failureKind?: RunFailureKind
  retryable?: boolean
  retryAfterSeconds?: number
  failure?: string
  fallback?: RunFallback & { createdAt?: string }
  toolCalls?: ToolCall[]
  steps: RunTraceStep[]
}

export interface ChatMessage {
  id: string
  role: MessageRole
  content: string
  status: MessageStatus
  createdAt: string
  runId?: string
  toolCall?: ToolCall
  attachments?: ChatAttachment[]
}

export type ChatAttachmentKind = 'text' | 'image'

export interface ChatAttachment {
  id: string
  name: string
  kind: ChatAttachmentKind
  mediaType: string
  sizeBytes: number
  /** Present only for newly selected local files; durable history loads lazily. */
  previewDataUrl?: string
}

export interface ChatAttachmentInput extends ChatAttachment {
  dataBase64: string
}

export interface ChatAttachmentContent {
  id: string
  mediaType: string
  dataUrl: string
}

/**
 * Why a conversation currently has its title.  `default` is the temporary
 * placeholder used before the first turn is titled, `generated` is produced
 * by Yuri in the background, and `user` is an explicit owner rename.
 */
export type ConversationTitleSource = 'default' | 'generated' | 'user'

export interface Conversation {
  id: string
  title: string
  /** Optional for bridges predating automatic conversation titles. */
  titleSource?: ConversationTitleSource
  preview: string
  updatedAt: string
  messages: ChatMessage[]
  /** Historical traces are optional for backwards-compatible transcripts. */
  traces?: RunTrace[]
  /**
   * The transcript continues before `messages[0]`.
   *
   * The backend returns only the newest page of a conversation, so this is what
   * tells the transcript whether "показать более ранние" still has anything to
   * fetch once the locally held history is fully uncovered. A backend that does
   * not page leaves it undefined, which reads as "this is the whole thing".
   */
  hasMoreMessages?: boolean
}

/**
 * One page of the conversation list.
 *
 * The bridge clamps every one of these — it treats the renderer as untrusted —
 * so these are a request, not a guarantee. Omitting a field asks for the
 * bridge's own default rather than restating it here.
 */
export interface ConversationPageOptions {
  /** Conversations on the page, newest-updated first. */
  limit?: number
  /** How many to skip. Without it, everything past the first page is unreachable. */
  offset?: number
  /**
   * Newest messages to carry per conversation.
   *
   * Omitted — the default — asks for metadata only: the sidebar draws a title,
   * a preview and a timestamp, and the one transcript actually opened is
   * fetched by `listMessages` instead of every conversation's being dragged
   * along with the list.
   */
  messageLimit?: number
}

/** One page of transcript older than a cursor, with the traces of its runs. */
export interface ChatHistoryPage {
  conversationId: string
  messages: ChatMessage[]
  traces: RunTrace[]
  /** False once the page reached the start of the transcript. */
  hasMore: boolean
}
