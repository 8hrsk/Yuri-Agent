import { useEffect, useMemo, useState } from 'react'

import { createYuriClient } from '../lib/client'
import { googleAIStudioSettings, openRouterSettings } from '../lib/client/settings'
import type { CodexAccount, CodexModel, OpenAIModel, OpenAIModelSort, ProviderSettings, RunUsageStats, UsageLimits, WebSearchSettings } from '../lib/contracts'
import { EncryptedBackupCard } from './EncryptedBackupCard'
import { Icon } from './Icon'
import { OpenAIModelPicker } from './OpenAIModelPicker'
import { ProviderUsageStats, type UsageWindowDays } from './ProviderUsageStats'

type ProviderSettingsViewProps = {
  onBackToChat: () => void
}

const initialSettings: ProviderSettings = {
  kind: 'openai-compatible',
  baseUrl: 'https://api.openai.com/v1',
  model: 'gpt-4o-mini',
  apiStyle: 'responses',
  apiKeyConfigured: false,
  favoriteModels: [],
  timeoutSeconds: 90,
  streamResponses: true,
}

const initialWebSearch: WebSearchSettings = {
  enabled: false,
  provider: 'searxng',
  endpoint: '',
  defaultResultLimit: 5,
}

function UsageMeter({ limits }: { limits: UsageLimits }) {
  return (
    <div className="usage-card">
      <div className="usage-card__heading">
        <div>
          <span className="section-heading__overline">Codex App Server</span>
          <strong>{limits.plan}</strong>
        </div>
        <span className="usage-card__reset">Сброс {limits.resetsAt}</span>
      </div>
      <div aria-label={`${limits.usedPercent}% лимита использовано`} className="usage-meter" role="meter" aria-valuemax={100} aria-valuemin={0} aria-valuenow={limits.usedPercent}>
        <span style={{ width: `${limits.usedPercent}%` }} />
      </div>
      <div className="usage-card__footer"><span>{limits.windowLabel}</span><strong>{limits.usedPercent}% использовано</strong></div>
      <p>{limits.detail}</p>
    </div>
  )
}

