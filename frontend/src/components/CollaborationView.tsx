import { useCallback, useEffect, useMemo, useState } from 'react'

import { createYuriClient } from '../lib/client'
import type { PeerDialogue, PeerDialogueStatus, PeerRelationship, PeerRelationshipDetail, PeerRelationshipVersion } from '../lib/contracts'
import { formatDateTime } from '../lib/datetime'
import { Icon } from './Icon'

type Feedback = { kind: 'success' | 'error'; text: string }

const statusLabels: Record<PeerDialogueStatus, string> = {
  queued: 'в очереди',
  running: 'выполняется',
  cancelling: 'останавливается',
  completed: 'завершено',
  failed: 'ошибка',
  cancelled: 'отменено',
  expired: 'истекло',
  unknown: 'неизвестно',
}

function formatDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return formatDateTime(date)
}

function budgetLabel(used: number, max: number, unit: string): string {
  return max > 0 ? `${used.toLocaleString('ru-RU')} / ${max.toLocaleString('ru-RU')} ${unit}` : `${used.toLocaleString('ru-RU')} ${unit}`
}

function statusClass(status: PeerDialogueStatus): string {
  return `collaboration-status collaboration-status--${status}`
}

const relationshipDimensionLabels: Record<string, string> = {
  trust: 'Доверие',
  warmth: 'Теплота',
  familiarity: 'Знакомство',
  reliability: 'Надёжность',
  closeness: 'Близость',
  curiosity: 'Интерес',
}

const relationshipOperationLabels: Record<PeerRelationshipVersion['operation'], string> = {
  create: 'создание',
  update: 'рефлексия',
  rollback: 'откат',
  reset: 'сброс',
  unknown: 'изменение',
}

function percent(value: number): string {
  return `${Math.round(Math.max(0, Math.min(1, value)) * 100)}%`
}

function PeerRelationshipCard({ relationship, detail, busy, onOpen, onReset, onRollback }: {
  relationship: PeerRelationship
  detail?: PeerRelationshipDetail
  busy: boolean
  onOpen: () => void
  onReset: () => void
  onRollback: (version: PeerRelationshipVersion) => void
}) {
  const dimensions = Object.entries(relationship.dimensions)
  return <article className="peer-relationship-card">
    <header className="peer-relationship-card__header">
      <span className="collaboration-card__icon"><Icon name="relationship" width={17} height={17} /></span>
      <div><span>НАПРАВЛЕННОЕ МНЕНИЕ</span><h3>Активный агент <Icon name="chevron-right" width={12} height={12} /> {relationship.peerName}</h3></div>
      <span className="peer-relationship-card__version">v{relationship.version}</span>
    </header>
    <p className="peer-relationship-card__summary">{relationship.summary}</p>

    {dimensions.length > 0 && <div className="peer-relationship-dimensions" aria-label="Оценки отношения">
      {dimensions.map(([id, value]) => <div key={id}><span>{relationshipDimensionLabels[id] ?? id}</span><i><b style={{ width: percent(value) }} /></i><strong>{percent(value)}</strong></div>)}
    </div>}

    {relationship.opinions.length > 0
      ? <div className="peer-opinions" aria-label="Субъективные мнения">
        {relationship.opinions.map((opinion) => <blockquote key={opinion.id}><p>{opinion.content}</p><footer><span>{opinion.label === 'inference' ? 'вывод' : 'мнение, не факт'}</span><strong>уверенность {percent(opinion.confidence)}</strong></footer></blockquote>)}
      </div>
      : <p className="peer-opinions__empty">Субъективных мнений пока нет.</p>}

    <details className="peer-relationship-history" onToggle={(event) => { if (event.currentTarget.open) onOpen() }}>
      <summary><Icon name="chevron-right" width={13} height={13} /> История изменений <span>{detail?.versions.length ?? '…'}</span></summary>
      {!detail
        ? <div className="peer-relationship-history__loading"><span className="memory-spinner" /> Загружаю версии…</div>
        : <div className="peer-relationship-versions">
          {detail.versions.map((version) => <div className="peer-relationship-version" key={version.id}>
            <div><strong>v{version.version} · {relationshipOperationLabels[version.operation]}</strong><time dateTime={version.createdAt}>{formatDate(version.createdAt)}</time></div>
            <p>{version.summary}</p>
            <small>{version.reason}</small>
            {version.id !== relationship.currentVersionId && <button disabled={busy} onClick={() => onRollback(version)} type="button">Вернуть эту версию</button>}
          </div>)}
        </div>}
    </details>
    <footer className="peer-relationship-card__footer">
      <span>Обновлено {formatDate(relationship.updatedAt)}</span>
      <button disabled={busy || relationship.opinions.length === 0 && dimensions.length === 0} onClick={onReset} type="button">{busy ? 'Применяю…' : 'Сбросить мнение'}</button>
    </footer>
  </article>
}

