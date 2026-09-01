import { memo, useCallback, useEffect, useMemo, useState, type MouseEvent } from 'react'

import { approvalStatusLabel, routeFallbackLabel, runStatusLabel, toolStatusLabel, type ChatTimelineEntry } from '../lib/chat-trace'
import type { ChatAttachment, ChatAttachmentContent, ChatMessage, RunTrace, RunTraceStep, ToolCall } from '../lib/contracts'
import { formatClock } from '../lib/datetime'
import {
  inferenceFailureGuidance,
  inferenceFailureRecoveryActions,
  inferenceRecoveryActionLabels,
  type InferenceRecoveryAction,
} from '../lib/inference-failures'
import { Icon } from './Icon'
import { MarkdownMessage } from './MarkdownMessage'

/**
 * Every component in this file is wrapped in `React.memo`.
 *
 * A streaming answer re-renders `ChatView` on each batch of tokens. Without
 * memoization React re-rendered every message bubble and every trace block of
 * the conversation, so the cost of one token grew with the length of the
 * history. Memoization only pays off while the props stay referentially
 * stable, so the callbacks below take ids instead of closing over a message,
 * and `chat-trace` keeps unchanged traces and their split fragments identical
 * across events.
 */

export const ToolCallCard = memo(function ToolCallCard({ toolCall }: { toolCall: ToolCall }) {
  // Pretty-printing tool arguments is unbounded work — a file write carries the
  // whole payload — and it used to run in the render body on every token (M-40).
  // The memo caps it at once per distinct args object; the card itself is only
  // mounted while the trace around it is open, so a collapsed trace never pays
  // it at all.
  const args = useMemo(() => JSON.stringify(toolCall.args, null, 2), [toolCall.args])
  return (
    <article className={`tool-card tool-card--${toolCall.status}`}>
      <div className="tool-card__topline">
        <span className="tool-card__icon"><Icon name="command" width={15} height={15} /></span>
        <span className="tool-card__title">
          <strong>{toolCall.label}</strong>
          <small>{toolCall.name}</small>
        </span>
        <span className={`risk-pill risk-pill--${toolCall.risk}`}>{toolCall.risk}</span>
      </div>
      <div className="tool-card__body">
        <div className="tool-card__status"><span className="tool-card__status-dot" /> {toolStatusLabel(toolCall.status)}</div>
        <div className="tool-card__payload">
          <span className="tool-card__payload-label">Параметры</span>
          <code>{args}</code>
        </div>
      </div>
      {toolCall.result && (
        <div className="tool-card__result-wrap">
          <span className="tool-card__payload-label">Результат</span>
          <p className="tool-card__result">{toolCall.result}</p>
        </div>
      )}
    </article>
  )
})

function traceStatusCopy(trace: RunTrace): string {
  if (trace.status === 'queued') return 'В очереди'
  if (trace.status === 'complete') return 'Завершено'
  if (trace.status === 'cancelled') return 'Остановлено'
  if (trace.status === 'error') return 'Ошибка'
  if (trace.status === 'waiting_approval') return 'Ожидает разрешения'
  const latestStatus = [...trace.steps].reverse().find((step): step is Extract<RunTraceStep, { kind: 'status' }> => step.kind === 'status')
  return latestStatus ? runStatusLabel(latestStatus.status) : 'Выполняется'
}

function traceToolCopy(trace: RunTrace): string {
  const labels = [
    ...trace.steps
      .filter((step): step is Extract<RunTraceStep, { kind: 'fallback' }> => step.kind === 'fallback')
      .map((step) => step.label),
    ...trace.steps
    .filter((step): step is Extract<RunTraceStep, { kind: 'tool' }> => step.kind === 'tool')
    .map((step) => step.toolCall.label || step.toolCall.name)
  ].filter((label, index, all) => all.indexOf(label) === index)
  if (labels.length === 0) return 'Обработка запроса'
  return labels.join(' · ')
}

function fallbackRouteCopy(step: Extract<RunTraceStep, { kind: 'fallback' }>): string {
  const from = [step.fromProviderId, step.fromModel].filter(Boolean).join(' · ') || 'текущий маршрут'
  const to = [step.toProviderId, step.toModel].filter(Boolean).join(' · ') || 'резервный маршрут'
  return `${from} → ${to}`
}

