import { useCallback, useEffect, useMemo, useState } from 'react'

import { createYuriClient } from '../lib/client'
import type {
  PluginEnableRequest,
  PluginPackageInspection,
  PluginPermission,
  PluginRecord,
  PluginSignatureStatus,
  PluginStatus,
  PluginTool,
} from '../lib/contracts'
import { formatDateTime } from '../lib/datetime'
import { declaredScope } from '../lib/plugin-consent'
import { Icon } from './Icon'
import { PluginConsentDialog } from './PluginConsentDialog'

type Feedback = { kind: 'success' | 'error'; text: string }

const statusLabels: Record<PluginStatus, string> = {
  installed: 'установлен',
  enabled: 'включён',
  running: 'работает',
  stopped: 'остановлен',
  crashed: 'сбой',
  error: 'ошибка',
  disabled: 'выключен',
  unknown: 'состояние неизвестно',
}

const signatureLabels: Record<PluginSignatureStatus, string> = {
  signed: 'подпись проверена',
  unsigned: 'без подписи',
  invalid: 'подпись недействительна',
  dev: 'dev mode',
  unknown: 'подпись не проверена',
}

function errorText(cause: unknown, fallback: string): string {
  return cause instanceof Error ? cause.message : fallback
}

function formatDate(value?: string): string {
  if (!value) return 'дата не указана'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return formatDateTime(date)
}

function statusClass(status: PluginStatus): string {
  return `plugin-status plugin-status--${status}`
}

function signatureClass(status: PluginSignatureStatus): string {
  return `plugin-signature plugin-signature--${status}`
}

function permissionLabel(permission: PluginPermission): string {
  const scope = declaredScope(permission)
  if (scope.kind === 'unrestricted') return `${permission.capability} · без ограничений`
  if (scope.values.length === 0) return `${permission.capability} · ${scope.kind}`
  return `${permission.capability} · ${scope.kind}: ${scope.values.join(', ')}`
}

function riskLabel(risk: PluginTool['risk']): string {
  return risk === 'critical' ? 'critical' : risk === 'high' ? 'high' : risk === 'medium' ? 'medium' : 'low'
}

function PluginPermissionList({ permissions }: { permissions: PluginPermission[] }) {
  if (permissions.length === 0) return <p className="plugin-muted">Плагин не запрашивает capabilities.</p>

  return (
    <ul className="plugin-permission-list">
      {permissions.map((permission) => (
        <li className={`plugin-permission${permission.granted ? ' plugin-permission--granted' : ''}`} key={`${permission.capability}:${permission.scope ?? ''}`}>
          <span className="plugin-permission__dot" />
          <span className="plugin-permission__copy">
            <strong>{permissionLabel(permission)}</strong>
            {permission.description && <small>{permission.description}</small>}
          </span>
          <span className="plugin-permission__state">{permission.granted ? 'выдано' : 'запрошено, не выдано'}</span>
        </li>
      ))}
    </ul>
  )
}

function PluginTools({ tools }: { tools: PluginTool[] }) {
  if (tools.length === 0) return <p className="plugin-muted">Инструменты не объявлены.</p>

  return (
    <ul className="plugin-tool-list">
      {tools.map((tool) => (
        <li key={tool.id}>
          <span className="plugin-tool__name">{tool.name}</span>
          <span className={`plugin-tool__risk plugin-tool__risk--${riskLabel(tool.risk)}`}>{riskLabel(tool.risk)}</span>
          {tool.description && <small>{tool.description}</small>}
        </li>
      ))}
    </ul>
  )
}