function PeerDialogueCard({ dialogue, busy, onCancel }: {
  dialogue: PeerDialogue
  busy: boolean
  onCancel: (dialogue: PeerDialogue) => void
}) {
  const canCancel = dialogue.status === 'queued' || dialogue.status === 'running'
  return <article className={`collaboration-card collaboration-card--${dialogue.status}`}>
    <header className="collaboration-card__header">
      <div className="collaboration-card__identity">
        <span className="collaboration-card__icon"><Icon name="relationship" width={17} height={17} /></span>
        <div>
          <h3><span>{dialogue.initiatorName}</span><Icon name="chevron-right" width={12} height={12} /><span>{dialogue.peerName}</span></h3>
          <span>{formatDate(dialogue.createdAt)}</span>
        </div>
      </div>
      <span className={statusClass(dialogue.status)}><i />{statusLabels[dialogue.status]}</span>
    </header>

    <p className="collaboration-card__purpose">{dialogue.purpose}</p>

    <div className="collaboration-card__budget" aria-label="Бюджет диалога">
      <span><small>Ходы</small><strong>{budgetLabel(dialogue.turnCount, dialogue.maxTurns, '')}</strong></span>
      <span><small>Токены</small><strong>{budgetLabel(dialogue.tokensUsed, dialogue.maxTokens, '')}</strong></span>
      <span><small>Режим</small><strong>tools off</strong></span>
    </div>

    <details className="collaboration-card__transcript">
      <summary><Icon name="chevron-right" width={13} height={13} /> Сообщения <span>{dialogue.messages.length}</span></summary>
      {dialogue.messages.length === 0
        ? <p className="collaboration-transcript__empty">Первое сообщение ещё не обработано.</p>
        : <div className="collaboration-transcript" role="list" aria-label={`Сообщения ${dialogue.initiatorName} и ${dialogue.peerName}`}>
          {dialogue.messages.map((message) => <div className="collaboration-message" key={message.id} role="listitem">
            <div className="collaboration-message__meta"><strong>{message.senderName}</strong><span>→ {message.recipientName}</span><time dateTime={message.createdAt}>{formatDate(message.createdAt)}</time></div>
            <p>{message.content}</p>
          </div>)}
        </div>}
    </details>

    {dialogue.failure && <div className="collaboration-card__error"><Icon name="warning" width={13} height={13} /> {dialogue.failure}</div>}
    <footer className="collaboration-card__footer">
      <span>{dialogue.finishedAt ? `Завершено ${formatDate(dialogue.finishedAt)}` : 'Работает в фоне'}</span>
      {canCancel && <button className="collaboration-card__cancel" disabled={busy} onClick={() => onCancel(dialogue)} type="button">{busy ? 'Останавливаю…' : 'Остановить'}</button>}
    </footer>
  </article>
}

