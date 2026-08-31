import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'

import { createYuriClient, subscribeMemoryUpdates } from '../lib/client'
import type {
  ArchiveSearchResponse,
  ArchiveSearchResult,
  MemoryContentKind,
  MemoryKind,
  MemoryLifecycleState,
  MemoryListOptions,
  MemoryRecord,
  MemoryScope,
  MemorySource,
} from '../lib/contracts'
import { formatDateTime } from '../lib/datetime'
import { Icon } from './Icon'

type MemoryViewMode = 'memory' | 'archive'
type LifecycleFilter = 'active' | 'dormant' | 'all'

const lifecycleLabels: Record<MemoryLifecycleState, string> = {
  active: 'В активном контексте',
  dormant: 'В спящем режиме',
  deleted: 'Удалено',
}

const kindLabels: Record<MemoryKind, string> = {
  core: 'Core',
  user_model: 'Профиль пользователя',
  episodic: 'Эпизод',
  semantic: 'Знание',
  relationship: 'Отношения',
  procedural: 'Правило',
}

const contentKindLabels: Record<MemoryContentKind, string> = {
  fact: 'факт',
  opinion: 'мнение',
  emotion: 'эмоция',
  inference: 'вывод',
  fiction: 'вымышленное прошлое',
}

const fictionProvenanceLabels = {
  owner_seed: 'Исходник владельца',
  interpreted: 'Интерпретация агента',
  uncertain: 'Неуверенная версия',
} as const

const scopeLabels: Record<MemoryScope, string> = {
  agent_private: 'Личная память агента',
  owner_shared: 'Общее о владельце',
  installation_shared: 'Общее знание',
}

const scopeOptions: Array<{ value: MemoryListOptions['scope']; label: string }> = [
  { value: 'all', label: 'Любая видимость' },
  { value: 'agent_private', label: 'Личная' },
  { value: 'owner_shared', label: 'Общее о владельце' },
  { value: 'installation_shared', label: 'Общее знание' },
]

const kindOptions: Array<{ value: MemoryListOptions['kind']; label: string }> = [
  { value: 'all', label: 'Все типы' },
  { value: 'core', label: 'Core' },
  { value: 'user_model', label: 'Профиль пользователя' },
  { value: 'episodic', label: 'Эпизоды' },
  { value: 'semantic', label: 'Знания' },
  { value: 'relationship', label: 'Отношения' },
  { value: 'procedural', label: 'Правила' },
]

function formatDate(value?: string): string {
  if (!value) return 'дата не указана'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return formatDateTime(date)
}

function formatPercent(value: number): string {
  return `${Math.round(Math.max(0, Math.min(1, value)) * 100)}%`
}

function sourceLabel(source: MemorySource): string {
  if (source.conversationTitle) return source.conversationTitle
  if (source.sourceType === 'identity_seed') return 'Версия персонализации владельца'
  if (source.sourceType === 'fiction_interpretation') return 'Вспомненный fictional-эпизод'
  if (source.sourceType === 'peer_dialogue') return 'Межагентный диалог'
  if (source.sourceType === 'peer_dialogue_message') return 'Реплика агента'
  if (source.sourceType === 'message' && source.messageId) return `Сообщение ${source.messageId.slice(0, 8)}`
  if (source.sourceType) return source.sourceType
  return 'Источник'
}

function sourceDetail(source: MemorySource): string {
  if (source.excerpt) return source.excerpt
  if (source.conversationId) return `Диалог ${source.conversationId.slice(0, 8)}`
  if (source.sourceId) return source.sourceId
  return 'Provenance не указан'
}

function replaceRecord(records: MemoryRecord[], next: MemoryRecord): MemoryRecord[] {
  return records.map((record) => record.id === next.id ? next : record)
}

type MemoryCardProps = {
  memory: MemoryRecord
  busy: boolean
  onPin: (memory: MemoryRecord) => void
  onEdit: (memory: MemoryRecord, content: string) => void
  onLifecycle: (memory: MemoryRecord, state: MemoryLifecycleState) => void
  onScope: (memory: MemoryRecord, scope: MemoryScope) => void
  onDelete: (memory: MemoryRecord) => void
  onDisableBackstory: (memory: MemoryRecord) => void
  onRehydrateBackstory: (memory: MemoryRecord) => void
}

