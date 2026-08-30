import { useCallback, useEffect, useMemo, useState } from 'react'

import { createYuriClient, requestBrowserNotificationPermission } from '../lib/client'
import type {
  ActivityEvent,
  ActivityStatus,
  ActivityType,
  ProactivitySettings,
} from '../lib/contracts'
import { formatDateTime } from '../lib/datetime'
import { Icon } from './Icon'

type Feedback = { kind: 'success' | 'error'; text: string }

const typeLabels: Record<ActivityType, string> = {
  job: 'Фоновые задачи',
  proactive: 'Проактивность',
  system: 'Система',
  reflection: 'Рефлексия',
  memory: 'Память',
  unknown: 'Другое',
}

const statusLabels: Record<ActivityStatus, string> = {
  queued: 'в очереди',
  running: 'выполняется',
  completed: 'завершено',
  failed: 'ошибка',
  cancelled: 'отменено',
  skipped: 'пропущено',
  blocked: 'заблокировано',
  info: 'информация',
  unknown: 'неизвестно',
}

const defaultSettings: ProactivitySettings = {
  enabled: false,
  quietHoursEnabled: true,
  quietHoursStart: '23:00',
  quietHoursEnd: '07:00',
  timezone: 'Europe/Moscow',
  dailyLimit: 5,
  cooldownMinutes: 30,
  allowLocalNotifications: true,
}

const timezones = ['Europe/Moscow', 'UTC', 'Europe/London', 'Europe/Berlin', 'America/New_York', 'America/Los_Angeles', 'Asia/Tokyo', 'Asia/Singapore']

function formatDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return formatDateTime(date)
}

function formatDuration(durationMs?: number): string {
  if (durationMs === undefined) return ''
  if (durationMs < 1000) return durationMs + ' мс'
  return (durationMs / 1000).toFixed(durationMs >= 10000 ? 0 : 1) + ' с'
}

function ActivityRow({ event }: { event: ActivityEvent }) {
  return <article className="activity-row">
    <span className={'activity-row__icon activity-row__icon--' + event.type}><Icon name={event.type === 'job' ? 'tasks' : event.type === 'proactive' ? 'spark' : event.type === 'memory' ? 'memory' : event.type === 'reflection' ? 'relationship' : 'activity'} width={16} height={16} /></span>
    <div className="activity-row__body"><div className="activity-row__heading"><strong>{event.title}</strong><span className={'activity-status activity-status--' + event.status}><i /> {statusLabels[event.status]}</span></div>{event.detail && <p>{event.detail}</p>}<div className="activity-row__meta"><span>{typeLabels[event.type]}</span>{event.source && <span>{event.source}</span>}{event.reason && <span>Причина: {event.reason}</span>}{event.provenance && <span>Источник: {event.provenance}</span>}</div></div>
    <div className="activity-row__time"><time dateTime={event.createdAt}>{formatDate(event.createdAt)}</time>{event.durationMs !== undefined && <small>{formatDuration(event.durationMs)}</small>}</div>
  </article>
}