export function CollaborationView({ activeAgentId }: { activeAgentId?: string }) {
  const client = useMemo(() => createYuriClient(), [])
  const [dialogues, setDialogues] = useState<PeerDialogue[]>([])
  const [relationships, setRelationships] = useState<PeerRelationship[]>([])
  const [relationshipDetails, setRelationshipDetails] = useState<Record<string, PeerRelationshipDetail>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string>()
  const [feedback, setFeedback] = useState<Feedback>()
  const [busyIds, setBusyIds] = useState<Set<string>>(new Set())

  const load = useCallback(async () => {
    setLoading(true)
    setError(undefined)
    try {
      const [nextDialogues, nextRelationships] = await Promise.all([
        client.listPeerDialogues({ limit: 50 }),
        client.listPeerRelationships({ limit: 50 }),
      ])
      setDialogues(nextDialogues)
      setRelationships(nextRelationships)
      setRelationshipDetails({})
    } catch (cause) {
      setDialogues([])
      setRelationships([])
      setError(cause instanceof Error ? cause.message : 'Не удалось загрузить данные агентов.')
    } finally {
      setLoading(false)
    }
  }, [client])

  useEffect(() => { void load() }, [load, activeAgentId])

  const visibleDialogues = useMemo(() => {
    if (!activeAgentId) return dialogues
    return dialogues.filter((dialogue) => dialogue.initiatorAgentId === activeAgentId || dialogue.peerAgentId === activeAgentId)
  }, [activeAgentId, dialogues])

  const runningCount = visibleDialogues.filter((dialogue) => dialogue.status === 'queued' || dialogue.status === 'running' || dialogue.status === 'cancelling').length

  const markBusy = (id: string, value: boolean) => setBusyIds((current) => {
    const next = new Set(current)
    if (value) next.add(id)
    else next.delete(id)
    return next
  })

  const cancel = async (dialogue: PeerDialogue) => {
    markBusy(dialogue.id, true)
    setFeedback(undefined)
    try {
      await client.cancelPeerDialogue(dialogue.id)
      await load()
      setFeedback({ kind: 'success', text: 'Фоновый диалог остановлен.' })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось остановить диалог.' })
    } finally {
      markBusy(dialogue.id, false)
    }
  }

  const loadRelationshipDetail = async (peerAgentId: string) => {
    if (relationshipDetails[peerAgentId]) return
    try {
      const detail = await client.getPeerRelationship(peerAgentId)
      if (detail) setRelationshipDetails((current) => ({ ...current, [peerAgentId]: detail }))
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось загрузить историю отношения.' })
    }
  }

  const applyRelationshipChange = async (relationship: PeerRelationship, action: 'reset' | 'rollback', version?: PeerRelationshipVersion) => {
    markBusy(relationship.relationshipId, true)
    setFeedback(undefined)
    try {
      const detail = action === 'reset'
        ? await client.resetPeerRelationship(relationship.peerAgentId)
        : await client.rollbackPeerRelationship(relationship.peerAgentId, version?.id ?? '')
      if (!detail) throw new Error('Backend не вернул обновлённое отношение.')
      setRelationships((current) => current.map((item) => item.relationshipId === relationship.relationshipId ? detail.relationship : item))
      setRelationshipDetails((current) => ({ ...current, [relationship.peerAgentId]: detail }))
      setFeedback({ kind: 'success', text: action === 'reset' ? 'Мнение сброшено. Предыдущая версия сохранена в истории.' : `Восстановлена версия v${version?.version}.` })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось изменить отношение.' })
    } finally {
      markBusy(relationship.relationshipId, false)
    }
  }

  return <div className="collaboration-view">
    <div className="ambient-glow ambient-glow--one" />
    <div className="ambient-glow ambient-glow--two" />
    <header className="collaboration-view__hero">
      <div><span className="welcome-card__eyebrow"><span className="eyebrow-dot" /> YURI COLLABORATION</span><h1>Диалоги агентов<span className="title-dot">.</span></h1><p>Именованные агенты могут коротко советоваться в фоне. Здесь видны только участники, цель, бюджет и сохранённый transcript — без приватного контекста.</p></div>
      <div className="collaboration-view__metric"><strong>{loading ? '…' : String(visibleDialogues.length).padStart(2, '0')}</strong><span>{relationships.length} отношений · {runningCount} выполняется</span></div>
    </header>

    <section className="collaboration-toolbar"><div><span className="section-heading__overline">PRIVATE SOCIAL MODEL</span><h2>Отношения активного агента</h2></div></section>
    {!loading && !error && relationships.length === 0 && <div className="collaboration-state collaboration-state--compact"><Icon name="relationship" width={21} height={21} /><strong>Отношения ещё не сформированы</strong><span>Они могут появиться после прямого фонового диалога и принадлежат только наблюдающему агенту.</span></div>}
    {!loading && !error && relationships.length > 0 && <div className="peer-relationship-list">{relationships.map((relationship) => <PeerRelationshipCard
      busy={busyIds.has(relationship.relationshipId)}
      detail={relationshipDetails[relationship.peerAgentId]}
      key={relationship.relationshipId}
      onOpen={() => void loadRelationshipDetail(relationship.peerAgentId)}
      onReset={() => void applyRelationshipChange(relationship, 'reset')}
      onRollback={(version) => void applyRelationshipChange(relationship, 'rollback', version)}
      relationship={relationship}
    />)}</div>}

    <section className="collaboration-toolbar collaboration-toolbar--dialogues"><div><span className="section-heading__overline">BACKGROUND PEER RUNS</span><h2>Последние внутренние диалоги</h2></div><button aria-label="Обновить данные агентов" className="icon-button" disabled={loading} onClick={() => void load()} type="button"><Icon name="refresh" width={15} height={15} /></button></section>

    {loading && <div className="collaboration-state" role="status"><span className="memory-spinner" /> Загружаю фоновые диалоги…</div>}
    {error && <div className="tasks-feedback tasks-feedback--error" role="alert"><Icon name="warning" width={14} height={14} /> {error}</div>}
    {!loading && !error && visibleDialogues.length === 0 && <div className="collaboration-state collaboration-state--empty"><Icon name="relationship" width={23} height={23} /><strong>Внутренних диалогов пока нет</strong><span>Агенты начинают их сами, когда задача требует совета. Ручной запуск не предусмотрен.</span></div>}
    {!loading && !error && visibleDialogues.length > 0 && <div className="collaboration-list">{visibleDialogues.map((dialogue) => <PeerDialogueCard busy={busyIds.has(dialogue.id)} dialogue={dialogue} key={dialogue.id} onCancel={(item) => void cancel(item)} />)}</div>}

    {feedback && <div className={`tasks-feedback tasks-feedback--${feedback.kind}`} role={feedback.kind === 'error' ? 'alert' : 'status'}><Icon name={feedback.kind === 'success' ? 'check' : 'warning'} width={14} height={14} /> {feedback.text}<button aria-label="Закрыть уведомление" className="icon-button icon-button--small" onClick={() => setFeedback(undefined)} type="button"><Icon name="x" width={13} height={13} /></button></div>}
    <div className="collaboration-note"><span className="collaboration-note__icon"><Icon name="shield" width={16} height={16} /></span><div><strong>Ограниченный канал между агентами</strong><p>Каждый диалог имеет фиксированный лимит ходов и токенов, не получает tools и не может сам создать новый диалог. Остановить queued/running запуск можно здесь.</p></div></div>
  </div>
}