function MemoryCard({ memory, busy, onPin, onEdit, onLifecycle, onScope, onDelete, onDisableBackstory, onRehydrateBackstory }: MemoryCardProps) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(memory.content)

  useEffect(() => {
    if (!editing) setDraft(memory.content)
  }, [editing, memory.content])

  const saveEdit = () => {
    const content = draft.trim()
    if (!content || content === memory.content.trim()) {
      setEditing(false)
      setDraft(memory.content)
      return
    }
    onEdit(memory, content)
    setEditing(false)
  }

  const ownerSeed = memory.fiction?.provenance === 'owner_seed'

  return (
    <article className={`memory-card memory-card--${memory.lifecycleState}`}>
      <header className="memory-card__header">
        <div className="memory-card__heading">
          <span className="memory-card__mark"><Icon name={memory.lifecycleState === 'dormant' ? 'clock' : 'memory'} width={16} height={16} /></span>
          <div>
            <span className="memory-card__kind">{kindLabels[memory.kind]}</span>
            <span className="memory-card__subkind">{contentKindLabels[memory.contentKind]} · {lifecycleLabels[memory.lifecycleState]}</span>
          </div>
        </div>
        <button
          aria-label={memory.pinned ? 'Открепить воспоминание' : 'Закрепить воспоминание'}
          aria-pressed={memory.pinned}
          className={`memory-icon-button${memory.pinned ? ' memory-icon-button--active' : ''}`}
          disabled={busy || Boolean(memory.fiction)}
          onClick={() => onPin(memory)}
          title={memory.fiction ? 'Fictional memory загружается только по релевантности' : memory.pinned ? 'Открепить' : 'Закрепить в core context'}
          type="button"
        >
          <Icon name="shield" width={15} height={15} />
        </button>
      </header>

      {editing ? (
        <div className="memory-card__editor">
          <label>
            <span className="sr-only">Текст воспоминания</span>
            <textarea autoFocus onChange={(event) => setDraft(event.target.value)} rows={4} value={draft} />
          </label>
          <div className="memory-card__editor-actions">
            <button className="memory-button memory-button--quiet" disabled={busy} onClick={() => { setEditing(false); setDraft(memory.content) }} type="button">Отмена</button>
            <button className="memory-button memory-button--accent" disabled={busy || draft.trim() === ''} onClick={saveEdit} type="button">Сохранить</button>
          </div>
        </div>
      ) : (
        <p className="memory-card__content">{memory.content || 'Пустая запись'}</p>
      )}

      {memory.fiction && (
        <div className="memory-card__fiction" aria-label="Происхождение вымышленного воспоминания">
          <div>
            <span className={`memory-fiction-badge memory-fiction-badge--${memory.fiction.provenance}`}>{fictionProvenanceLabels[memory.fiction.provenance]}</span>
            {memory.fiction.recallState === 'remembered' && <span className="memory-fiction-badge memory-fiction-badge--remembered">Вспомнено агентом</span>}
          </div>
          <small>
            {ownerSeed
              ? `Owner seed${memory.fiction.episodeId ? ` · episode ${memory.fiction.episodeId}` : ''}. Агент может осмыслить его отдельно, но не переписать.`
              : `Производная версия${memory.fiction.sourceMemoryId ? ` · источник ${memory.fiction.sourceMemoryId.slice(0, 12)}` : ''}. Это субъективная интерпретация, не факт.`}
          </small>
        </div>
      )}

      <div className="memory-card__signals" aria-label="Сигналы памяти">
        <div className="memory-signal">
          <div className="memory-signal__label"><span>Уверенность</span><strong>{formatPercent(memory.confidence)}</strong></div>
          <div className="memory-meter"><span style={{ width: `${Math.round(memory.confidence * 100)}%` }} /></div>
        </div>
        <div className="memory-signal">
          <div className="memory-signal__label"><span>Значимость</span><strong>{formatPercent(memory.salience)}</strong></div>
          <div className="memory-meter memory-meter--mint"><span style={{ width: `${Math.round(memory.salience * 100)}%` }} /></div>
        </div>
      </div>

      <div className="memory-card__meta">
        <span>Обновлено {formatDate(memory.updatedAt)}</span>
        <span>{memory.accessCount} {memory.accessCount === 1 ? 'воспоминание' : 'вызовов'}</span>
      </div>

      <label className="memory-card__scope">
        <span><strong>Видимость</strong><small>{memory.scope === 'agent_private' ? `Только ${memory.agentName || 'этот агент'}` : 'Доступно всем локальным агентам'}</small></span>
        <select
          aria-label="Видимость воспоминания"
          disabled={busy || Boolean(memory.fiction)}
          onChange={(event) => onScope(memory, event.target.value as MemoryScope)}
          value={memory.scope}
        >
          {(scopeOptions.slice(1) as Array<{ value: MemoryScope; label: string }>).map((option) => <option key={option.value} value={option.value}>{scopeLabels[option.value]}</option>)}
        </select>
      </label>

      <div className="memory-card__provenance">
        <span className="memory-card__section-label">Источники</span>
        {memory.sources.length > 0 ? (
          <div className="provenance-list">
            {memory.sources.map((source, index) => (
              <details className="provenance-item" key={`${source.sourceId ?? source.sourceType}-${index}`}>
                <summary><Icon name="chevron-right" width={12} height={12} /><span>{sourceLabel(source)}</span></summary>
                <p>{sourceDetail(source)}</p>
              </details>
            ))}
          </div>
        ) : <span className="memory-card__no-source">Источник не указан</span>}
      </div>

      {memory.history.length > 0 && (
        <details className="memory-card__history">
          <summary><Icon name="chevron-right" width={12} height={12} />История происхождения · {memory.history.length}</summary>
          <ol>
            {memory.history.map((entry) => (
              <li key={`${entry.version}-${entry.operation}`}>
                <span>v{entry.version} · {entry.operation}</span>
                <small>{entry.reason || 'Изменение памяти'} · {formatDate(entry.createdAt)}</small>
              </li>
            ))}
          </ol>
        </details>
      )}

      <footer className="memory-card__actions">
        {!editing && (!ownerSeed || memory.lifecycleState !== 'deleted') && <button className="memory-button" disabled={busy} onClick={() => setEditing(true)} type="button">Изменить</button>}
        {ownerSeed && memory.lifecycleState !== 'deleted' && <button className="memory-button memory-button--danger" disabled={busy} onClick={() => onDisableBackstory(memory)} type="button">Отключить эпизод</button>}
        {ownerSeed && memory.lifecycleState === 'deleted' && <button className="memory-button memory-button--accent" disabled={busy} onClick={() => onRehydrateBackstory(memory)} type="button">Перегидратировать из backstory</button>}
        {!ownerSeed && memory.lifecycleState === 'active' && <button className="memory-button" disabled={busy} onClick={() => onLifecycle(memory, 'dormant')} type="button">Забыть</button>}
        {!ownerSeed && memory.lifecycleState === 'dormant' && <button className="memory-button memory-button--accent" disabled={busy} onClick={() => onLifecycle(memory, 'active')} type="button">Вспомнить</button>}
        {!ownerSeed && memory.lifecycleState === 'deleted' && <button className="memory-button memory-button--accent" disabled={busy} onClick={() => onLifecycle(memory, 'active')} type="button">Восстановить</button>}
        {!ownerSeed && <button className="memory-button memory-button--danger" disabled={busy} onClick={() => onDelete(memory)} type="button">Удалить</button>}
      </footer>
    </article>
  )
}