function ProactivitySettingsCard({ settings, saving, onChange, onSave, onEnableLocalNotifications }: {
  settings: ProactivitySettings
  saving: boolean
  onChange: (next: ProactivitySettings) => void
  onSave: () => void
  onEnableLocalNotifications: () => void
}) {
  const update = <K extends keyof ProactivitySettings>(key: K, value: ProactivitySettings[K]) => onChange({ ...settings, [key]: value })
  return <section className="proactivity-card" aria-labelledby="proactivity-title">
    <div className="proactivity-card__heading"><div><span className="section-heading__overline">PROACTIVE POLICY</span><h2 id="proactivity-title">Правила инициативы</h2></div><span className={'proactivity-state' + (settings.enabled ? ' proactivity-state--on' : '')}><i /> {settings.enabled ? 'разрешено' : 'выключено'}</span></div>
    <p className="proactivity-card__lead">Yuri может сама начать разговор только по разрешённому триггеру. Эти ограничения применяются перед каждым уведомлением и новым сообщением.</p>
    <div className="proactivity-setting-list">
      <label className="proactivity-toggle"><button aria-checked={settings.enabled} className={'toggle' + (settings.enabled ? ' toggle--on' : '')} onClick={() => update('enabled', !settings.enabled)} role="switch" type="button"><i /></button><span><strong>Разрешить проактивность</strong><small>Глобальный выключатель для фоновых и событийных сообщений.</small></span></label>
      <label className="proactivity-toggle"><button aria-checked={settings.allowLocalNotifications} className={'toggle' + (settings.allowLocalNotifications ? ' toggle--on' : '')} onClick={() => { if (!settings.allowLocalNotifications) onEnableLocalNotifications(); update('allowLocalNotifications', !settings.allowLocalNotifications) }} role="switch" type="button"><i /></button><span><strong>Локальные уведомления</strong><small>Показывать системное уведомление после завершения фоновой задачи.</small></span></label>
      <label className="proactivity-toggle"><button aria-checked={settings.quietHoursEnabled} className={'toggle' + (settings.quietHoursEnabled ? ' toggle--on' : '')} onClick={() => update('quietHoursEnabled', !settings.quietHoursEnabled)} role="switch" type="button"><i /></button><span><strong>Тихие часы</strong><small>Не начинать новые проактивные сообщения в заданном окне.</small></span></label>
    </div>
    <div className="proactivity-form">
      <label><span>Начало quiet hours</span><input disabled={!settings.quietHoursEnabled} onChange={(event) => update('quietHoursStart', event.target.value)} type="time" value={settings.quietHoursStart} /></label>
      <label><span>Конец quiet hours</span><input disabled={!settings.quietHoursEnabled} onChange={(event) => update('quietHoursEnd', event.target.value)} type="time" value={settings.quietHoursEnd} /></label>
      <label><span>Часовой пояс</span><select onChange={(event) => update('timezone', event.target.value)} value={timezones.includes(settings.timezone) ? settings.timezone : ''}><option disabled value="">{settings.timezone}</option>{timezones.map((zone) => <option key={zone} value={zone}>{zone}</option>)}</select></label>
      <label><span>Лимит сообщений в день</span><input min={0} onChange={(event) => update('dailyLimit', Math.max(0, Number(event.target.value) || 0))} type="number" value={settings.dailyLimit} /></label>
      <label><span>Cooldown, минут</span><input min={0} onChange={(event) => update('cooldownMinutes', Math.max(0, Number(event.target.value) || 0))} type="number" value={settings.cooldownMinutes} /></label>
    </div>
    <div className="proactivity-card__footer"><span><Icon name="shield" width={13} height={13} /> Негативный affect не может обходить policy.</span><button className="button button--accent" disabled={saving} onClick={onSave} type="button">{saving ? 'Сохраняю…' : 'Сохранить правила'}</button></div>
  </section>
}

