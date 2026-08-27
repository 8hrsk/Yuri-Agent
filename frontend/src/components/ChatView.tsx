import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'

import type { BackendConnection } from '../lib/backend'
import { createYuriClient } from '../lib/client'
import type {
  ApprovalRequest,
  ChatEvent,
  ChatMessage,
  Conversation,
  RunStatus,
  ToolCall,
} from '../lib/contracts'
import { useTTS, useVoice } from '../hooks/useVoice'
import { Icon } from './Icon'

type ChatViewProps = {
  backend: BackendConnection
  onOpenSettings: () => void
}

const starterPrompts = [
  'Познакомиться с Yuri',
  'Проверить доступ к файлам',
  'Запиши заметку в Documents',
]

const statusCopy: Record<RunStatus, string> = {
  idle: 'Готова к диалогу',
  thinking: 'Yuri думает…',
  tool_running: 'Выполняю действие…',
  waiting_approval: 'Ожидается ваше разрешение',
  speaking: 'Yuri говорит…',
  cancelled: 'Запуск остановлен',
  error: 'Нужна проверка запуска',
}

function makeId(prefix: string): string {
  const suffix = typeof crypto !== 'undefined' && 'randomUUID' in crypto
    ? crypto.randomUUID()
    : Math.random().toString(36).slice(2)
  return `${prefix}-${suffix}`
}

function formatTime(value: string): string {
  return new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit' }).format(new Date(value))
}

function formatDuration(durationMs: number): string {
  const seconds = Math.max(0, Math.floor(durationMs / 1000))
  return `00:${seconds.toString().padStart(2, '0')}`
}

function updateConversation(conversations: Conversation[], id: string, update: (conversation: Conversation) => Conversation): Conversation[] {
  return conversations.map((conversation) => conversation.id === id ? update(conversation) : conversation)
}

function upsertMessage(messages: ChatMessage[], message: ChatMessage): ChatMessage[] {
  const existingIndex = messages.findIndex((candidate) => candidate.id === message.id)
  if (existingIndex === -1) return [...messages, message]
  return messages.map((candidate, index) => index === existingIndex ? message : candidate)
}

function ToolCallCard({ toolCall }: { toolCall: ToolCall }) {
  const statusLabel: Record<ToolCall['status'], string> = {
    pending: 'Подготовлено',
    running: 'Выполняется',
    completed: 'Завершено',
    failed: 'Ошибка',
    cancelled: 'Остановлено',
    denied: 'Отклонено',
  }

  return (
    <div className={`tool-card tool-card--${toolCall.status}`}>
      <div className="tool-card__topline">
        <span className="tool-card__icon"><Icon name="command" width={15} height={15} /></span>
        <span className="tool-card__title">
          <strong>{toolCall.label}</strong>
          <small>{toolCall.name}</small>
        </span>
        <span className={`risk-pill risk-pill--${toolCall.risk}`}>{toolCall.risk}</span>
      </div>
      <div className="tool-card__body">
        <div className="tool-card__status"><span className="tool-card__status-dot" /> {statusLabel[toolCall.status]}</div>
        <code>{JSON.stringify(toolCall.args, null, 2)}</code>
      </div>
      {toolCall.result && <p className="tool-card__result">{toolCall.result}</p>}
    </div>
  )
}

function MessageBubble({
  message,
  onSpeak,
  onStopSpeaking,
  speaking,
  speechSupported,
  onRetry,
}: {
  message: ChatMessage
  onSpeak: () => void
  onStopSpeaking: () => void
  speaking: boolean
  speechSupported: boolean
  onRetry: () => void
}) {
  if (message.role === 'tool' && message.toolCall) return <ToolCallCard toolCall={message.toolCall} />

  const isAssistant = message.role === 'assistant'
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
        <span className="message__author">{isAssistant ? 'Yuri' : 'Вы'}</span>
        <time dateTime={message.createdAt}>{formatTime(message.createdAt)}</time>
        {statusLabel && <span className="message__status">{statusLabel}</span>}
      </div>
      <div className="message__content">
        {message.content || (message.status === 'streaming' ? <span className="typing-indicator" aria-label="Yuri печатает"><i /><i /><i /></span> : null)}
        {message.status === 'streaming' && message.content && <span className="stream-cursor" aria-hidden="true" />}
      </div>
      {isAssistant && message.content && message.status !== 'streaming' && (
        <div className="message__actions">
          {speechSupported && (
            <button
              aria-label={speaking ? 'Остановить озвучивание' : 'Озвучить ответ'}
              className="message-action"
              onClick={speaking ? onStopSpeaking : onSpeak}
              type="button"
            >
              <Icon name={speaking ? 'x' : 'volume'} width={14} height={14} />
              {speaking ? 'Остановить' : 'Слушать'}
            </button>
          )}
          {(message.status === 'error' || message.status === 'cancelled') && (
            <button aria-label="Повторить ответ" className="message-action" onClick={onRetry} type="button">
              <Icon name="refresh" width={14} height={14} /> Повторить
            </button>
          )}
        </div>
      )}
    </article>
  )
}