function traceStepLabel(step: RunTraceStep): string {
  switch (step.kind) {
    case 'thinking': return step.label
    case 'status': return step.label
    case 'tool': return `${step.toolCall.label} · ${toolStatusLabel(step.status)}`
    case 'approval': return `Подтверждение · ${approvalStatusLabel(step.status)}`
    case 'completion': return step.label
    case 'fallback': return step.label || routeFallbackLabel
  }
}

export const TraceStepCard = memo(function TraceStepCard({ step }: { step: RunTraceStep }) {
  if (step.kind === 'tool') return <ToolCallCard toolCall={step.toolCall} />

  const statusClass = step.status
  const icon = step.kind === 'thinking'
    ? 'spark'
    : step.kind === 'approval'
      ? 'shield'
      : step.kind === 'fallback'
        ? 'refresh'
      : step.kind === 'completion' && step.status === 'error'
        ? 'warning'
        : step.kind === 'completion' && step.status === 'complete'
          ? 'check'
          : 'activity'
  return (
    <div className={`trace-step trace-step--${step.kind} trace-step--${statusClass}`}>
      <span className="trace-step__icon"><Icon name={icon} width={14} height={14} /></span>
      <span className="trace-step__copy">
        <strong>{traceStepLabel(step)}</strong>
        {step.kind === 'approval' && <small>{step.approval.scope || step.approval.explanation}</small>}
        {step.kind === 'fallback' && <>
          <small className="trace-step__route">{fallbackRouteCopy(step)}</small>
          <small>{step.reason}</small>
        </>}
        {step.kind === 'completion' && step.error && <small>{step.error}</small>}
      </span>
    </div>
  )
})

/**
 * A trace block is the heaviest thing in the transcript: one card per tool call,
 * each holding the pretty-printed JSON of its arguments. Rendering all of that
 * behind a `<details>` nobody opened put thousands of nodes in the DOM of a long
 * conversation and paid the serialization for every one of them (H-18/M-40).
 *
 * The disclosure is therefore controlled rather than native: the body is not
 * rendered at all until the user opens the block. `onToggle` would have been the
 * idiomatic hook, but it only fires after the browser has already expanded the
 * (empty) element, so the open state is driven from the summary click instead.
 */
type ExecutionTraceProps = {
  trace: RunTrace
  showRecovery?: boolean
  recoveryMessageId?: string
  recoveryDisabled?: boolean
  onRetry?: (messageId: string) => void
  onOpenSettings?: () => void
  onOpenPersonality?: () => void
  onNewConversation?: () => void
}