function ArchiveResultCard({ result }: { result: ArchiveSearchResult }) {
  return (
    <article className="archive-result">
      <div className="archive-result__heading">
        <div>
          <span className="archive-result__source"><Icon name="chat" width={14} height={14} /> {result.conversationTitle || result.conversationId || 'Диалог без названия'}</span>
          <span className="archive-result__meta">{result.role === 'user' ? 'Вы' : result.role === 'assistant' ? 'Yuri' : result.role || 'сообщение'}{result.createdAt ? ` · ${formatDate(result.createdAt)}` : ''}</span>
        </div>
        {result.score !== undefined && <span className="archive-result__score">{Math.round(result.score * 100)}%</span>}
      </div>
      <p>{result.snippet || result.content}</p>
      <div className="archive-result__provenance">
        <span>{result.messageId ? `Сообщение ${result.messageId.slice(0, 8)}` : 'Архивное сообщение'}</span>
        {result.matchType && <span>{result.matchType}</span>}
      </div>
    </article>
  )
}

function ArchiveSearch({ client }: { client: ReturnType<typeof createYuriClient> }) {
  const [query, setQuery] = useState('')
  const [includeDormant, setIncludeDormant] = useState(true)
  const [response, setResponse] = useState<ArchiveSearchResponse>()
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string>()

  const search = async () => {
    const trimmed = query.trim()
    if (!trimmed) {
      setResponse(undefined)
      setError('Введите, что нужно вспомнить из архива.')
      return
    }
    setLoading(true)
    setError(undefined)
    try {
      const result = await client.searchArchive({ query: trimmed, includeDormant, limit: 40 })
      setResponse(result)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Не удалось выполнить поиск по архиву.')
    } finally {
      setLoading(false)
    }
  }

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    void search()
  }

  return (
    <section className="archive-view" aria-labelledby="archive-title">
      <div className="memory-view__topline">
        <div>
          <span className="welcome-card__eyebrow"><span className="eyebrow-dot" /> SESSION ARCHIVE</span>
          <h2 id="archive-title">Поиск по прошлым диалогам<span className="title-dot">.</span></h2>
          <p>Целенаправленный поиск возвращает исходные сообщения с provenance. Найденное воспоминание не становится фактом автоматически.</p>
        </div>
        <span className="stage-pill">FTS · VECTOR · PROVENANCE</span>
      </div>

      <form className="archive-search-form" onSubmit={handleSubmit}>
        <label className="archive-search-input">
          <Icon name="search" width={16} height={16} />
          <span className="sr-only">Поиск по архиву</span>
          <input onChange={(event) => setQuery(event.target.value)} placeholder="Что нужно вспомнить? Например: решение по проекту" value={query} />
        </label>
        <label className="archive-toggle">
          <input checked={includeDormant} onChange={(event) => setIncludeDormant(event.target.checked)} type="checkbox" />
          <span>Включая спящие записи</span>
        </label>
        <button className="button button--accent" disabled={loading || query.trim() === ''} type="submit">{loading ? 'Ищу…' : 'Искать'}</button>
      </form>

      {error && <div className="memory-feedback memory-feedback--error" role="alert"><Icon name="warning" width={14} height={14} /> {error}</div>}
      {loading && <div className="memory-state memory-state--loading"><span className="memory-spinner" /> Ищу в архиве диалогов…</div>}
      {!loading && response && response.results.length === 0 && <div className="memory-state memory-state--empty"><Icon name="search" width={20} height={20} /><strong>Ничего не найдено</strong><span>Попробуйте более короткий запрос или включите спящие записи.</span></div>}
      {!loading && response && response.results.length > 0 && (
        <div className="archive-results" role="list" aria-label="Результаты поиска по архиву">
          <div className="archive-results__summary">Найдено: {response.total ?? response.results.length}</div>
          {response.results.map((result) => <ArchiveResultCard key={result.id} result={result} />)}
        </div>
      )}
      {!loading && !response && !error && <div className="memory-state memory-state--empty"><Icon name="chat" width={20} height={20} /><strong>Архив ещё не запрошен</strong><span>Введите запрос, чтобы Yuri нашла нужный фрагмент среди всех сессий.</span></div>}
    </section>
  )
}