function ApprovalDialog({
  approval,
  busy,
  onDecision,
}: {
  approval: ApprovalRequest
  busy: boolean
  onDecision: (decision: 'approve' | 'deny') => void
}) {
  return (
    <div className="approval-backdrop">
      <section aria-describedby="approval-description" aria-labelledby="approval-title" aria-modal="true" className="approval-dialog" role="dialog">
        <div className="approval-dialog__mark"><Icon name="shield" width={22} height={22} /></div>
        <span className="section-heading__overline">Требуется подтверждение</span>
        <h2 id="approval-title">{approval.title}</h2>
        <p id="approval-description">{approval.explanation}</p>
        <div className="approval-dialog__scope">
          <span>Операция</span>
          <strong>{approval.scope}</strong>
        </div>
        <p className="approval-dialog__hint">Разрешение действует только для этого конкретного действия. Yuri не может расширить его из содержимого файла.</p>
        <div className="approval-dialog__actions">
          <button className="button button--quiet" disabled={busy} onClick={() => onDecision('deny')} type="button">Отклонить</button>
          <button className="button button--accent" disabled={busy} onClick={() => onDecision('approve')} type="button">
            {busy ? 'Сохраняю решение…' : 'Разрешить действие'}
          </button>
        </div>
      </section>
    </div>
  )
}

