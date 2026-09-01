import type { RunUsageStats, RunUsageStatsGroup } from '../lib/contracts'
import { Icon } from './Icon'

export type UsageWindowDays = 7 | 30 | 90

type ProviderUsageStatsProps = {
  stats?: RunUsageStats
  loading: boolean
  error?: string
  windowDays: UsageWindowDays
  onDaysChange: (days: UsageWindowDays) => void
  onRefresh: () => void
}

const windowOptions: UsageWindowDays[] = [7, 30, 90]

function sum(values: number[]): number {
  return values.reduce((total, value) => total + value, 0)
}

function failureCount(group: RunUsageStatsGroup): number {
  const explicit = sum(Object.values(group.failureKinds))
  if (explicit > 0) return explicit
  return sum(Object.entries(group.statusCounts)
    .filter(([status]) => status === 'failed' || status === 'error')
    .map(([, count]) => count))
}

function formatTokens(value: number): string {
  return value.toLocaleString('ru-RU')
}

function formatWindow(stats: RunUsageStats | undefined, days: UsageWindowDays): string {
  if (!stats?.from || !stats.to) return `Последние ${days} дней`
  const from = new Date(stats.from)
  const to = new Date(stats.to)
  if (Number.isNaN(from.valueOf()) || Number.isNaN(to.valueOf())) return `Последние ${days} дней`
  const format = new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'short' })
  return `${format.format(from)} — ${format.format(to)}`
}

export function ProviderUsageStats({ stats, loading, error, windowDays, onDaysChange, onRefresh }: ProviderUsageStatsProps) {
  const groups = stats?.groups ?? []
  const totalRuns = sum(groups.map((group) => group.runCount))
  const totalTokens = sum(groups.map((group) => group.totalTokens))
  const totalFailures = sum(groups.map(failureCount))

  return (
    <section aria-labelledby="provider-usage-title" className="provider-usage settings-card">
      <div className="provider-usage__heading">
        <div>
          <span className="section-heading__overline">ROUTE USAGE</span>
          <h2 id="provider-usage-title">Использование провайдеров</h2>
          <p>Историческая статистика запусков по агентам и маршрутам.</p>
        </div>
        <span className="provider-usage__window">{formatWindow(stats, windowDays)}</span>
      </div>

      <div className="provider-usage__toolbar">
        <div aria-label="Период статистики" className="provider-usage__ranges" role="group">
          {windowOptions.map((days) => <button aria-pressed={windowDays === days} className={windowDays === days ? 'provider-usage__range provider-usage__range--active' : 'provider-usage__range'} key={days} onClick={() => onDaysChange(days)} type="button">{days} дней</button>)}
        </div>
        <button aria-label="Обновить статистику использования" className="provider-usage__refresh" disabled={loading} onClick={onRefresh} type="button"><Icon name="refresh" width={13} height={13} /> Обновить</button>
      </div>

      {error && <div className="provider-usage__error" role="alert">{error}</div>}
      {loading ? <div className="provider-usage__empty" role="status">Загружаю статистику…</div> : groups.length === 0 ? <div className="provider-usage__empty">За выбранный период запусков нет.</div> : (
        <>
          <div aria-label="Итоги использования" className="provider-usage__summary" role="group">
            <div className="provider-usage__metric"><span>Запуски</span><strong>{formatTokens(totalRuns)}</strong></div>
            <div className="provider-usage__metric"><span>Токены</span><strong>{formatTokens(totalTokens)}</strong></div>
            <div className="provider-usage__metric provider-usage__metric--failure"><span>Ошибки</span><strong>{formatTokens(totalFailures)}</strong></div>
          </div>
          <div className="provider-usage__table-wrap">
            <table className="provider-usage__table">
              <thead><tr><th scope="col">Агент</th><th scope="col">Провайдер</th><th scope="col">Модель</th><th scope="col">Запуски</th><th scope="col">Токены</th><th scope="col">Ошибки</th></tr></thead>
              <tbody>{groups.map((group) => <tr key={`${group.agentId}:${group.providerId ?? ''}:${group.model ?? ''}`}>
                <td><span className="provider-usage__agent"><strong>{group.agentName || 'Неизвестный агент'}</strong><code>{group.agentId}</code></span></td>
                <td>{group.providerId || 'Не указан'}</td>
                <td><code>{group.model || 'Не указана'}</code></td>
                <td>{formatTokens(group.runCount)}</td>
                <td>{formatTokens(group.totalTokens)}</td>
                <td className={failureCount(group) > 0 ? 'provider-usage__failure' : undefined}>{formatTokens(failureCount(group))}</td>
              </tr>)}</tbody>
            </table>
          </div>
          <p className="provider-usage__note"><Icon name="activity" width={13} height={13} /> Стоимость не показывается: исторические запуски не содержат сохранённого snapshot цен.</p>
        </>
      )}
    </section>
  )
}
