import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react'

import { createYuriClient } from '../lib/client'
import { manualRunFeedback } from '../lib/scheduler-ui'
import type {
  DeliveryChannel,
  JobRun,
  MisfirePolicy,
  Schedule,
  ScheduleInput,
  ScheduleType,
} from '../lib/contracts'
import { Icon } from './Icon'

type Feedback = { kind: 'success' | 'info' | 'error'; text: string }

type TaskFormState = {
  title: string
  prompt: string
  type: ScheduleType
  runAt: string
  intervalMinutes: string
  expression: string
  timezone: string
  misfirePolicy: MisfirePolicy
  deliveryChannel: DeliveryChannel
  maxDurationSeconds: string
  maxTokens: string
  maxToolCalls: string
}

const timezones = [
  'Europe/Moscow',
  'UTC',
  'Europe/London',
  'Europe/Berlin',
  'America/New_York',
  'America/Los_Angeles',
  'Asia/Tokyo',
  'Asia/Singapore',
]

const typeLabels: Record<ScheduleType, string> = {
  once: 'Однократно',
  interval: 'Интервал',
  cron: 'CRON',
}

const statusLabels: Record<Schedule['status'], string> = {
  active: 'активна',
  paused: 'пауза',
  completed: 'завершена',
  error: 'ошибка',
  unknown: 'неизвестно',
}

const runStatusLabels: Record<JobRun['status'], string> = {
  queued: 'в очереди',
  running: 'выполняется',
  completed: 'завершено',
  failed: 'ошибка',
  cancelled: 'отменено',
  skipped: 'пропущено',
  unknown: 'неизвестно',
}