function InspectionPanel({ inspection, devMode, busy, onInstall }: {
  inspection: PluginPackageInspection
  devMode: boolean
  busy: boolean
  onInstall: () => void
}) {
  const manifest = inspection.manifest
  // `installable` is the backend's verdict, already resolved against the
  // persisted dev-mode switch. Recomputing the policy here could only produce
  // a different answer than the one InstallPlugin will enforce, so the view
  // reports the verdict instead of second-guessing it.
  const canInstall = inspection.installable

  return (
    <section className="plugin-inspection" aria-labelledby="plugin-inspection-title">
      <div className="plugin-card__heading">
        <div>
          <span className="section-heading__overline">PACKAGE REVIEW</span>
          <h2 id="plugin-inspection-title">Проверка пакета</h2>
        </div>
        <span className={canInstall ? 'plugin-check plugin-check--ok' : 'plugin-check plugin-check--error'}>
          <i /> {canInstall ? 'готов к установке' : 'нужны исправления'}
        </span>
      </div>

      <div className="plugin-inspection__path"><Icon name="plugins" width={14} height={14} /><span>{inspection.path}</span></div>

      {manifest ? (
        <>
          <div className="plugin-inspection__manifest">
            <div className="plugin-inspection__identity">
              <span className="plugin-avatar"><Icon name="plugins" width={17} height={17} /></span>
              <div><strong>{manifest.name}</strong><small>{manifest.id} · v{manifest.version}</small></div>
            </div>
            <span className={signatureClass(inspection.signatureStatus)}><i /> {signatureLabels[inspection.signatureStatus]}</span>
          </div>
          {manifest.description && <p className="plugin-description">{manifest.description}</p>}
          <div className="plugin-inspection__meta">
            <span>{manifest.publisher || 'Publisher не указан'}</span>
            <span>{manifest.protocolVersion ? `RPC ${manifest.protocolVersion}` : 'RPC version не указана'}</span>
            {inspection.checksum && <span>sha256 {inspection.checksum.slice(0, 16)}…</span>}
          </div>
          <div className="plugin-review-grid">
            <div><span className="plugin-review-grid__label">Разрешения</span><PluginPermissionList permissions={manifest.permissions} /></div>
            <div><span className="plugin-review-grid__label">Tools</span><PluginTools tools={manifest.tools} /></div>
          </div>
        </>
      ) : <p className="plugin-muted">Manifest не удалось прочитать.</p>}

      {inspection.warnings.length > 0 && <div className="plugin-messages plugin-messages--warning"><Icon name="warning" width={14} height={14} /><div>{inspection.warnings.map((warning) => <p key={warning}>{warning}</p>)}</div></div>}
      {inspection.errors.length > 0 && <div className="plugin-messages plugin-messages--error"><Icon name="warning" width={14} height={14} /><div>{inspection.errors.map((error) => <p key={error}>{error}</p>)}</div></div>}
      {inspection.requiresDevMode && !devMode && <p className="plugin-inspection__hint">Подпись пакета не подтверждена. Установить и запустить его можно только после включения dev mode, а это снимает проверку подписи для всех плагинов.</p>}
      {inspection.requiresDevMode && devMode && <p className="plugin-inspection__hint plugin-inspection__hint--warning">Подпись пакета не подтверждена. Установка разрешена только потому, что включён dev mode.</p>}

      <div className="plugin-card__actions">
        <button className="button button--accent" disabled={!canInstall || busy} onClick={onInstall} type="button"><Icon name="plus" width={14} height={14} /> {busy ? 'Устанавливаю…' : 'Установить плагин'}</button>
      </div>
    </section>
  )
}

function PluginCard({ plugin, busy, onAction, onUninstall }: {
  plugin: PluginRecord
  busy: boolean
  onAction: (plugin: PluginRecord, action: 'enable' | 'disable' | 'start' | 'stop') => void
  onUninstall: (plugin: PluginRecord) => void
}) {
  const canStart = plugin.enabled && !plugin.running
  const canStop = plugin.running

  return (
    <article className="plugin-card">
      <header className="plugin-card__header">
        <div className="plugin-card__identity">
          <span className="plugin-avatar"><Icon name="plugins" width={18} height={18} /></span>
          <div><h3>{plugin.name}</h3><span>{plugin.id} · v{plugin.version}</span></div>
        </div>
        <div className="plugin-card__badges">
          <span className={statusClass(plugin.status)}><i /> {statusLabels[plugin.status]}</span>
          <span className={signatureClass(plugin.signatureStatus)}>{signatureLabels[plugin.signatureStatus]}</span>
        </div>
      </header>

      {plugin.description && <p className="plugin-description">{plugin.description}</p>}
      <div className="plugin-card__meta">
        <span>{plugin.publisher || 'Publisher не указан'}</span>
        {plugin.protocolVersion && <span>RPC {plugin.protocolVersion}</span>}
        {plugin.installedAt && <span>установлен {formatDate(plugin.installedAt)}</span>}
      </div>

      <div className="plugin-card__sections">
        <div><span className="plugin-review-grid__label">Capabilities · {plugin.permissions.length}</span><PluginPermissionList permissions={plugin.permissions} /></div>
        <div><span className="plugin-review-grid__label">Tools · {plugin.tools.length}</span><PluginTools tools={plugin.tools} /></div>
      </div>

      {plugin.lastError && <div className="plugin-inline-error"><Icon name="warning" width={13} height={13} /> {plugin.lastError}</div>}
      {plugin.installPath && <div className="plugin-install-path"><Icon name="lock" width={12} height={12} /> {plugin.installPath}</div>}

      <footer className="plugin-card__actions">
        {plugin.enabled ? (
          <button className="plugin-action" disabled={busy} onClick={() => onAction(plugin, 'disable')} type="button">Выключить</button>
        ) : (
          <button className="plugin-action plugin-action--accent" disabled={busy} onClick={() => onAction(plugin, 'enable')} type="button">Включить</button>
        )}
        {canStart && <button className="plugin-action" disabled={busy} onClick={() => onAction(plugin, 'start')} type="button">Запустить</button>}
        {canStop && <button className="plugin-action" disabled={busy} onClick={() => onAction(plugin, 'stop')} type="button">Остановить</button>}
        <button className="plugin-action plugin-action--danger" disabled={busy} onClick={() => onUninstall(plugin)} type="button">Удалить</button>
      </footer>
    </article>
  )
}