export const ExecutionTrace = memo(function ExecutionTrace({
  trace,
  showRecovery = false,
  recoveryMessageId,
  recoveryDisabled = false,
  onRetry,
  onOpenSettings,
  onOpenPersonality,
  onNewConversation,
}: ExecutionTraceProps) {
  const [open, setOpen] = useState(false)
  const toggle = useCallback((event: MouseEvent<HTMLElement>) => {
    // Without this the user agent toggles the element under React, and the two
    // sources of truth drift apart on the next render.
    event.preventDefault()
    setOpen((current) => !current)
  }, [])
  const toolCount = trace.steps.filter((step) => step.kind === 'tool').length
  const waiting = trace.status === 'waiting_approval'
  const thinkingOnly = toolCount === 0 && trace.steps.every((step) => step.kind === 'thinking')
  const summary = traceToolCopy(trace)
  const heading = trace.kind === 'subagent' ? 'Субагент' : thinkingOnly ? 'Thinking' : 'Выполнение'
  const route = [trace.providerId, trace.model].filter(Boolean).join(' · ')
  const usage = trace.totalTokens !== undefined && trace.totalTokens > 0 ? `${trace.totalTokens.toLocaleString('ru-RU')} ток.` : ''
  const provenance = [route, usage].filter(Boolean).join(' · ')
  const failureGuidance = trace.status === 'error' ? inferenceFailureGuidance(trace.failureKind, trace.retryAfterSeconds, trace.retryable) : undefined
  const recoveryActions = showRecovery && trace.status === 'error'
    ? inferenceFailureRecoveryActions(trace.failureKind, trace.retryable).filter((action) => {
        if (action === 'retry') return Boolean(recoveryMessageId && onRetry)
        if (action === 'settings') return Boolean(onOpenSettings)
        if (action === 'personality') return Boolean(onOpenPersonality)
        return Boolean(onNewConversation)
      })
    : []
  const recover = (action: InferenceRecoveryAction) => {
    if (action === 'retry' && recoveryMessageId) onRetry?.(recoveryMessageId)
    else if (action === 'settings') onOpenSettings?.()
    else if (action === 'personality') onOpenPersonality?.()
    else if (action === 'new_chat') onNewConversation?.()
  }
  return (
    <details className={`run-trace run-trace--${trace.status}`} open={open}>
      <summary className="run-trace__summary" onClick={toggle}>
        <span className="run-trace__mark"><Icon name={waiting ? 'shield' : 'activity'} width={14} height={14} /></span>
        <span className="run-trace__heading">
          <strong>{heading}</strong>
          <small>{summary}</small>
          {provenance && <small className="run-trace__provenance">{provenance}</small>}
          {failureGuidance && <small className="run-trace__failure-guidance">{failureGuidance}</small>}
        </span>
        <span className="run-trace__status">{traceStatusCopy(trace)}</span>
        <Icon name="chevron-right" width={14} height={14} />
      </summary>
      {recoveryActions.length > 0 && <div aria-label="Действия после ошибки" className="run-trace__recovery">
        {recoveryActions.map((action) => <button disabled={recoveryDisabled} key={action} onClick={() => recover(action)} type="button">{inferenceRecoveryActionLabels[action]}</button>)}
      </div>}
      {open && (
        <div className="run-trace__body">
          <p className="run-trace__notice">Здесь показаны статусы выполнения, вызовы инструментов и их результаты. Скрытые рассуждения модели не отображаются.</p>
          <div className="run-trace__steps">
            {trace.steps.map((step) => <TraceStepCard key={step.id} step={step} />)}
          </div>
        </div>
      )}
    </details>
  )
})

type MessageBubbleProps = {
  agentName: string
  message: ChatMessage
  /** Takes the id and text so the parent can hand down one stable callback. */
  onSpeak: (messageId: string, content: string) => void
  onStopSpeaking: () => void
  speaking: boolean
  speechSupported: boolean
  onRetry: (messageId: string) => void
  loadAttachment: (messageId: string, attachmentId: string) => Promise<ChatAttachmentContent | undefined>
  onOpenExternalURL: (url: string) => void
  onOpenLocalPath: (path: string) => void
}

function AttachmentCard({ attachment, messageId, loadAttachment }: {
  attachment: ChatAttachment
  messageId: string
  loadAttachment: MessageBubbleProps['loadAttachment']
}) {
  const [dataUrl, setDataUrl] = useState(attachment.previewDataUrl)
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    if (attachment.kind !== 'image' || dataUrl || failed) return
    let active = true
    void loadAttachment(messageId, attachment.id).then((content) => {
      if (active && content?.dataUrl) setDataUrl(content.dataUrl)
      else if (active) setFailed(true)
    }).catch(() => { if (active) setFailed(true) })
    return () => { active = false }
  }, [attachment.id, attachment.kind, dataUrl, failed, loadAttachment, messageId])
  return (
    <div className={`message-attachment message-attachment--${attachment.kind}`}>
      {attachment.kind === 'image' && dataUrl
        ? <img alt={attachment.name} loading="lazy" src={dataUrl} />
        : <span className="message-attachment__icon"><Icon name="file" width={16} height={16} /></span>}
      <span><strong>{attachment.name}</strong><small>{Math.max(1, Math.round(attachment.sizeBytes / 1024))} КБ · {attachment.mediaType}</small></span>
    </div>
  )
}