function localDateTimeValue(value?: string): string {
  if (!value) {
    const date = new Date(Date.now() + 60 * 60 * 1000)
    const offset = date.getTimezoneOffset() * 60000
    return new Date(date.getTime() - offset).toISOString().slice(0, 16)
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value.slice(0, 16)
  const offset = date.getTimezoneOffset() * 60000
  return new Date(date.getTime() - offset).toISOString().slice(0, 16)
}

function formatDate(value?: string): string {
  if (!value) return 'не запланировано'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat('ru-RU', { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

function formatDuration(durationMs?: number): string {
  if (durationMs === undefined) return '—'
  if (durationMs < 1000) return durationMs + ' мс'
  return (durationMs / 1000).toFixed(durationMs >= 10000 ? 0 : 1) + ' с'
}

function emptyForm(): TaskFormState {
  return {
    title: '',
    prompt: '',
    type: 'cron',
    runAt: localDateTimeValue(),
    intervalMinutes: '60',
    expression: '0 9 * * 1-5',
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'Europe/Moscow',
    misfirePolicy: 'run_once',
    deliveryChannel: 'in_app',
    maxDurationSeconds: '180',
    maxTokens: '1800',
    maxToolCalls: '8',
  }
}

function scheduleToForm(schedule: Schedule): TaskFormState {
  return {
    title: schedule.title,
    prompt: schedule.prompt,
    type: schedule.type,
    runAt: localDateTimeValue(schedule.runAt),
    intervalMinutes: schedule.intervalSeconds ? String(Math.max(1, Math.round(schedule.intervalSeconds / 60))) : '60',
    expression: schedule.expression || '0 9 * * 1-5',
    timezone: schedule.timezone,
    misfirePolicy: schedule.misfirePolicy,
    deliveryChannel: schedule.deliveryChannel,
    maxDurationSeconds: String(schedule.budget?.maxDurationSeconds ?? 180),
    maxTokens: String(schedule.budget?.maxTokens ?? 1800),
    maxToolCalls: String(schedule.budget?.maxToolCalls ?? 8),
  }
}

function numberOrUndefined(value: string): number | undefined {
  const numeric = Number(value)
  return Number.isFinite(numeric) && numeric > 0 ? Math.round(numeric) : undefined
}

function formToInput(form: TaskFormState, id?: string): ScheduleInput {
  const runAt = form.type === 'once' && form.runAt ? new Date(form.runAt).toISOString() : undefined
  return {
    id,
    title: form.title.trim(),
    prompt: form.prompt.trim(),
    type: form.type,
    runAt,
    intervalSeconds: form.type === 'interval' ? Math.max(60, (Number(form.intervalMinutes) || 0) * 60) : undefined,
    expression: form.type === 'cron' ? form.expression.trim() : undefined,
    timezone: form.timezone,
    misfirePolicy: form.misfirePolicy,
    deliveryChannel: form.deliveryChannel,
    budget: {
      maxDurationSeconds: numberOrUndefined(form.maxDurationSeconds),
      maxTokens: numberOrUndefined(form.maxTokens),
      maxToolCalls: numberOrUndefined(form.maxToolCalls),
    },
  }
}

function scheduleDescription(schedule: Schedule): string {
  if (schedule.type === 'once') return 'Один раз · ' + formatDate(schedule.runAt)
  if (schedule.type === 'interval') {
    const minutes = Math.max(1, Math.round((schedule.intervalSeconds ?? 3600) / 60))
    return 'Каждые ' + (minutes >= 60 ? Math.round(minutes / 60) + ' ч' : minutes + ' мин')
  }
  return schedule.expression || 'CRON не задан'
}

function TaskForm({ initial, editing, busy, onCancel, onSave }: {
  initial: TaskFormState
  editing: boolean
  busy: boolean
  onCancel: () => void
  onSave: (input: ScheduleInput) => void
}) {
  const [form, setForm] = useState(initial)
  const [validation, setValidation] = useState<string>()

  useEffect(() => setForm(initial), [initial])

  const update = <K extends keyof TaskFormState>(key: K, value: TaskFormState[K]) => {
    setForm((current) => ({ ...current, [key]: value }))
    setValidation(undefined)
  }

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!form.title.trim() || !form.prompt.trim()) {
      setValidation('Укажите название и инструкцию для Yuri.')
      return
    }
    if (form.type === 'once' && !form.runAt) {
      setValidation('Выберите дату и время запуска.')
      return
    }
    if (form.type === 'interval' && (!Number.isFinite(Number(form.intervalMinutes)) || Number(form.intervalMinutes) < 1)) {
      setValidation('Интервал должен быть не меньше одной минуты.')
      return
    }
    if (form.type === 'cron' && form.expression.trim().split(/\s+/).length !== 5) {
      setValidation('Используйте стандартное 5-польное CRON-выражение.')
      return
    }
    onSave(formToInput(form))
  }

  return (
    <section className="task-editor" aria-labelledby="task-editor-title">
      <div className="task-editor__heading">
        <div><span className="section-heading__overline">{editing ? 'EDIT SCHEDULE' : 'NEW SCHEDULE'}</span><h2 id="task-editor-title">{editing ? 'Изменить задачу' : 'Новая фоновая задача'}</h2></div>
        <span className="stage-pill">DURABLE · POLICY · BUDGET</span>
      </div>
      <form className="task-form" onSubmit={submit}>
        <div className="task-form__main">
          <label><span>Название</span><input autoFocus onChange={(event) => update('title', event.target.value)} placeholder="Например, утренняя сводка" value={form.title} /></label>
          <label><span>Инструкция Yuri</span><textarea onChange={(event) => update('prompt', event.target.value)} placeholder="Что Yuri должна сделать при запуске?" rows={4} value={form.prompt} /></label>
        </div>
        <div className="task-form__schedule">
          <div className="task-form__types" role="tablist" aria-label="Тип расписания">
            {(Object.keys(typeLabels) as ScheduleType[]).map((type) => <button aria-selected={form.type === type} className={form.type === type ? 'task-type task-type--active' : 'task-type'} key={type} onClick={() => update('type', type)} role="tab" type="button">{typeLabels[type]}</button>)}
          </div>
          {form.type === 'once' && <label><span>Дата и время</span><input onChange={(event) => update('runAt', event.target.value)} type="datetime-local" value={form.runAt} /></label>}
          {form.type === 'interval' && <label><span>Повторять, минут</span><input min={1} onChange={(event) => update('intervalMinutes', event.target.value)} type="number" value={form.intervalMinutes} /></label>}
          {form.type === 'cron' && <label><span>5-польное CRON-выражение</span><input onChange={(event) => update('expression', event.target.value)} placeholder="0 9 * * 1-5" spellCheck={false} value={form.expression} /><small>минуты · часы · день месяца · месяц · день недели</small></label>}
          <label><span>Часовой пояс IANA</span><select onChange={(event) => update('timezone', event.target.value)} value={timezones.includes(form.timezone) ? form.timezone : ''}><option disabled value="">{form.timezone}</option>{timezones.map((zone) => <option key={zone} value={zone}>{zone}</option>)}</select></label>
          <div className="task-form__split">
            <label><span>При пропуске запуска</span><select onChange={(event) => update('misfirePolicy', event.target.value as MisfirePolicy)} value={form.misfirePolicy}><option value="run_once">Выполнить один раз</option><option value="skip">Пропустить</option></select></label>
            <label><span>Доставка результата</span><select onChange={(event) => update('deliveryChannel', event.target.value as DeliveryChannel)} value={form.deliveryChannel}><option value="in_app">В приложении</option><option value="notification">Уведомление macOS</option></select></label>
          </div>
        </div>
        <div className="task-form__budget">
          <div className="task-form__budget-heading"><span className="section-heading__overline">RUN BUDGET</span><small>Ограничения одного запуска</small></div>
          <div className="task-form__split task-form__split--three">
            <label><span>Время, сек</span><input min={1} onChange={(event) => update('maxDurationSeconds', event.target.value)} type="number" value={form.maxDurationSeconds} /></label>
            <label><span>Токены</span><input min={1} onChange={(event) => update('maxTokens', event.target.value)} type="number" value={form.maxTokens} /></label>
            <label><span>Вызовы tools</span><input min={1} onChange={(event) => update('maxToolCalls', event.target.value)} type="number" value={form.maxToolCalls} /></label>
          </div>
        </div>
        {validation && <p className="task-form__validation" role="alert"><Icon name="warning" width={14} height={14} /> {validation}</p>}
        <footer className="task-editor__actions"><button className="button button--quiet" disabled={busy} onClick={onCancel} type="button">Отмена</button><button className="button button--accent" disabled={busy} type="submit">{busy ? 'Сохраняю…' : editing ? 'Сохранить изменения' : 'Создать задачу'}</button></footer>
      </form>
    </section>
  )
}

function RunHistory({ runs, busyIds, onCancel }: {
  runs: JobRun[]
  busyIds: Set<string>
  onCancel: (run: JobRun) => void
}) {
  if (runs.length === 0) return <p className="task-history__empty">Запусков ещё не было.</p>
  return <div className="task-history" role="list" aria-label="История запусков">
    {runs.map((run) => <div className="task-history__row" key={run.id} role="listitem"><span className={'task-run-status task-run-status--' + run.status}><i />{runStatusLabels[run.status]}</span><span className="task-history__date">{formatDate(run.startedAt)}</span><span className="task-history__trigger">{run.triggeredBy === 'manual' ? 'вручную' : run.triggeredBy === 'recovery' ? 'recovery' : 'по расписанию'}</span><span className="task-history__duration">{formatDuration(run.durationMs)}</span>{(run.status === 'queued' || run.status === 'running') && <button className="task-history__cancel" disabled={busyIds.has(run.id)} onClick={() => onCancel(run)} type="button">{busyIds.has(run.id) ? 'Останавливаю…' : 'Остановить'}</button>}{run.error && <span className="task-history__error">{run.error}</span>}</div>)}
  </div>
}

function ScheduleCard({ schedule, runs, busy, busyIds, onToggle, onRun, onCancelRun, onEdit, onDelete }: {
  schedule: Schedule
  runs: JobRun[]
  busy: boolean
  busyIds: Set<string>
  onToggle: (schedule: Schedule) => void
  onRun: (schedule: Schedule) => void
  onCancelRun: (run: JobRun) => void
  onEdit: (schedule: Schedule) => void
  onDelete: (schedule: Schedule) => void
}) {
  return <article className={'task-card task-card--' + schedule.status}>
    <header className="task-card__header">
      <div className="task-card__identity"><span className="task-card__icon"><Icon name={schedule.type === 'cron' ? 'clock' : 'tasks'} width={17} height={17} /></span><div><h3>{schedule.title}</h3><span>{typeLabels[schedule.type]} · {statusLabels[schedule.status]}</span></div></div>
      <button aria-checked={schedule.enabled} aria-label={schedule.enabled ? 'Поставить задачу на паузу' : 'Возобновить задачу'} className={'toggle' + (schedule.enabled ? ' toggle--on' : '')} onClick={() => onToggle(schedule)} role="switch" type="button"><i /></button>
    </header>
    <p className="task-card__prompt">{schedule.prompt}</p>
    <div className="task-card__schedule"><span><Icon name="clock" width={13} height={13} /> {scheduleDescription(schedule)}</span><span><Icon name="relationship" width={13} height={13} /> {schedule.timezone}</span></div>
    <div className="task-card__next"><span className="section-heading__overline">NEXT RUN</span><strong>{schedule.enabled ? formatDate(schedule.nextRunAt) : 'на паузе'}</strong>{schedule.lastRunAt && <small>Последний: {formatDate(schedule.lastRunAt)}</small>}</div>
    {schedule.lastError && <div className="task-card__error"><Icon name="warning" width={13} height={13} /> {schedule.lastError}</div>}
    <details className="task-card__history"><summary><Icon name="chevron-right" width={13} height={13} /> История запусков <span>{runs.length}</span></summary><RunHistory busyIds={busyIds} onCancel={onCancelRun} runs={runs} /></details>
    <footer className="task-card__actions"><button className="task-action task-action--accent" disabled={busy} onClick={() => onRun(schedule)} type="button"><Icon name="arrow-up" width={13} height={13} /> Запустить сейчас</button><button className="task-action" disabled={busy} onClick={() => onEdit(schedule)} type="button">Изменить</button><button className="task-action task-action--danger" disabled={busy} onClick={() => onDelete(schedule)} type="button">Удалить</button></footer>
  </article>
}

export function TasksView() {
  const client = useMemo(() => createYuriClient(), [])
  const [schedules, setSchedules] = useState<Schedule[]>([])
  const [runs, setRuns] = useState<JobRun[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string>()
  const [feedback, setFeedback] = useState<Feedback>()
  const [editor, setEditor] = useState<{ initial: TaskFormState; scheduleId?: string }>()
  const [editorBusy, setEditorBusy] = useState(false)
  const [busyIds, setBusyIds] = useState<Set<string>>(new Set())

  const load = useCallback(async () => {
    setLoading(true)
    setError(undefined)
    try {
      const [nextSchedules, nextRuns] = await Promise.all([client.listSchedules(), client.listJobRuns({ limit: 100 })])
      setSchedules(nextSchedules)
      setRuns(nextRuns)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Не удалось загрузить расписания.')
      setSchedules([])
      setRuns([])
    } finally {
      setLoading(false)
    }
  }, [client])

  useEffect(() => { void load() }, [load])

  const markBusy = (id: string, value: boolean) => setBusyIds((current) => {
    const next = new Set(current)
    if (value) next.add(id)
    else next.delete(id)
    return next
  })

  const save = async (input: ScheduleInput) => {
    setEditorBusy(true)
    setFeedback(undefined)
    try {
      const result = editor?.scheduleId ? await client.updateSchedule({ ...input, id: editor.scheduleId }) : await client.createSchedule(input)
      setEditor(undefined)
      await load()
      setFeedback({ kind: 'success', text: result ? 'Задача «' + result.title + '» сохранена.' : 'Задача передана scheduler backend.' })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось сохранить задачу.' })
    } finally {
      setEditorBusy(false)
    }
  }

  const toggle = async (schedule: Schedule) => {
    markBusy(schedule.id, true)
    setFeedback(undefined)
    try {
      await client.setScheduleEnabled(schedule.id, !schedule.enabled)
      await load()
      setFeedback({ kind: 'success', text: schedule.enabled ? 'Задача поставлена на паузу.' : 'Задача возобновлена.' })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось изменить состояние задачи.' })
    } finally {
      markBusy(schedule.id, false)
    }
  }

  const runNow = async (schedule: Schedule) => {
    markBusy(schedule.id, true)
    setFeedback(undefined)
    try {
      const result = await client.runScheduleNow(schedule.id)
      await load()
      setFeedback(manualRunFeedback(result?.status))
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось запустить задачу.' })
    } finally {
      markBusy(schedule.id, false)
    }
  }

  const cancelRun = async (run: JobRun) => {
    markBusy(run.id, true)
    setFeedback(undefined)
    try {
      const result = await client.cancelJobRun(run.id)
      await load()
      setFeedback({
        kind: 'info',
        text: result?.status === 'cancelled'
          ? 'Фоновый запуск остановлен.'
          : 'Запрос на остановку передан scheduler.',
      })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось остановить запуск.' })
    } finally {
      markBusy(run.id, false)
    }
  }

  const remove = async (schedule: Schedule) => {
    if (!globalThis.confirm('Удалить задачу «' + schedule.title + '»?\n\nИстория запусков останется в Activity, но расписание больше не запустится.')) return
    markBusy(schedule.id, true)
    setFeedback(undefined)
    try {
      await client.deleteSchedule(schedule.id)
      setSchedules((current) => current.filter((item) => item.id !== schedule.id))
      setFeedback({ kind: 'success', text: 'Задача удалена.' })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось удалить задачу.' })
    } finally {
      markBusy(schedule.id, false)
    }
  }

  const runsBySchedule = useMemo(() => {
    const map = new Map<string, JobRun[]>()
    runs.forEach((run) => map.set(run.scheduleId, [...(map.get(run.scheduleId) ?? []), run]))
    return map
  }, [runs])
  const activeCount = schedules.filter((schedule) => schedule.enabled).length
  const countLabel = loading ? '…' : String(schedules.length).padStart(2, '0')

  return <div className="tasks-view">
    <div className="ambient-glow ambient-glow--one" />
    <div className="ambient-glow ambient-glow--two" />
    <header className="tasks-view__hero"><div><span className="welcome-card__eyebrow"><span className="eyebrow-dot" /> YURI SCHEDULER</span><h1>Задачи<span className="title-dot">.</span></h1><p>Durable расписания позволяют Yuri работать в фоне с ограниченным бюджетом. Каждый запуск переживает перезапуск приложения и проходит policy gate перед side effect.</p></div><div className="tasks-view__metric"><strong>{countLabel}</strong><span>{activeCount} активных</span></div></header>
    {editor ? <TaskForm busy={editorBusy} editing={Boolean(editor.scheduleId)} initial={editor.initial} onCancel={() => setEditor(undefined)} onSave={(input) => void save(input)} /> : <section className="tasks-toolbar"><div><span className="section-heading__overline">DURABLE JOBS</span><h2>Ваши расписания</h2></div><button className="button button--accent" onClick={() => setEditor({ initial: emptyForm() })} type="button"><Icon name="plus" width={14} height={14} /> Новая задача</button></section>}
    {feedback && <div className={'tasks-feedback tasks-feedback--' + feedback.kind} role={feedback.kind === 'error' ? 'alert' : 'status'}><Icon name={feedback.kind === 'success' ? 'check' : feedback.kind === 'error' ? 'warning' : 'clock'} width={14} height={14} /> {feedback.text}<button aria-label="Закрыть уведомление" className="icon-button icon-button--small" onClick={() => setFeedback(undefined)} type="button"><Icon name="x" width={13} height={13} /></button></div>}
    {error && <div className="tasks-feedback tasks-feedback--error" role="alert"><Icon name="warning" width={14} height={14} /> {error}<button aria-label="Закрыть ошибку" className="icon-button icon-button--small" onClick={() => setError(undefined)} type="button"><Icon name="x" width={13} height={13} /></button></div>}
    {loading && <div className="task-state" role="status"><span className="memory-spinner" /> Загружаю scheduler…</div>}
    {!loading && !error && schedules.length === 0 && <div className="task-state task-state--empty"><Icon name="tasks" width={23} height={23} /><strong>Фоновых задач пока нет</strong><span>Создайте одноразовую, интервальную или CRON-задачу. Она появится здесь после сохранения в durable scheduler.</span><button className="button button--quiet" onClick={() => setEditor({ initial: emptyForm() })} type="button">Создать первую задачу</button></div>}
    {!loading && schedules.length > 0 && <div className="task-grid">{schedules.map((schedule) => <ScheduleCard busy={busyIds.has(schedule.id)} busyIds={busyIds} key={schedule.id} onCancelRun={cancelRun} onDelete={remove} onEdit={(item) => setEditor({ initial: scheduleToForm(item), scheduleId: item.id })} onRun={runNow} onToggle={toggle} runs={runsBySchedule.get(schedule.id) ?? []} schedule={schedule} />)}</div>}
    <div className="tasks-note"><span className="tasks-note__icon"><Icon name="shield" width={16} height={16} /></span><div><strong>Безопасность фоновых запусков</strong><p>Пауза, дневные лимиты, quiet hours и разрешения проверяются непосредственно перед выполнением. Автоматический запуск не получает новых capabilities.</p></div></div>
  </div>
}