export function PluginView() {
  const client = useMemo(() => createYuriClient(), [])
  const [plugins, setPlugins] = useState<PluginRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string>()
  const [feedback, setFeedback] = useState<Feedback>()
  const [installPath, setInstallPath] = useState('')
  // Dev mode is a persisted owner decision held by the backend, never a local
  // component flag: the same switch decides whether InstallPlugin and
  // StartPlugin accept an unverified package.
  const [devMode, setDevMode] = useState(false)
  const [devModeReady, setDevModeReady] = useState(false)
  const [devModeBusy, setDevModeBusy] = useState(false)
  const [inspection, setInspection] = useState<PluginPackageInspection>()
  const [busyIds, setBusyIds] = useState<Set<string>>(new Set())
  const [inspecting, setInspecting] = useState(false)
  const [installing, setInstalling] = useState(false)
  const [consentTarget, setConsentTarget] = useState<PluginRecord>()
  const [consentError, setConsentError] = useState<string>()

  const loadPlugins = useCallback(async () => {
    setLoading(true)
    setError(undefined)
    try {
      setPlugins(await client.listPlugins())
    } catch (cause) {
      setError(errorText(cause, 'Не удалось загрузить список плагинов.'))
      setPlugins([])
    } finally {
      setLoading(false)
    }
  }, [client])

  useEffect(() => { void loadPlugins() }, [loadPlugins])

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const enabled = await client.pluginDevMode()
        if (!cancelled) setDevMode(enabled)
      } catch (cause) {
        if (!cancelled) setFeedback({ kind: 'error', text: errorText(cause, 'Не удалось прочитать состояние dev mode.') })
      } finally {
        if (!cancelled) setDevModeReady(true)
      }
    })()
    return () => { cancelled = true }
  }, [client])

  const markBusy = (id: string, value: boolean) => {
    setBusyIds((current) => {
      const next = new Set(current)
      if (value) next.add(id)
      else next.delete(id)
      return next
    })
  }

  // Inspection is keyed only by the path: the backend resolves the package
  // against its own dev-mode state and returns the verdict in `installable`.
  const inspectPath = useCallback(async (path: string): Promise<boolean> => {
    setInspecting(true)
    setFeedback(undefined)
    try {
      setInspection(await client.inspectPluginPackage(path))
      return true
    } catch (cause) {
      setInspection(undefined)
      setFeedback({ kind: 'error', text: errorText(cause, 'Проверка пакета завершилась ошибкой.') })
      return false
    } finally {
      setInspecting(false)
    }
  }, [client])

  const inspect = async () => {
    const path = installPath.trim()
    if (!path) {
      setFeedback({ kind: 'error', text: 'Укажите путь к локальному пакету плагина.' })
      return
    }
    await inspectPath(path)
  }

  // Writing the switch is the whole action: it is persisted config, and the
  // reviewed package's `installable` was computed against the previous value,
  // so the package is re-checked rather than left showing a stale verdict.
  const toggleDevMode = async (enabled: boolean) => {
    setDevModeBusy(true)
    setFeedback(undefined)
    try {
      await client.setPluginDevMode(enabled)
      setDevMode(enabled)
      const reinspected = inspection ? await inspectPath(inspection.path) : true
      if (reinspected) {
        setFeedback({
          kind: 'success',
          text: enabled
            ? 'Dev mode включён: Yuri будет устанавливать и запускать плагины без подтверждённой подписи.'
            : 'Dev mode выключен: снова требуется подтверждённая подпись.',
        })
      }
    } catch (cause) {
      setFeedback({ kind: 'error', text: errorText(cause, 'Не удалось изменить состояние dev mode.') })
    } finally {
      setDevModeBusy(false)
    }
  }

  const install = async () => {
    // The backend decides installability; the button is disabled on the same
    // field, so reaching here with a refused package means the state changed
    // underneath and the install must not be attempted.
    if (!inspection?.installable) return
    setInstalling(true)
    setFeedback(undefined)
    try {
      const installed = await client.installPlugin({ path: inspection.path })
      await loadPlugins()
      setInspection(undefined)
      if (installed) setFeedback({ kind: 'success', text: `${installed.name} установлен и пока выключен.` })
      else setFeedback({ kind: 'success', text: 'Пакет передан plugin host. Плагин установлен и пока выключен.' })
      setInstallPath('')
    } catch (cause) {
      setFeedback({ kind: 'error', text: errorText(cause, 'Не удалось установить плагин.') })
    } finally {
      setInstalling(false)
    }
  }

  // Enabling is the one lifecycle action that needs the owner's consent list,
  // so it opens the dialog instead of going straight to the bridge.
  const runAction = async (plugin: PluginRecord, action: 'enable' | 'disable' | 'start' | 'stop') => {
    if (action === 'enable') {
      setConsentError(undefined)
      setConsentTarget(plugin)
      return
    }
    markBusy(plugin.id, true)
    setFeedback(undefined)
    try {
      const updated = action === 'disable'
        ? await client.disablePlugin(plugin.id)
        : action === 'start'
          ? await client.startPlugin(plugin.id)
          : await client.stopPlugin(plugin.id)
      if (updated) setPlugins((current) => current.map((item) => item.id === updated.id ? updated : item))
      else await loadPlugins()
      setFeedback({ kind: 'success', text: action === 'disable' ? 'Плагин выключен.' : action === 'start' ? 'Плагин запущен.' : 'Плагин остановлен.' })
    } catch (cause) {
      setFeedback({ kind: 'error', text: errorText(cause, 'Операция с плагином завершилась ошибкой.') })
    } finally {
      markBusy(plugin.id, false)
    }
  }

  // The backend is the authority on whether a consented scope is narrow enough.
  // Its rejection is shown inside the dialog, with the owner's input intact, so
  // a refused narrowing can be corrected instead of quietly disappearing.
  const confirmConsent = async (plugin: PluginRecord, request: PluginEnableRequest) => {
    markBusy(plugin.id, true)
    setConsentError(undefined)
    setFeedback(undefined)
    try {
      const updated = await client.enablePlugin(request)
      if (updated) setPlugins((current) => current.map((item) => item.id === updated.id ? updated : item))
      else await loadPlugins()
      setConsentTarget(undefined)
      setFeedback({
        kind: 'success',
        text: request.capabilities.length === 0
          ? 'Плагин включён без единого доступа.'
          : `Плагин включён. Выдано доступов: ${request.capabilities.length}.`,
      })
    } catch (cause) {
      setConsentError(errorText(cause, 'Backend отклонил список согласий.'))
    } finally {
      markBusy(plugin.id, false)
    }
  }

  const uninstall = async (plugin: PluginRecord) => {
    const confirmed = globalThis.confirm(`Удалить плагин «${plugin.name}»?\n\nБудут удалены его установленный пакет и локальные grants. Это действие нельзя отменить.`)
    if (!confirmed) return
    markBusy(plugin.id, true)
    setFeedback(undefined)
    try {
      await client.uninstallPlugin(plugin.id)
      setPlugins((current) => current.filter((item) => item.id !== plugin.id))
      setFeedback({ kind: 'success', text: 'Плагин удалён.' })
    } catch (cause) {
      setFeedback({ kind: 'error', text: errorText(cause, 'Не удалось удалить плагин.') })
    } finally {
      markBusy(plugin.id, false)
    }
  }

  const countLabel = loading ? '…' : String(plugins.length).padStart(2, '0')

  return (
    <div className="plugin-view">
      <div className="ambient-glow ambient-glow--one" />
      <div className="ambient-glow ambient-glow--two" />
      <header className="plugin-view__hero">
        <div>
          <span className="welcome-card__eyebrow"><span className="eyebrow-dot" /> YURI PLUGIN HOST</span>
          <h1>Плагины<span className="title-dot">.</span></h1>
          <p>Расширения в отдельных процессах подключают Yuri к внешним сервисам и инструментам. Каждый пакет проверяется до установки, а capabilities выдаются отдельно от характера и памяти агента.</p>
        </div>
        <div className="plugin-view__metric"><strong>{countLabel}</strong><span>установленных</span></div>
      </header>

      <section className="plugin-install-card" aria-labelledby="plugin-install-title">
        <div className="plugin-card__heading">
          <div><span className="section-heading__overline">LOCAL PACKAGE</span><h2 id="plugin-install-title">Добавить расширение</h2></div>
          <span className="stage-pill">RPC · SIGNATURE · POLICY</span>
        </div>
        <p className="plugin-install-card__lead">Укажите путь к распакованному каталогу с <code>plugin.json</code>. Плагин будет установлен выключенным до явного включения.</p>
        <div className="plugin-install-form">
          <label className="plugin-path-input"><Icon name="plugins" width={15} height={15} /><span className="sr-only">Путь к пакету</span><input onChange={(event) => { setInstallPath(event.target.value); setInspection(undefined) }} placeholder="/Users/you/Downloads/yuri-plugin" spellCheck={false} value={installPath} /></label>
          <button className="button button--quiet" disabled={inspecting || !installPath.trim()} onClick={() => void inspect()} type="button">{inspecting ? 'Проверяю…' : 'Проверить пакет'}</button>
        </div>
        <label className="plugin-dev-toggle"><input checked={devMode} disabled={!devModeReady || devModeBusy} onChange={(event) => void toggleDevMode(event.target.checked)} type="checkbox" /><span><strong>Разрешить dev mode</strong><small>Настройка Yuri, а не этой формы: она сохраняется и снимает требование подтверждённой подписи для всех плагинов — неподписанный пакет можно будет установить и запустить. Включайте только для локальной разработки и выключайте после.</small></span></label>
      </section>

      {inspection && <InspectionPanel busy={installing} devMode={devMode} inspection={inspection} onInstall={() => void install()} />}
      {feedback && <div className={`plugin-feedback plugin-feedback--${feedback.kind}`} role={feedback.kind === 'error' ? 'alert' : 'status'}><Icon name={feedback.kind === 'success' ? 'check' : 'warning'} width={14} height={14} /> {feedback.text}<button aria-label="Закрыть уведомление" className="icon-button icon-button--small" onClick={() => setFeedback(undefined)} type="button"><Icon name="x" width={13} height={13} /></button></div>}
      {error && <div className="plugin-feedback plugin-feedback--error" role="alert"><Icon name="warning" width={14} height={14} /> {error}<button aria-label="Закрыть ошибку" className="icon-button icon-button--small" onClick={() => setError(undefined)} type="button"><Icon name="x" width={13} height={13} /></button></div>}

      <section className="plugin-list" aria-labelledby="plugin-list-title">
        <div className="plugin-list__heading"><div><span className="section-heading__overline">INSTALLED EXTENSIONS</span><h2 id="plugin-list-title">Установленные плагины</h2></div><span className="section-heading__count">{countLabel} packages</span></div>
        {loading && <div className="plugin-state" role="status"><span className="memory-spinner" /> Загружаю plugin host…</div>}
        {!loading && !error && plugins.length === 0 && <div className="plugin-state plugin-state--empty"><Icon name="plugins" width={22} height={22} /><strong>Плагинов пока нет</strong><span>Установите reference plugin или локальный пакет, чтобы подключить новый источник данных. Автоматический GitHub browser будет добавлен позднее.</span></div>}
        {!loading && plugins.length > 0 && <div className="plugin-grid">{plugins.map((plugin) => <PluginCard busy={busyIds.has(plugin.id)} key={plugin.id} onAction={runAction} onUninstall={uninstall} plugin={plugin} />)}</div>}
      </section>

      {consentTarget && (
        <PluginConsentDialog
          busy={busyIds.has(consentTarget.id)}
          error={consentError}
          onCancel={() => { setConsentTarget(undefined); setConsentError(undefined) }}
          onConfirm={(request) => void confirmConsent(consentTarget, request)}
          plugin={consentTarget}
        />
      )}
    </div>
  )
}