export function ProviderSettingsView({ onBackToChat }: ProviderSettingsViewProps) {
  const client = useMemo(() => createYuriClient(), [])
  const [settings, setSettings] = useState<ProviderSettings>(initialSettings)
  const [openAISettings, setOpenAISettings] = useState<ProviderSettings>(openRouterSettings)
  const [googleSettings, setGoogleSettings] = useState<ProviderSettings>(googleAIStudioSettings)
  const [openAIModels, setOpenAIModels] = useState<OpenAIModel[]>([])
  const [modelSort, setModelSort] = useState<OpenAIModelSort>('')
  const [loadingModels, setLoadingModels] = useState(false)
  const [apiKey, setApiKey] = useState('')
  const [allowedDirectories, setAllowedDirectories] = useState('')
  const [webSearch, setWebSearch] = useState<WebSearchSettings>(initialWebSearch)
  const [codex, setCodex] = useState<CodexAccount>({ connected: false })
  const [codexModels, setCodexModels] = useState<CodexModel[]>([])
  const [limits, setLimits] = useState<UsageLimits>()
  const [usageStats, setUsageStats] = useState<RunUsageStats>()
  const [usageWindowDays, setUsageWindowDays] = useState<UsageWindowDays>(30)
  const [usageLoading, setUsageLoading] = useState(true)
  const [usageError, setUsageError] = useState<string>()
  const [usageRefreshToken, setUsageRefreshToken] = useState(0)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [testingSearch, setTestingSearch] = useState(false)
  const [loggingIn, setLoggingIn] = useState(false)
  const [loggingOut, setLoggingOut] = useState(false)
  const [feedback, setFeedback] = useState<{ kind: 'success' | 'error'; text: string }>()

  useEffect(() => {
    let mounted = true
    void Promise.all([client.getProviderSnapshot(), client.getAllowedDirectories(), client.getWebSearchSettings()]).then(([snapshot, directories, search]) => {
      if (!mounted) return
      setSettings(snapshot.settings)
      setOpenAISettings(snapshot.openAI ?? openRouterSettings)
      setGoogleSettings(snapshot.googleAIStudio ?? googleAIStudioSettings)
      setCodex(snapshot.codex)
      setLimits(snapshot.codex.limits)
      setAllowedDirectories(directories.join('\n'))
      setWebSearch(search)
      setLoading(false)
      if (snapshot.codex.connected) void client.getCodexModels().then(setCodexModels).catch(() => setCodexModels([]))
    }).catch(() => {
      if (mounted) {
        setFeedback({ kind: 'error', text: 'Не удалось загрузить настройки провайдеров.' })
        setLoading(false)
      }
    })
    return () => { mounted = false }
  }, [client])

  useEffect(() => {
    let mounted = true
    const to = new Date()
    const from = new Date(to.getTime() - usageWindowDays * 24 * 60 * 60 * 1000)
    setUsageLoading(true)
    void client.getRunUsageStats({ from: from.toISOString(), to: to.toISOString() }).then((next) => {
      if (!mounted) return
      setUsageStats(next)
      setUsageError(undefined)
    }).catch((cause) => {
      if (!mounted) return
      setUsageError(cause instanceof Error ? cause.message : 'Не удалось загрузить статистику использования.')
    }).finally(() => {
      if (mounted) setUsageLoading(false)
    })
    return () => { mounted = false }
  }, [client, usageRefreshToken, usageWindowDays])

  const updateSettings = <K extends keyof ProviderSettings>(key: K, value: ProviderSettings[K]) => {
    setSettings((current) => {
      const next = { ...current, [key]: value }
      if (next.kind === 'openai-compatible') setOpenAISettings(next)
      if (next.kind === 'google-ai-studio') setGoogleSettings(next)
      return next
    })
    setFeedback(undefined)
  }

  const selectOpenAIProvider = () => {
    setSettings(openAISettings)
    setFeedback(undefined)
  }

  const selectOpenRouter = () => {
    const configured = openAISettings.providerId === 'openrouter' ? openAISettings : openRouterSettings
    setSettings(configured)
    setOpenAISettings(configured)
    setOpenAIModels([])
    setApiKey('')
    setFeedback(undefined)
  }

  const selectGoogleAIStudio = () => {
    setSettings(googleSettings)
    setOpenAIModels([])
    setApiKey('')
    setFeedback(undefined)
  }

  const handleLoadOpenAIModels = async (sort: OpenAIModelSort = modelSort) => {
    setModelSort(sort)
    setLoadingModels(true)
    setFeedback(undefined)
    try {
      const models = await client.getOpenAIModels(settings.providerId ?? 'openai', sort)
      setOpenAIModels(models)
      setFeedback({ kind: 'success', text: `Каталог загружен: ${models.length} моделей.` })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось загрузить каталог моделей.' })
    } finally {
      setLoadingModels(false)
    }
  }

  const handleConnectOpenAI = async () => {
    setLoadingModels(true)
    setFeedback(undefined)
    try {
      const models = await client.connectOpenAIProvider(settings, apiKey.trim() || undefined)
      const connected = { ...settings, apiKeyConfigured: true }
      setSettings(connected)
      if (connected.kind === 'google-ai-studio') setGoogleSettings(connected)
      else setOpenAISettings(connected)
      setOpenAIModels(models)
      setApiKey('')
      setFeedback({ kind: 'success', text: `Ключ сохранён в системном keyring. Доступно моделей: ${models.length}.` })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось сохранить ключ или получить модели.' })
    } finally {
      setLoadingModels(false)
    }
  }

  const handleToggleModelFavorite = async (model: OpenAIModel) => {
    const favorite = !model.favorite
    try {
      await client.setOpenAIModelFavorite(settings.providerId ?? 'openai', model.id, favorite)
      setOpenAIModels((current) => current.map((item) => item.id === model.id ? { ...item, favorite } : item))
      setSettings((current) => {
        const favorites = new Set(current.favoriteModels)
        if (favorite) favorites.add(model.id)
        else favorites.delete(model.id)
        const next = { ...current, favoriteModels: [...favorites] }
        if (next.kind === 'google-ai-studio') setGoogleSettings(next)
        else setOpenAISettings(next)
        return next
      })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось обновить избранное.' })
    }
  }

  const handleSave = async () => {
    setSaving(true)
    setFeedback(undefined)
    try {
      await client.saveProviderSettings(settings, apiKey)
      await client.saveAllowedDirectories(allowedDirectories.split('\n').map((item) => item.trim()).filter(Boolean))
      await client.saveWebSearchSettings(webSearch)
      setSettings((current) => {
        const next = { ...current, apiKeyConfigured: current.apiKeyConfigured || Boolean(apiKey.trim()) }
        if (next.kind === 'openai-compatible') setOpenAISettings(next)
        if (next.kind === 'google-ai-studio') setGoogleSettings(next)
        return next
      })
      setApiKey('')
      setFeedback({ kind: 'success', text: 'Настройки сохранены. Секрет передан только в защищённый backend-вызов.' })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось сохранить настройки.' })
    } finally {
      setSaving(false)
    }
  }

  const handleTest = async () => {
    setTesting(true)
    setFeedback(undefined)
    try {
      const result = await client.testProvider(settings)
      setFeedback({ kind: result.ok ? 'success' : 'error', text: result.message })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Проверка завершилась ошибкой.' })
    } finally {
      setTesting(false)
    }
  }

  const handleTestSearch = async () => {
    setTestingSearch(true)
    setFeedback(undefined)
    try {
      const result = await client.testWebSearchSettings(webSearch)
      setFeedback({ kind: result.ok ? 'success' : 'error', text: result.message })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Проверка SearXNG завершилась ошибкой.' })
    } finally {
      setTestingSearch(false)
    }
  }

  const handleLogin = async () => {
    setLoggingIn(true)
    setFeedback(undefined)
    try {
      const account = await client.loginCodex()
      setCodex(account)
      setLimits(account.limits)
      if (account.connected) {
        setSettings((current) => ({ ...current, kind: 'codex-app-server', model: current.kind === 'codex-app-server' ? current.model : '' }))
        setCodexModels(await client.getCodexModels().catch(() => []))
        setFeedback({ kind: 'success', text: 'Codex App Server подключён через OAuth.' })
      } else if (account.loginUrl) {
        setFeedback({ kind: 'success', text: account.userCode ? `OAuth открыт. Введите код ${account.userCode} в окне браузера, затем обновите аккаунт.` : 'OAuth открыт в браузере. Завершите вход, затем обновите аккаунт.' })
      } else {
        setFeedback({ kind: 'error', text: 'OAuth-вход не завершён.' })
      }
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось начать OAuth-вход.' })
    } finally {
      setLoggingIn(false)
    }
  }

  const handleRefreshLimits = async () => {
    const nextLimits = await client.refreshCodexLimits()
    if (nextLimits) {
      setLimits(nextLimits)
      setFeedback({ kind: 'success', text: 'Лимиты обновлены.' })
    } else {
      setFeedback({ kind: 'error', text: 'Сначала подключите Codex App Server аккаунт.' })
    }
  }

  const handleLogout = async () => {
    setLoggingOut(true)
    setFeedback(undefined)
    try {
      const result = await client.logoutCodex()
      if (!result.disconnected) throw new Error('Codex App Server не подтвердил выход.')
      setCodex({ connected: false })
      setLimits(undefined)
      setFeedback({ kind: 'success', text: 'Вы вышли из ChatGPT. Для Codex потребуется новый OAuth-вход.' })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось выйти из Codex App Server.' })
    } finally {
      setLoggingOut(false)
    }
  }

  return (
    <div className="settings-view">
      <div className="settings-view__topline">
        <button className="back-button" onClick={onBackToChat} type="button"><Icon name="chevron-right" width={14} height={14} /> Вернуться в Chat</button>
        <span className="stage-pill">SECURE LOCAL DATA · STAGE 7</span>
      </div>
      <div className="settings-view__hero">
        <span className="welcome-card__eyebrow"><span className="eyebrow-dot" /> CONFIGURATION</span>
        <h1>Провайдеры<span className="title-dot">.</span></h1>
        <p>Выберите канал, через который Yuri получает модель. Ключи не записываются в UI-хранилище и не попадают в контекст модели.</p>
      </div>

      <div className="provider-tabs" role="tablist" aria-label="Тип провайдера">
        <button aria-selected={settings.kind === 'openai-compatible'} className={settings.kind === 'openai-compatible' ? 'provider-tab provider-tab--active' : 'provider-tab'} onClick={selectOpenAIProvider} role="tab" type="button">
          <span className="provider-tab__logo">O</span><span><strong>OpenAI-compatible</strong><small>API key · streaming</small></span>
        </button>
        <button aria-selected={settings.kind === 'codex-app-server'} className={settings.kind === 'codex-app-server' ? 'provider-tab provider-tab--active' : 'provider-tab'} onClick={() => setSettings((current) => ({ ...current, kind: 'codex-app-server', model: current.kind === 'codex-app-server' ? current.model : '' }))} role="tab" type="button">
          <span className="provider-tab__logo provider-tab__logo--codex">C</span><span><strong>Codex App Server</strong><small>ChatGPT OAuth · work limits</small></span>
        </button>
        <button aria-selected={settings.kind === 'google-ai-studio'} className={settings.kind === 'google-ai-studio' ? 'provider-tab provider-tab--active' : 'provider-tab'} onClick={selectGoogleAIStudio} role="tab" type="button">
          <span className="provider-tab__logo">G</span><span><strong>Google AI Studio</strong><small>Gemini API key · Free Tier slow mode</small></span>
        </button>
        <button aria-selected={settings.kind === 'antigravity'} className={settings.kind === 'antigravity' ? 'provider-tab provider-tab--active' : 'provider-tab'} onClick={() => updateSettings('kind', 'antigravity')} role="tab" type="button">
          <span className="provider-tab__logo">A</span><span><strong>Antigravity</strong><small>OAuth unavailable</small></span>
        </button>
      </div>

      {loading ? <div className="settings-loading" role="status">Загружаю конфигурацию…</div> : (
        <>
        <div className="settings-grid">
          <section aria-labelledby="provider-form-title" className="settings-card">
            <div className="settings-card__heading">
              <div><span className="section-heading__overline">Endpoint</span><h2 id="provider-form-title">{settings.kind === 'openai-compatible' ? 'OpenAI-compatible API' : settings.kind === 'google-ai-studio' ? 'Google AI Studio API' : settings.kind === 'codex-app-server' ? 'Codex App Server' : 'Antigravity'}</h2></div>
              <span className={`settings-status settings-status--${settings.kind === 'antigravity' || (settings.kind === 'codex-app-server' && !codex.connected) ? 'off' : 'on'}`}><i /> {settings.kind === 'antigravity' ? 'unsupported auth mode' : settings.kind === 'codex-app-server' ? (codex.connected ? 'account connected' : 'account required') : (settings.apiKeyConfigured ? 'key configured' : 'key not configured')}</span>
            </div>
            {settings.kind === 'openai-compatible' || settings.kind === 'google-ai-studio' ? (
              <div className="settings-form">
                {settings.kind === 'openai-compatible' && <div className="openrouter-setup">
                  <div className="openrouter-setup__heading"><span><strong>OpenRouter</strong><br /><small>OpenAI-compatible Chat Completions · каталог моделей</small></span><span className={`settings-status settings-status--${settings.providerId === 'openrouter' ? 'on' : 'off'}`}><i /> {settings.providerId === 'openrouter' ? 'selected' : 'preset'}</span></div>
                  <div className="openrouter-setup__actions"><button className="button button--quiet" onClick={selectOpenRouter} type="button">Использовать OpenRouter</button></div>
                </div>}
                <label><span>Base URL</span><input disabled={settings.kind === 'google-ai-studio'} onChange={(event) => updateSettings('baseUrl', event.target.value)} spellCheck={false} type="url" value={settings.baseUrl} /></label>
                <label><span>API style</span><select disabled={settings.kind === 'google-ai-studio'} onChange={(event) => updateSettings('apiStyle', event.target.value as ProviderSettings['apiStyle'])} value={settings.apiStyle}><option value="chat_completions">Chat Completions</option><option value="responses">Responses</option></select></label>
                <label><span>API key <small>{settings.apiKeyConfigured ? '· сохранён в keyring' : '· не задан'}</small></span><input autoComplete="new-password" onChange={(event) => setApiKey(event.target.value)} placeholder={settings.apiKeyConfigured ? 'Оставьте пустым, чтобы сохранить текущий' : settings.kind === 'google-ai-studio' ? 'AIza…' : 'sk-or-v1-…'} type="password" value={apiKey} /></label>
                <button className="button button--quiet button--wide" disabled={loadingModels || (!apiKey.trim() && !settings.apiKeyConfigured)} onClick={() => void handleConnectOpenAI()} type="button"><Icon name="lock" width={14} height={14} /> {loadingModels ? 'Загружаю модели…' : settings.apiKeyConfigured ? 'Обновить ключ и каталог моделей' : 'Сохранить ключ и загрузить модели'}</button>
                {(settings.apiKeyConfigured || openAIModels.length > 0) && <OpenAIModelPicker loading={loadingModels} models={openAIModels} onReload={(sort) => void handleLoadOpenAIModels(sort)} onSelect={(model) => updateSettings('model', model)} onToggleFavorite={(model) => void handleToggleModelFavorite(model)} sort={modelSort} value={settings.model} />}
                <label><span>Model <small>· выбран из каталога или введён вручную</small></span><input onChange={(event) => updateSettings('model', event.target.value)} placeholder={settings.kind === 'google-ai-studio' ? 'gemini-2.5-flash' : 'openai/gpt-4.1-mini'} spellCheck={false} value={settings.model} /></label>
                {settings.kind === 'google-ai-studio' && <section aria-label="Локальные лимиты Google AI Studio" className="settings-form__quota"><label><span>Quota mode</span><select onChange={(event) => updateSettings('quotaMode', event.target.value as ProviderSettings['quotaMode'])} value={settings.quotaMode ?? 'free-tier'}><option value="free-tier">Free Tier · slow mode</option><option value="custom">Custom limits</option><option value="off">Off</option></select></label><div className="settings-form__row"><label><span>RPM</span><input min={0} onChange={(event) => updateSettings('quotaProfile', { ...settings.quotaProfile, rpm: Number(event.target.value) || 0 })} type="number" value={settings.quotaProfile?.rpm ?? 0} /></label><label><span>TPM</span><input min={0} onChange={(event) => updateSettings('quotaProfile', { ...settings.quotaProfile, tpm: Number(event.target.value) || 0 })} type="number" value={settings.quotaProfile?.tpm ?? 0} /></label><label><span>RPD</span><input min={0} onChange={(event) => updateSettings('quotaProfile', { ...settings.quotaProfile, rpd: Number(event.target.value) || 0 })} type="number" value={settings.quotaProfile?.rpd ?? 0} /></label></div><small>Нули означают «неизвестно»; Free Tier использует single-flight и не притворяется точным остатком квоты.</small></section>}
                <div className="settings-form__row">
                  <label><span>Timeout, sec</span><input inputMode="numeric" max={600} min={5} onChange={(event) => updateSettings('timeoutSeconds', Number(event.target.value) || 90)} type="number" value={settings.timeoutSeconds} /></label>
                  <label className="toggle-label"><span>Stream responses</span><button aria-checked={settings.streamResponses} className={`toggle${settings.streamResponses ? ' toggle--on' : ''}`} onClick={() => updateSettings('streamResponses', !settings.streamResponses)} role="switch" type="button"><i /></button></label>
                </div>
              </div>
            ) : settings.kind === 'codex-app-server' ? (
              <div className="codex-account">
                <div className="codex-account__row"><div className="codex-account__avatar">{codex.connected ? '✓' : 'C'}</div><div><strong>{codex.connected ? codex.email : 'Аккаунт ChatGPT не подключён'}</strong><small>{codex.connected ? `${codex.plan ?? 'ChatGPT'} · авторизован ${codex.authenticatedAt ? new Date(codex.authenticatedAt).toLocaleDateString('ru-RU') : ''}` : 'Для Codex App Server нужен официальный OAuth-поток.'}</small></div></div>
                {codex.connected && <label className="codex-model-picker"><span>Модель</span><select aria-label="Модель Codex" onChange={(event) => updateSettings('model', event.target.value)} value={settings.model}><option value="">Автоматически · {codexModels.find((model) => model.isDefault)?.displayName ?? 'модель аккаунта'}</option>{codexModels.map((model) => <option key={model.id} value={model.model}>{model.displayName}{model.isDefault ? ' · default' : ''}</option>)}</select><small>{settings.model ? codexModels.find((model) => model.model === settings.model)?.description : 'Codex выберет модель по умолчанию для вашего аккаунта.'}</small></label>}
                <button className="button button--accent button--wide" disabled={loggingIn || loggingOut} onClick={() => void handleLogin()} type="button"><Icon name="command" width={15} height={15} />{loggingIn ? 'Открываю OAuth…' : codex.connected ? 'Переподключить через OAuth' : 'Войти через ChatGPT'}</button>
                {codex.connected && <button className="button button--quiet button--wide" disabled={loggingIn || loggingOut} onClick={() => void handleLogout()} type="button"><Icon name="lock" width={14} height={14} />{loggingOut ? 'Завершаю сессию…' : 'Выйти из ChatGPT'}</button>}
                <p className="settings-footnote"><Icon name="lock" width={13} height={13} /> Yuri использует только официальный Codex App Server интерфейс. Токен не показывается модели и не сохраняется в SQLite.</p>
              </div>
            ) : (
              <div className="codex-account">
                <div className="codex-account__row"><div className="codex-account__avatar">A</div><div><strong>Antigravity OAuth недоступен</strong><small>unsupported_auth_mode · данные авторизации не запрашиваются</small></div></div>
                <p className="settings-footnote"><Icon name="lock" width={13} height={13} /> Yuri не импортирует токены Gemini CLI, browser cookies или token cache. Подключение останется выключенным до официального разрешённого integration contract.</p>
                <button className="button button--quiet button--wide" onClick={selectOpenAIProvider} type="button">Перейти к API key endpoint</button>
              </div>
            )}
            <div className="settings-form settings-form--permissions">
              <label>
                <span>Разрешённые директории <small>· один абсолютный путь на строку</small></span>
                <textarea
                  onChange={(event) => setAllowedDirectories(event.target.value)}
                  placeholder={'/Users/you/Documents\n/Users/you/Projects'}
                  rows={3}
                  spellCheck={false}
                  value={allowedDirectories}
                />
              </label>
              <p className="settings-footnote"><Icon name="lock" width={13} height={13} /> В этих корнях Yuri может читать файлы. Каждая операция <code>filesystem.write</code> показывает точный путь и требует отдельного подтверждения; удаление и symlink escape запрещены.</p>
            </div>
            <div className="settings-form settings-form--permissions">
              <label className="toggle-label">
                <span>Поиск в интернете <small>· SearXNG JSON API</small></span>
                <button aria-checked={webSearch.enabled} className={`toggle${webSearch.enabled ? ' toggle--on' : ''}`} onClick={() => setWebSearch((current) => ({ ...current, enabled: !current.enabled }))} role="switch" type="button"><i /></button>
              </label>
              <label>
                <span>SearXNG endpoint</span>
                <input disabled={!webSearch.enabled} onChange={(event) => setWebSearch((current) => ({ ...current, endpoint: event.target.value }))} placeholder="https://search.example.com" spellCheck={false} type="url" value={webSearch.endpoint} />
              </label>
              <label>
                <span>Результатов по умолчанию <small>· от 3 до 10</small></span>
                <input disabled={!webSearch.enabled} max={10} min={3} onChange={(event) => setWebSearch((current) => ({ ...current, defaultResultLimit: Math.max(3, Math.min(10, Number(event.target.value) || 5)) }))} type="number" value={webSearch.defaultResultLimit} />
              </label>
              <button className="button button--quiet" disabled={!webSearch.enabled || testingSearch || !webSearch.endpoint.trim()} onClick={() => void handleTestSearch()} type="button">{testingSearch ? 'Проверяю SearXNG…' : 'Проверить поиск'}</button>
              <p className="settings-footnote"><Icon name="search" width={13} height={13} /> <code>web.search</code> возвращает только заголовки, ссылки и snippets. Чтение выбранной страницы выполняется отдельным вызовом <code>web.fetch</code>.</p>
            </div>
            <div className="settings-card__actions"><button className="button button--quiet" disabled={testing || settings.kind === 'antigravity'} onClick={() => void handleTest()} type="button">{testing ? 'Проверяю…' : 'Проверить соединение'}</button><button className="button button--accent" disabled={saving || settings.kind === 'antigravity'} onClick={() => void handleSave()} type="button">{saving ? 'Сохраняю…' : 'Сохранить'}</button></div>
          </section>

          <aside className="settings-side">
            {settings.kind === 'codex-app-server' && codex.connected && limits && <><UsageMeter limits={limits} /><button className="text-button text-button--right" onClick={() => void handleRefreshLimits()} type="button">Обновить лимиты <Icon name="refresh" width={13} height={13} /></button></>}
            <div className="settings-note"><span className="settings-note__icon"><Icon name="shield" width={17} height={17} /></span><div><strong>Разрешения отдельно</strong><p>Настройка провайдера не выдаёт Yuri доступ к файлам, сети или внешним отправкам. Каждая capability проверяется перед side effect.</p></div></div>
            <div className="settings-note settings-note--muted"><span className="settings-note__icon"><Icon name="spark" width={17} height={17} /></span><div><strong>OpenAI-compatible</strong><p>Подходит для OpenAI, локальных прокси и других endpoint с Chat Completions/Responses-style API.</p></div></div>
          </aside>
        </div>
        <ProviderUsageStats error={usageError} loading={usageLoading} onDaysChange={setUsageWindowDays} onRefresh={() => setUsageRefreshToken((current) => current + 1)} stats={usageStats} windowDays={usageWindowDays} />
        </>
      )}
      {!loading && <EncryptedBackupCard client={client} />}
      {feedback && <div className={`settings-feedback settings-feedback--${feedback.kind}`} role="status"><Icon name={feedback.kind === 'success' ? 'check' : 'warning'} width={15} height={15} /> {feedback.text}</div>}
    </div>
  )
}
