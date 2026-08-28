import { useEffect, useMemo, useState } from 'react'

import { createYuriClient } from '../lib/client'
import type { CodexAccount, ProviderSettings, UsageLimits } from '../lib/contracts'
import { EncryptedBackupCard } from './EncryptedBackupCard'
import { Icon } from './Icon'

type ProviderSettingsViewProps = {
  onBackToChat: () => void
}

const initialSettings: ProviderSettings = {
  kind: 'openai-compatible',
  baseUrl: 'https://api.openai.com/v1',
  model: 'gpt-4o-mini',
  apiKeyConfigured: false,
  timeoutSeconds: 90,
  streamResponses: true,
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
  const [apiKey, setApiKey] = useState('')
  const [allowedDirectories, setAllowedDirectories] = useState('')
  const [codex, setCodex] = useState<CodexAccount>({ connected: false })
  const [limits, setLimits] = useState<UsageLimits>()
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [loggingIn, setLoggingIn] = useState(false)
  const [feedback, setFeedback] = useState<{ kind: 'success' | 'error'; text: string }>()

  useEffect(() => {
    let mounted = true
    void Promise.all([client.getProviderSnapshot(), client.getAllowedDirectories()]).then(([snapshot, directories]) => {
      if (!mounted) return
      setSettings(snapshot.settings)
      setCodex(snapshot.codex)
      setLimits(snapshot.codex.limits)
      setAllowedDirectories(directories.join('\n'))
      setLoading(false)
    }).catch(() => {
      if (mounted) {
        setFeedback({ kind: 'error', text: 'Не удалось загрузить настройки провайдеров.' })
        setLoading(false)
      }
    })
    return () => { mounted = false }
  }, [client])

  const updateSettings = <K extends keyof ProviderSettings>(key: K, value: ProviderSettings[K]) => {
    setSettings((current) => ({ ...current, [key]: value }))
    setFeedback(undefined)
  }

  const handleSave = async () => {
    setSaving(true)
    setFeedback(undefined)
    try {
      await client.saveProviderSettings(settings, apiKey)
      await client.saveAllowedDirectories(allowedDirectories.split('\n').map((item) => item.trim()).filter(Boolean))
      setSettings((current) => ({ ...current, apiKeyConfigured: current.apiKeyConfigured || Boolean(apiKey.trim()) }))
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

  const handleLogin = async () => {
    setLoggingIn(true)
    setFeedback(undefined)
    try {
      const account = await client.loginCodex()
      setCodex(account)
      setLimits(account.limits)
      if (account.connected) {
        setSettings((current) => ({ ...current, kind: 'codex-app-server' }))
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
        <button aria-selected={settings.kind === 'openai-compatible'} className={settings.kind === 'openai-compatible' ? 'provider-tab provider-tab--active' : 'provider-tab'} onClick={() => updateSettings('kind', 'openai-compatible')} role="tab" type="button">
          <span className="provider-tab__logo">O</span><span><strong>OpenAI-compatible</strong><small>API key · streaming</small></span>
        </button>
        <button aria-selected={settings.kind === 'codex-app-server'} className={settings.kind === 'codex-app-server' ? 'provider-tab provider-tab--active' : 'provider-tab'} onClick={() => updateSettings('kind', 'codex-app-server')} role="tab" type="button">
          <span className="provider-tab__logo provider-tab__logo--codex">C</span><span><strong>Codex App Server</strong><small>ChatGPT OAuth · work limits</small></span>
        </button>
      </div>

      {loading ? <div className="settings-loading" role="status">Загружаю конфигурацию…</div> : (
        <div className="settings-grid">
          <section aria-labelledby="provider-form-title" className="settings-card">
            <div className="settings-card__heading">
              <div><span className="section-heading__overline">Endpoint</span><h2 id="provider-form-title">{settings.kind === 'openai-compatible' ? 'OpenAI-compatible API' : 'Codex App Server'}</h2></div>
              <span className={`settings-status settings-status--${settings.kind === 'codex-app-server' && !codex.connected ? 'off' : 'on'}`}><i /> {settings.kind === 'codex-app-server' ? (codex.connected ? 'account connected' : 'account required') : (settings.apiKeyConfigured ? 'key configured' : 'key not configured')}</span>
            </div>
            {settings.kind === 'openai-compatible' ? (
              <div className="settings-form">
                <label><span>Base URL</span><input onChange={(event) => updateSettings('baseUrl', event.target.value)} spellCheck={false} type="url" value={settings.baseUrl} /></label>
                <label><span>Model</span><input onChange={(event) => updateSettings('model', event.target.value)} spellCheck={false} value={settings.model} /></label>
                <label><span>API key <small>{settings.apiKeyConfigured ? '· сохранён в keyring' : '· не задан'}</small></span><input autoComplete="new-password" onChange={(event) => setApiKey(event.target.value)} placeholder={settings.apiKeyConfigured ? 'Оставьте пустым, чтобы сохранить текущий' : 'sk-…'} type="password" value={apiKey} /></label>
                <div className="settings-form__row">
                  <label><span>Timeout, sec</span><input inputMode="numeric" max={600} min={5} onChange={(event) => updateSettings('timeoutSeconds', Number(event.target.value) || 90)} type="number" value={settings.timeoutSeconds} /></label>
                  <label className="toggle-label"><span>Stream responses</span><button aria-checked={settings.streamResponses} className={`toggle${settings.streamResponses ? ' toggle--on' : ''}`} onClick={() => updateSettings('streamResponses', !settings.streamResponses)} role="switch" type="button"><i /></button></label>
                </div>
              </div>
            ) : (
              <div className="codex-account">
                <div className="codex-account__row"><div className="codex-account__avatar">{codex.connected ? '✓' : 'C'}</div><div><strong>{codex.connected ? codex.email : 'Аккаунт ChatGPT не подключён'}</strong><small>{codex.connected ? `${codex.plan ?? 'ChatGPT'} · авторизован ${codex.authenticatedAt ? new Date(codex.authenticatedAt).toLocaleDateString('ru-RU') : ''}` : 'Для Codex App Server нужен официальный OAuth-поток.'}</small></div></div>
                <button className="button button--accent button--wide" disabled={loggingIn} onClick={() => void handleLogin()} type="button"><Icon name="command" width={15} height={15} />{loggingIn ? 'Открываю OAuth…' : codex.connected ? 'Переподключить через OAuth' : 'Войти через ChatGPT'}</button>
                <p className="settings-footnote"><Icon name="lock" width={13} height={13} /> Yuri использует только официальный Codex App Server интерфейс. Токен не показывается модели и не сохраняется в SQLite.</p>
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
            <div className="settings-card__actions"><button className="button button--quiet" disabled={testing} onClick={() => void handleTest()} type="button">{testing ? 'Проверяю…' : 'Проверить соединение'}</button><button className="button button--accent" disabled={saving} onClick={() => void handleSave()} type="button">{saving ? 'Сохраняю…' : 'Сохранить'}</button></div>
          </section>

          <aside className="settings-side">
            {settings.kind === 'codex-app-server' && codex.connected && limits && <><UsageMeter limits={limits} /><button className="text-button text-button--right" onClick={() => void handleRefreshLimits()} type="button">Обновить лимиты <Icon name="refresh" width={13} height={13} /></button></>}
            <div className="settings-note"><span className="settings-note__icon"><Icon name="shield" width={17} height={17} /></span><div><strong>Разрешения отдельно</strong><p>Настройка провайдера не выдаёт Yuri доступ к файлам, сети или внешним отправкам. Каждая capability проверяется перед side effect.</p></div></div>
            <div className="settings-note settings-note--muted"><span className="settings-note__icon"><Icon name="spark" width={17} height={17} /></span><div><strong>OpenAI-compatible</strong><p>Подходит для OpenAI, локальных прокси и других endpoint с Chat Completions/Responses-style API.</p></div></div>
          </aside>
        </div>
      )}
      {!loading && <EncryptedBackupCard client={client} />}
      {feedback && <div className={`settings-feedback settings-feedback--${feedback.kind}`} role="status"><Icon name={feedback.kind === 'success' ? 'check' : 'warning'} width={15} height={15} /> {feedback.text}</div>}
    </div>
  )
}