export function MemoryView() {
  const client = useMemo(() => createYuriClient(), [])
  const [mode, setMode] = useState<MemoryViewMode>('memory')
  const [lifecycle, setLifecycle] = useState<LifecycleFilter>('active')
  const [kind, setKind] = useState<MemoryListOptions['kind']>('all')
  const [scope, setScope] = useState<MemoryListOptions['scope']>('all')
  const [query, setQuery] = useState('')
  const [records, setRecords] = useState<MemoryRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string>()
  const [refreshKey, setRefreshKey] = useState(0)
  const [busyIds, setBusyIds] = useState<Set<string>>(new Set())
  const requestId = useRef(0)

  const load = useCallback(async () => {
    const currentRequest = ++requestId.current
    setLoading(true)
    setError(undefined)
    try {
      const result = await client.listMemories({
        lifecycleState: lifecycle,
        kind,
        scope,
        query: query.trim() || undefined,
        limit: 80,
      })
      if (currentRequest !== requestId.current) return
      setRecords(result)
    } catch (cause) {
      if (currentRequest !== requestId.current) return
      setError(cause instanceof Error ? cause.message : 'Не удалось загрузить память Yuri.')
      setRecords([])
    } finally {
      if (currentRequest === requestId.current) setLoading(false)
    }
  }, [client, kind, lifecycle, query, scope])

  useEffect(() => {
    const timer = globalThis.setTimeout(() => { void load() }, query.trim() ? 220 : 0)
    return () => globalThis.clearTimeout(timer)
  }, [load, query, refreshKey])

  useEffect(() => subscribeMemoryUpdates(() => setRefreshKey((current) => current + 1)), [])

  const runMutation = async (id: string, action: () => Promise<MemoryRecord | undefined>) => {
    setBusyIds((current) => new Set(current).add(id))
    setError(undefined)
    try {
      const next = await action()
      if (next) {
        setRecords((current) => replaceRecord(current, next))
      } else {
        setRefreshKey((current) => current + 1)
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Не удалось изменить запись памяти.')
    } finally {
      setBusyIds((current) => {
        const next = new Set(current)
        next.delete(id)
        return next
      })
    }
  }

  const handlePin = (memory: MemoryRecord) => {
    void runMutation(memory.id, () => client.updateMemory(memory.id, { pinned: !memory.pinned }))
  }

  const handleEdit = (memory: MemoryRecord, content: string) => {
    void runMutation(memory.id, () => memory.fiction?.provenance === 'owner_seed'
      ? client.updateBackstoryMemory(memory.id, content)
      : client.updateMemory(memory.id, { content }))
  }

  const handleLifecycle = (memory: MemoryRecord, state: MemoryLifecycleState) => {
    void runMutation(memory.id, () => client.setMemoryLifecycle(memory.id, state))
  }

  const handleScope = (memory: MemoryRecord, nextScope: MemoryScope) => {
    if (nextScope === memory.scope) return
    if (nextScope !== 'agent_private') {
      const confirmed = globalThis.confirm(`Открыть это воспоминание всем локальным агентам?\n\n${memory.content.slice(0, 160)}`)
      if (!confirmed) return
    }
    void runMutation(memory.id, () => client.setMemoryScope(memory.id, nextScope))
  }

  const handleDelete = (memory: MemoryRecord) => {
    const confirmed = globalThis.confirm(`Удалить запись памяти?\n\n${memory.content.slice(0, 160)}`)
    if (!confirmed) return
    void runMutation(memory.id, async () => {
      await client.deleteMemory(memory.id)
      return undefined
    })
  }

  const handleDisableBackstory = (memory: MemoryRecord) => {
    const confirmed = globalThis.confirm(`Отключить этот эпизод backstory? Он перестанет вспоминаться, но останется в owner seed и истории.\n\n${memory.content.slice(0, 160)}`)
    if (!confirmed) return
    void runMutation(memory.id, () => client.disableBackstoryMemory(memory.id))
  }

  const handleRehydrateBackstory = (memory: MemoryRecord) => {
    void runMutation(memory.id, () => client.rehydrateBackstoryMemory(memory.id))
  }

  const visibleCountLabel = loading ? '…' : String(records.length).padStart(2, '0')

  return (
    <div className="memory-view">
      <div className="ambient-glow ambient-glow--one" />
      <div className="ambient-glow ambient-glow--two" />
      <header className="memory-view__hero">
        <div>
          <span className="welcome-card__eyebrow"><span className="eyebrow-dot" /> YURI MEMORY SYSTEM</span>
          <h1>Память<span className="title-dot">.</span></h1>
          <p>Общий контекст Yuri для всех диалогов. Активное ядро компактно, а полная история остаётся в архиве и подгружается только по необходимости.</p>
        </div>
        <div className="memory-view__metric"><strong>{visibleCountLabel}</strong><span>{mode === 'memory' ? 'записей в выборке' : 'архивный поиск'}</span></div>
      </header>

      <div className="memory-tabs" role="tablist" aria-label="Память и архив">
        <button aria-selected={mode === 'memory'} className={mode === 'memory' ? 'memory-tab memory-tab--active' : 'memory-tab'} onClick={() => setMode('memory')} role="tab" type="button"><Icon name="memory" width={15} height={15} /> Память</button>
        <button aria-selected={mode === 'archive'} className={mode === 'archive' ? 'memory-tab memory-tab--active' : 'memory-tab'} onClick={() => setMode('archive')} role="tab" type="button"><Icon name="search" width={15} height={15} /> Архив сессий</button>
      </div>

      {mode === 'archive' ? <ArchiveSearch client={client} /> : (
        <section className="memory-list-view" aria-labelledby="memory-list-title">
          <div className="memory-toolbar">
            <div className="memory-toolbar__heading">
              <div>
                <span className="section-heading__overline">LIVE MEMORY STATE</span>
                <h2 id="memory-list-title">Что Yuri держит в уме</h2>
              </div>
              <span className="section-heading__count">{visibleCountLabel} records</span>
            </div>
            <div className="memory-filters">
              <label className="memory-search">
                <Icon name="search" width={14} height={14} />
                <span className="sr-only">Фильтр памяти</span>
                <input onChange={(event) => setQuery(event.target.value)} placeholder="Фильтр по содержимому" value={query} />
              </label>
              <label className="memory-select-label">
                <span className="sr-only">Тип памяти</span>
                <select onChange={(event) => setKind(event.target.value as MemoryListOptions['kind'])} value={kind}>
                  {kindOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
                </select>
              </label>
              <label className="memory-select-label">
                <span className="sr-only">Видимость памяти</span>
                <select onChange={(event) => setScope(event.target.value as MemoryListOptions['scope'])} value={scope}>
                  {scopeOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
                </select>
              </label>
            </div>
          </div>

          <div className="memory-lifecycle-tabs" role="tablist" aria-label="Состояние памяти">
            {(['active', 'dormant', 'all'] as const).map((state) => (
              <button aria-selected={lifecycle === state} className={lifecycle === state ? 'lifecycle-tab lifecycle-tab--active' : 'lifecycle-tab'} key={state} onClick={() => setLifecycle(state)} role="tab" type="button">
                {state === 'active' ? 'Активные' : state === 'dormant' ? 'Спящие' : 'Все'}
              </button>
            ))}
          </div>

          {error && <div className="memory-feedback memory-feedback--error" role="alert"><Icon name="warning" width={14} height={14} /> {error}<button aria-label="Закрыть ошибку" className="icon-button icon-button--small" onClick={() => setError(undefined)} type="button"><Icon name="x" width={13} height={13} /></button></div>}
          {loading && <div className="memory-state memory-state--loading"><span className="memory-spinner" /> Загружаю память Yuri…</div>}
          {!loading && !error && records.length === 0 && <div className="memory-state memory-state--empty"><Icon name="memory" width={22} height={22} /><strong>{query ? 'Ничего не найдено' : lifecycle === 'dormant' ? 'Спящих записей нет' : 'Память пока пуста'}</strong><span>{query ? 'Измените фильтр или выполните поиск по архиву сессий.' : 'После содержательного диалога Yuri сама выберет материал, который может пригодиться в будущем.'}</span></div>}
          {!loading && records.length > 0 && <div className="memory-grid">{records.map((memory) => <MemoryCard busy={busyIds.has(memory.id)} key={memory.id} memory={memory} onDelete={handleDelete} onDisableBackstory={handleDisableBackstory} onEdit={handleEdit} onLifecycle={handleLifecycle} onPin={handlePin} onRehydrateBackstory={handleRehydrateBackstory} onScope={handleScope} />)}</div>}
        </section>
      )}
    </div>
  )
}