export const MessageBubble = memo(function MessageBubble({
  agentName,
  message,
  onSpeak,
  onStopSpeaking,
  speaking,
  speechSupported,
  onRetry,
  loadAttachment,
  onOpenExternalURL,
  onOpenLocalPath,
}: MessageBubbleProps) {
  if (message.role === 'tool' && message.toolCall) return <ToolCallCard toolCall={message.toolCall} />

  const isAssistant = message.role === 'assistant'
  const interrupted = message.status === 'cancelled' || message.status === 'error'
  const statusLabel = message.status === 'streaming'
    ? 'Печатает'
    : message.status === 'cancelled'
      ? 'Остановлено'
      : message.status === 'error'
        ? 'Ошибка'
        : undefined

  return (
    <article className={`message message--${message.role} message--${message.status}`}>
      <div className="message__meta">
        <span className="message__author">{isAssistant ? agentName : 'Вы'}</span>
        <time dateTime={message.createdAt}>{formatClock(new Date(message.createdAt))}</time>
        {statusLabel && <span className="message__status">{statusLabel}</span>}
      </div>
      <div className="message__content">
        {message.content
          ? isAssistant
            ? <MarkdownMessage content={message.content} onOpenExternalURL={onOpenExternalURL} onOpenLocalPath={onOpenLocalPath} />
            : message.content
          : message.status === 'streaming'
            ? <span className="typing-indicator" aria-label={`${agentName} печатает`}><i /><i /><i /></span>
            : null}
        {message.status === 'streaming' && message.content && <span className="stream-cursor" aria-hidden="true" />}
      </div>
      {message.attachments && message.attachments.length > 0 && (
        <div className="message-attachments">
          {message.attachments.map((attachment) => <AttachmentCard attachment={attachment} key={attachment.id} loadAttachment={loadAttachment} messageId={message.id} />)}
        </div>
      )}
      {/* An interrupted answer keeps its actions even with no text: retrying is
          exactly what the user needs there. */}
      {isAssistant && message.status !== 'streaming' && (message.content || interrupted) && (
        <div className="message__actions">
          {speechSupported && message.content && (
            <button
              aria-label={speaking ? 'Остановить озвучивание' : 'Озвучить ответ'}
              className="message-action"
              onClick={speaking ? onStopSpeaking : () => onSpeak(message.id, message.content)}
              type="button"
            >
              <Icon name={speaking ? 'x' : 'volume'} width={14} height={14} />
              {speaking ? 'Остановить' : 'Слушать'}
            </button>
          )}
          {interrupted && (
            <button aria-label="Повторить ответ" className="message-action" onClick={() => onRetry(message.id)} type="button">
              <Icon name="refresh" width={14} height={14} /> Повторить
            </button>
          )}
        </div>
      )}
    </article>
  )
})

type ChatTimelineProps = {
  agentName: string
  entries: ChatTimelineEntry[]
  onRetry: (messageId: string) => void
  onOpenSettings?: () => void
  onOpenPersonality?: () => void
  onNewConversation?: () => void
  recoveryDisabled?: boolean
  onSpeak: (messageId: string, content: string) => void
  onStopSpeaking: () => void
  speakingId?: string
  speechSupported: boolean
  loadAttachment: MessageBubbleProps['loadAttachment']
  onOpenExternalURL: MessageBubbleProps['onOpenExternalURL']
  onOpenLocalPath: MessageBubbleProps['onOpenLocalPath']
}

/**
 * The conversation body. Memoized so that state which has nothing to do with
 * the transcript — the voice recording timer, the composer draft — cannot
 * reconcile it.
 */
export const ChatTimeline = memo(function ChatTimeline({
  agentName,
  entries,
  onRetry,
  onOpenSettings,
  onOpenPersonality,
  onNewConversation,
  recoveryDisabled,
  onSpeak,
  onStopSpeaking,
  speakingId,
  speechSupported,
  loadAttachment,
  onOpenExternalURL,
  onOpenLocalPath,
}: ChatTimelineProps) {
  return (
    <>
      {entries.map((entry) => entry.kind === 'trace'
        ? <ExecutionTrace
            key={entry.key}
            onNewConversation={onNewConversation}
            onOpenPersonality={onOpenPersonality}
            onOpenSettings={onOpenSettings}
            onRetry={onRetry}
            recoveryDisabled={recoveryDisabled}
            recoveryMessageId={entry.recoveryMessageId}
            showRecovery={entry.showRecovery}
            trace={entry.trace}
          />
        : <MessageBubble
            agentName={agentName}
            key={entry.key}
            message={entry.message}
            loadAttachment={loadAttachment}
            onOpenExternalURL={onOpenExternalURL}
            onOpenLocalPath={onOpenLocalPath}
            onRetry={onRetry}
            onSpeak={onSpeak}
            onStopSpeaking={onStopSpeaking}
            speaking={speakingId === entry.message.id}
            speechSupported={speechSupported}
          />)}
    </>
  )
})