export function ActivityView() {
  const client = useMemo(() => createYuriClient(), [])
  const [settings, setSettings] = useState<ProactivitySettings>(defaultSettings)
  const [events, setEvents] = useState<ActivityEvent[]>([])
  const [filter, setFilter] = useState<ActivityType | 'all'>('all')
  const [statusFilter, setStatusFilter] = useState<ActivityStatus | 'all'>('all')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string>()
  const [feedback, setFeedback] = useState<Feedback>()

  const load = useCallback(async () => {
    setLoading(true)
    setError(undefined)
    try {
      const [nextSettings, nextEvents] = await Promise.all([client.getProactivitySettings(), client.listActivity({ limit: 100 })])
      setSettings(nextSettings)
      setEvents(nextEvents)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Не удалось загрузить Activity stream.')
      setEvents([])
    } finally {
      setLoading(false)
    }
  }, [client])

  useEffect(() => { void load() }, [load])

  const saveSettings = async () => {
    setSaving(true)
    setFeedback(undefined)
    try {
      await client.saveProactivitySettings(settings)
      setFeedback({ kind: 'success', text: 'Правила проактивности сохранены.' })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось сохранить правила проактивности.' })
    } finally {
      setSaving(false)
    }
  }

  const enableLocalNotifications = () => {
    void requestBrowserNotificationPermission().then((permission) => {
      if (permission === 'denied') {
        setFeedback({ kind: 'error', text: 'Системные уведомления запрещены браузером. Встроенные уведомления Yuri продолжат работать.' })
      }
    })
  }

  const filteredEvents = useMemo(() => events.filter((event) => (filter === 'all' || event.type === filter) && (statusFilter === 'all' || event.status === statusFilter)), [events, filter, statusFilter])
  const completedCount = events.filter((event) => event.status === 'completed').length
  const proactiveCount = events.filter((event) => event.type === 'proactive').length
  const countLabel = loading ? '…' : String(events.length).padStart(2, '0')

  return <div className="activity-view">
    <div className="ambient-glow ambient-glow--one" />
    <div className="ambient-glow ambient-glow--two" />
    <header className="activity-view__hero"><div><span className="welcome-card__eyebrow"><span className="eyebrow-dot" /> YURI ACTIVITY STREAM</span><h1>Активность<span className="title-dot">.</span></h1><p>Здесь видны фоновые запуски, проактивные решения и policy gates. Yuri объясняет, почему она начала или отложила действие.</p></div><div className="activity-view__metric"><strong>{countLabel}</strong><span>{completedCount} завершено · {proactiveCount} proactive</span></div></header>
    <div className="activity-layout">
      <div className="activity-main">
        <section className="activity-toolbar"><div><span className="section-heading__overline">APPEND-ONLY STREAM</span><h2>Последние события</h2></div><button aria-label="Обновить Activity" className="icon-button" onClick={() => void load()} type="button"><Icon name="refresh" width={15} height={15} /></button></section>
        <div className="activity-filters"><div className="activity-filter-group" role="tablist" aria-label="Тип события">{(['all', 'job', 'proactive', 'system', 'reflection', 'memory'] as const).map((type) => <button aria-selected={filter === type} className={filter === type ? 'activity-filter activity-filter--active' : 'activity-filter'} key={type} onClick={() => setFilter(type)} role="tab" type="button">{type === 'all' ? 'Все' : typeLabels[type]}</button>)}</div><label className="activity-status-filter"><span>Статус</span><select onChange={(event) => setStatusFilter(event.target.value as ActivityStatus | 'all')} value={statusFilter}><option value="all">Все</option>{(['running', 'completed', 'failed', 'skipped', 'blocked', 'cancelled', 'info'] as ActivityStatus[]).map((status) => <option key={status} value={status}>{statusLabels[status]}</option>)}</select></label></div>
        {loading && <div className="activity-state" role="status"><span className="memory-spinner" /> Загружаю activity stream…</div>}
        {error && <div className="tasks-feedback tasks-feedback--error" role="alert"><Icon name="warning" width={14} height={14} /> {error}</div>}
        {!loading && !error && filteredEvents.length === 0 && <div className="activity-state activity-state--empty"><Icon name="activity" width={23} height={23} /><strong>Событий по этому фильтру нет</strong><span>Когда Yuri выполнит задачу или применит policy gate, запись появится здесь.</span></div>}
        {!loading && !error && filteredEvents.length > 0 && <div className="activity-list">{filteredEvents.map((event) => <ActivityRow event={event} key={event.id} />)}</div>}
      </div>
      <ProactivitySettingsCard onChange={setSettings} onEnableLocalNotifications={enableLocalNotifications} onSave={() => void saveSettings()} saving={saving} settings={settings} />
    </div>
    {feedback && <div className={'tasks-feedback tasks-feedback--' + feedback.kind} role={feedback.kind === 'error' ? 'alert' : 'status'}><Icon name={feedback.kind === 'success' ? 'check' : 'warning'} width={14} height={14} /> {feedback.text}<button aria-label="Закрыть уведомление" className="icon-button icon-button--small" onClick={() => setFeedback(undefined)} type="button"><Icon name="x" width={13} height={13} /></button></div>}
    <div className="activity-note"><span className="activity-note__icon"><Icon name="shield" width={16} height={16} /></span><div><strong>Activity — объяснимый журнал</strong><p>События содержат источник, причину и provenance. Содержимое секретов и чувствительных данных в журнал не попадает.</p></div></div>
  </div>
}