export function ChatView({ backend, onOpenSettings }: ChatViewProps) {
  const client = useMemo(() => createYuriClient(), [])
  const [conversations, setConversations] = useState<Conversation[]>([])
  const [selectedId, setSelectedId] = useState('')
  const [conversationFilter, setConversationFilter] = useState('')
  const [draft, setDraft] = useState('')
  const [runId, setRunId] = useState<string>()
  const [runStatus, setRunStatus] = useState<RunStatus>('idle')
  const [runLabel, setRunLabel] = useState(statusCopy.idle)
  const [pendingApproval, setPendingApproval] = useState<ApprovalRequest>()
  const [approvalBusy, setApprovalBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [loading, setLoading] = useState(true)
  const [transcribing, setTranscribing] = useState(false)
  const messagesEndRef = useRef<HTMLDivElement>(null)
  const transcribedBlobRef = useRef<Blob>()
  const voice = useVoice()
  const voiceBlob = voice.blob
  const clearVoice = voice.clear
  const tts = useTTS()

  const selectedConversation = conversations.find((conversation) => conversation.id === selectedId)
  const lastMessageContent = selectedConversation?.messages.at(-1)?.content
  const visibleConversations = conversations.filter((conversation) => {
    const query = conversationFilter.trim().toLocaleLowerCase('ru-RU')
    return !query || `${conversation.title} ${conversation.preview}`.toLocaleLowerCase('ru-RU').includes(query)
  })

  useEffect(() => {
    let mounted = true
    void client.listConversations().then(async (loaded) => {
      if (!mounted) return
      const available = loaded.length > 0 ? loaded : [await client.createConversation('Новый диалог')]
      if (!mounted) return
      setConversations(available)
      setSelectedId(available[0]?.id ?? '')
      setLoading(false)
    }).catch(() => {
      if (!mounted) return
      setError('Не удалось загрузить список диалогов.')
      setLoading(false)
    })
    return () => { mounted = false }
  }, [client])

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' })
  }, [selectedConversation?.messages.length, lastMessageContent])

  useEffect(() => {
    const blob = voiceBlob
    if (!blob || transcribedBlobRef.current === blob) return
    transcribedBlobRef.current = blob
    let active = true
    setTranscribing(true)
    setError(undefined)
    void client.transcribeAudio(blob).then((text) => {
      if (!active) return
      setDraft(text)
      clearVoice()
    }).catch((cause) => {
      if (active) setError(cause instanceof Error ? cause.message : 'Не удалось распознать голос.')
    }).finally(() => {
      if (active) setTranscribing(false)
    })
    return () => { active = false }
  }, [clearVoice, client, voiceBlob])

  const handleEvent = useCallback((conversationId: string, event: ChatEvent) => {
    if (event.type === 'run.started') {
      setRunId(event.runId)
      setRunStatus('thinking')
      setRunLabel(statusCopy.thinking)
      return
    }

    if (event.type === 'run.status') {
      setRunId(event.runId)
      setRunStatus(event.status)
      setRunLabel(event.label || statusCopy[event.status])
      return
    }

    if (event.type === 'assistant.delta') {
      setConversations((current) => updateConversation(current, conversationId, (conversation) => {
        const currentMessage = conversation.messages.find((message) => message.id === event.messageId)
        const message: ChatMessage = currentMessage ?? {
          id: event.messageId,
          role: 'assistant',
          content: '',
          status: 'streaming',
          createdAt: new Date().toISOString(),
          runId: event.runId,
        }
        return {
          ...conversation,
          updatedAt: new Date().toISOString(),
          messages: upsertMessage(conversation.messages, { ...message, content: message.content + event.delta, status: 'streaming' }),
        }
      }))
      return
    }

    if (event.type === 'assistant.completed') {
      setConversations((current) => updateConversation(current, conversationId, (conversation) => ({
        ...conversation,
        messages: conversation.messages.map((message) => message.id === event.messageId ? { ...message, status: 'complete' } : message),
      })))
      return
    }

    if (event.type === 'tool.started') {
      setRunStatus('tool_running')
      setRunLabel(`Инструмент: ${event.toolCall.name}`)
      setConversations((current) => updateConversation(current, conversationId, (conversation) => ({
        ...conversation,
        messages: upsertMessage(conversation.messages, {
          id: `tool-message-${event.toolCall.id}`,
          role: 'tool',
          content: '',
          status: 'streaming',
          createdAt: new Date().toISOString(),
          runId: event.runId,
          toolCall: event.toolCall,
        }),
      })))
      return
    }

    if (event.type === 'tool.updated') {
      setConversations((current) => updateConversation(current, conversationId, (conversation) => ({
        ...conversation,
        messages: conversation.messages.map((message) => message.toolCall?.id === event.toolCall.id
          ? { ...message, status: event.toolCall.status === 'completed' ? 'complete' : event.toolCall.status === 'denied' ? 'error' : message.status, toolCall: event.toolCall }
          : message),
      })))
      return
    }

    if (event.type === 'approval.required') {
      setPendingApproval(event.approval)
      setRunStatus('waiting_approval')
      setRunLabel(statusCopy.waiting_approval)
      return
    }

    if (event.type === 'run.completed') {
      setRunId(undefined)
      setRunStatus(event.status === 'complete' ? 'idle' : event.status)
      setRunLabel(event.status === 'complete' ? statusCopy.idle : event.status === 'cancelled' ? statusCopy.cancelled : event.error ?? statusCopy.error)
      if (event.status === 'error') setError(event.error ?? 'Запуск завершился ошибкой.')
    }
  }, [])

  const startRun = useCallback(async (text: string, retryOfMessageId?: string) => {
    const trimmed = text.trim()
    if (!trimmed || !selectedId || runId) return
    setError(undefined)
    const userMessage: ChatMessage = {
      id: makeId('user'),
      role: 'user',
      content: trimmed,
      status: 'complete',
      createdAt: new Date().toISOString(),
    }
    if (!retryOfMessageId) {
      setConversations((current) => updateConversation(current, selectedId, (conversation) => ({
        ...conversation,
        title: conversation.messages.length === 1 ? trimmed.slice(0, 36) : conversation.title,
        preview: trimmed,
        updatedAt: new Date().toISOString(),
        messages: [...conversation.messages, userMessage],
      })))
    }
    setDraft('')
    setRunStatus('thinking')
    setRunLabel(statusCopy.thinking)
    try {
      await (retryOfMessageId
        ? client.retryLast({ conversationId: selectedId, text: trimmed, retryOfMessageId }, (event) => handleEvent(selectedId, event))
        : client.sendMessage({ conversationId: selectedId, text: trimmed }, (event) => handleEvent(selectedId, event)))
    } catch (cause) {
      setRunId(undefined)
      setRunStatus('error')
      setRunLabel(statusCopy.error)
      setError(cause instanceof Error ? cause.message : 'Не удалось отправить сообщение.')
    }
  }, [client, handleEvent, runId, selectedId])

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    void startRun(draft)
  }

  const handleDraftKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
      event.preventDefault()
      void startRun(draft)
    }
  }

  const handleNewConversation = () => {
    void client.createConversation('Новый диалог').then((conversation) => {
      setConversations((current) => [conversation, ...current])
      setSelectedId(conversation.id)
      setDraft('')
      setError(undefined)
    }).catch(() => setError('Не удалось создать новый диалог.'))
  }

  const handleApproval = async (decision: 'approve' | 'deny') => {
    if (!pendingApproval) return
    setApprovalBusy(true)
    try {
      await client.approve(pendingApproval.id, decision)
      setPendingApproval(undefined)
    } finally {
      setApprovalBusy(false)
    }
  }

  const handleCancel = async () => {
    if (!runId) return
    setRunLabel('Останавливаем запуск…')
    await client.cancelRun(runId)
  }

  const handleRetry = (message: ChatMessage) => {
    const previousUser = selectedConversation?.messages
      .slice(0, selectedConversation.messages.findIndex((candidate) => candidate.id === message.id))
      .reverse()
      .find((candidate) => candidate.role === 'user')
    if (previousUser) void startRun(previousUser.content, message.id)
  }

  const handleVoice = () => {
    if (voice.state === 'recording') voice.stop()
    else if (voice.state === 'ready' || voice.state === 'error') {
      voice.clear()
      void voice.start()
    } else {
      void voice.start()
    }
  }

  return (
    <div className="chat-view chat-view--workspace">
      <div className="ambient-glow ambient-glow--one" />
      <div className="ambient-glow ambient-glow--two" />
      <div className="chat-layout">
        <aside aria-label="Список диалогов" className="conversation-panel">
          <div className="conversation-panel__header">
            <div>
              <span className="section-heading__overline">Workspace</span>
              <h1>Диалоги</h1>
            </div>
            <button aria-label="Создать новый диалог" className="round-button" onClick={handleNewConversation} type="button"><Icon name="plus" width={16} height={16} /></button>
          </div>
          <label className="conversation-search">
            <Icon name="search" width={15} height={15} />
            <span className="sr-only">Поиск диалогов</span>
            <input onChange={(event) => setConversationFilter(event.target.value)} placeholder="Найти диалог" value={conversationFilter} />
          </label>
          <div className="conversation-list" role="list">
            {loading && <div className="conversation-empty">Загружаю локальные диалоги…</div>}
            {!loading && visibleConversations.length === 0 && <div className="conversation-empty">Диалоги не найдены</div>}
            {visibleConversations.map((conversation) => (
              <button
                aria-current={conversation.id === selectedId ? 'true' : undefined}
                className={`conversation-item${conversation.id === selectedId ? ' conversation-item--active' : ''}`}
                key={conversation.id}
                onClick={() => setSelectedId(conversation.id)}
                role="listitem"
                type="button"
              >
                <span className="conversation-item__mark"><span /></span>
                <span className="conversation-item__copy">
                  <strong>{conversation.title}</strong>
                  <small>{conversation.preview || 'Пока нет сообщений'}</small>
                </span>
                <time dateTime={conversation.updatedAt}>{formatTime(conversation.updatedAt)}</time>
              </button>
            ))}
          </div>
          <div className="conversation-panel__footer">
            <span className={`client-mode client-mode--${client.mode}`}><span /> {client.mode === 'wails' ? 'Wails backend' : 'Локальный preview'}</span>
            <button className="text-button" onClick={onOpenSettings} type="button">Провайдеры <Icon name="chevron-right" width={13} height={13} /></button>
          </div>
        </aside>

        <section aria-label="Текущий диалог" className="chat-main">
          <header className="chat-main__header">
            <div>
              <span className="section-heading__overline">Conversation · local</span>
              <h2>{selectedConversation?.title ?? 'Новый диалог'}</h2>
            </div>
            <div className="chat-main__header-meta">
              <span className={`run-state run-state--${runStatus}`} role="status"><i /> {runLabel}</span>
              <span className="chat-main__privacy"><Icon name="lock" width={13} height={13} /> private</span>
            </div>
          </header>

          <div aria-live="polite" className="messages" role="log">
            {selectedConversation?.messages.length === 0 && (
              <div className="empty-conversation">
                <div className="empty-conversation__orb">Y</div>
                <h3>С чего начнём?</h3>
                <p>Напишите Yuri задачу. Ответ появится потоково, а рискованные действия будут показаны до выполнения.</p>
              </div>
            )}
            {selectedConversation?.messages.map((message) => (
              <MessageBubble
                key={message.id}
                message={message}
                onRetry={() => handleRetry(message)}
                onSpeak={() => tts.speak(message.id, message.content)}
                onStopSpeaking={tts.stop}
                speaking={tts.speakingId === message.id}
                speechSupported={tts.supported}
              />
            ))}
            <div ref={messagesEndRef} />
          </div>

          {error && (
            <div className="chat-error" role="alert">
              <Icon name="warning" width={15} height={15} />
              <span>{error}</span>
              <button aria-label="Закрыть уведомление об ошибке" className="icon-button icon-button--small" onClick={() => setError(undefined)} type="button"><Icon name="x" width={14} height={14} /></button>
            </div>
          )}

          <div className="composer-wrap composer-wrap--active">
            <form className="composer" onSubmit={handleSubmit}>
              <div className="composer__topline">
                <span className="composer__label">Новое сообщение</span>
                <span className="composer__mode"><span /> {backend.status === 'connected' ? 'Yuri · connected' : 'Yuri · local preview'}</span>
              </div>
              <textarea
                aria-label="Сообщение Yuri"
                className="composer__input"
                disabled={Boolean(runId)}
                onChange={(event) => setDraft(event.target.value)}
                onKeyDown={handleDraftKeyDown}
                placeholder="Напишите что-нибудь…"
                rows={2}
                value={draft}
              />
              <div className="composer__toolbar">
                <div className="composer__note-group">
                  <span className="composer__note">⌘/Ctrl + Enter · отправить</span>
                  {voice.state === 'recording' && <span className="voice-timer" aria-live="polite"><i /> {formatDuration(voice.durationMs)}</span>}
                  {transcribing && <span className="voice-ready">Yuri распознаёт голос…</span>}
                  {!transcribing && voice.state === 'ready' && <span className="voice-ready">Голосовой фрагмент записан{voice.blob ? ` · ${Math.max(1, Math.round(voice.blob.size / 1024))} KB` : ''}</span>}
                  {voice.error && <span className="voice-error" role="alert">{voice.error}</span>}
                </div>
                <div className="composer__actions">
                  <button
                    aria-label={voice.state === 'recording' ? 'Остановить запись' : 'Записать голосовое сообщение'}
                    aria-pressed={voice.state === 'recording'}
                    className={`voice-button${voice.state === 'recording' ? ' voice-button--recording' : ''}`}
                    disabled={Boolean(runId) || transcribing}
                    onClick={handleVoice}
                    title="Push-to-talk: запись с микрофона"
                    type="button"
                  >
                    <Icon name="mic" width={16} height={16} />
                  </button>
                  <button aria-label="Прикрепить файл" className="composer__attach" disabled title="Вложения подключатся в следующем инкременте" type="button">+</button>
                  {runId ? (
                    <button aria-label="Остановить запуск" className="stop-button" onClick={() => void handleCancel()} type="button"><Icon name="x" width={16} height={16} /></button>
                  ) : (
                    <button aria-label="Отправить сообщение" className="send-button" disabled={draft.trim() === '' || transcribing} type="submit"><Icon name="arrow-up" width={17} height={17} /></button>
                  )}
                </div>
              </div>
            </form>
            {backend.status !== 'connected' && (
              <button className="connection-callout" onClick={onOpenSettings} type="button">
                <span className="connection-callout__icon"><Icon name="settings" width={16} height={16} /></span>
                <span><strong>Локальный preview режим</strong><small>Подключите OpenAI-compatible endpoint или Codex App Server в Settings</small></span>
                <Icon name="chevron-right" width={15} height={15} />
              </button>
            )}
          </div>
        </section>
      </div>
      <section aria-label="Быстрые действия" className="chat-starters">
        <span className="chat-starters__label">Быстрый старт</span>
        {starterPrompts.map((prompt) => <button key={prompt} onClick={() => setDraft(prompt)} type="button">{prompt}<Icon name="arrow-up" width={13} height={13} /></button>)}
      </section>
      {pendingApproval && <ApprovalDialog approval={pendingApproval} busy={approvalBusy} onDecision={(decision) => void handleApproval(decision)} />}
    </div>
  )
}
