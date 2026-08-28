import { useCallback, useEffect, useMemo, useState } from 'react'

import { createYuriClient } from '../lib/client'
import { isOnboardingComplete, onboardingStepIndex, onboardingSteps, validateOnboardingProvider, type OnboardingStep } from '../lib/onboarding'
import type { CodexAccount, ProviderSettings, YuriClient } from '../lib/contracts'
import { Icon } from './Icon'

type OnboardingViewProps = {
  client?: YuriClient
  onComplete: () => void
}

type Feedback = {
  kind: 'success' | 'error'
  text: string
}

type BusyState = 'loading' | 'oauth' | 'testing' | undefined

const defaultSettings: ProviderSettings = {
  kind: 'openai-compatible',
  baseUrl: 'https://api.openai.com/v1',
  model: 'gpt-4o-mini',
  apiKeyConfigured: false,
  timeoutSeconds: 90,
  streamResponses: true,
}

function errorMessage(cause: unknown, fallback: string): string {
  return cause instanceof Error && cause.message.trim() ? cause.message : fallback
}

function OnboardingStepper({ activeStep }: { activeStep: OnboardingStep }) {
  const activeIndex = onboardingStepIndex(activeStep)
  return (
    <ol aria-label="Шаги первого запуска" className="onboarding-stepper">
      {onboardingSteps.map((item, index) => (
        <li className={index <= activeIndex ? 'onboarding-step onboarding-step--active' : 'onboarding-step'} key={item.id}>
          <span aria-current={item.id === activeStep ? 'step' : undefined} className="onboarding-step__index">{index < activeIndex ? <Icon name="check" width={13} height={13} /> : index + 1}</span>
          <span className="onboarding-step__label">{item.label}</span>
        </li>
      ))}
    </ol>
  )
}

export function OnboardingView({ client: providedClient, onComplete }: OnboardingViewProps) {
  const client = useMemo(() => providedClient ?? createYuriClient(), [providedClient])
  const [step, setStep] = useState<OnboardingStep>('welcome')
  const [settings, setSettings] = useState<ProviderSettings>(defaultSettings)
  const [apiKey, setApiKey] = useState('')
  const [codex, setCodex] = useState<CodexAccount>({ connected: false })
  const [busy, setBusy] = useState<BusyState>('loading')
  const [loadError, setLoadError] = useState<string>()
  const [feedback, setFeedback] = useState<Feedback>()

  const load = useCallback(async () => {
    setBusy('loading')
    setLoadError(undefined)
    try {
      const [snapshot, onboarding] = await Promise.all([
        client.getProviderSnapshot(),
        client.getOnboardingState(),
      ])
      setSettings(snapshot.settings)
      setCodex(snapshot.codex)
      if (isOnboardingComplete(onboarding)) {
        onComplete()
        return
      }
      setStep('welcome')
    } catch (cause) {
      setLoadError(errorMessage(cause, 'Не удалось загрузить состояние первого запуска.'))
    } finally {
      setBusy(undefined)
    }
  }, [client, onComplete])

  useEffect(() => {
    void load()
  }, [load])

  const updateSettings = <K extends keyof ProviderSettings>(key: K, value: ProviderSettings[K]) => {
    setSettings((current) => ({ ...current, [key]: value }))
    setFeedback(undefined)
  }

  const handleProbe = async () => {
    const validationError = validateOnboardingProvider(settings, codex)
    if (validationError) {
      setFeedback({ kind: 'error', text: validationError })
      return
    }

    setBusy('testing')
    setFeedback(undefined)
    try {
      const result = await client.completeOnboarding(settings, apiKey.trim() || undefined)
      if (!result.ok || !isOnboardingComplete(result.state)) {
        setFeedback({ kind: 'error', text: result.message || 'Проверка не завершена. Исправьте настройки и повторите попытку.' })
        return
      }
      setApiKey('')
      setFeedback({ kind: 'success', text: result.message || 'Провайдер отвечает. Настройка сохранена.' })
      setStep('success')
    } catch (cause) {
      setFeedback({ kind: 'error', text: errorMessage(cause, 'Не удалось сохранить настройки и проверить провайдер.') })
    } finally {
      setBusy(undefined)
    }
  }

  const handleLogin = async () => {
    setBusy('oauth')
    setFeedback(undefined)
    try {
      const account = await client.loginCodex()
      setCodex(account)
      if (account.connected) {
        setSettings((current) => ({ ...current, kind: 'codex-app-server' }))
        setFeedback({ kind: 'success', text: 'Codex App Server подключён. Теперь выполните проверку.' })
      } else if (account.loginUrl) {
        setFeedback({ kind: 'success', text: account.userCode ? `OAuth открыт. Введите код ${account.userCode}, затем повторите проверку.` : 'OAuth открыт в браузере. Завершите вход, затем повторите проверку.' })
      } else {
        setFeedback({ kind: 'error', text: 'OAuth-вход не завершён.' })
      }
    } catch (cause) {
      setFeedback({ kind: 'error', text: errorMessage(cause, 'Не удалось начать OAuth-вход.') })
    } finally {
      setBusy(undefined)
    }
  }

  const renderFeedback = feedback && (
    <div aria-live="polite" className={`onboarding-feedback onboarding-feedback--${feedback.kind}`} role={feedback.kind === 'error' ? 'alert' : 'status'}>
      <Icon name={feedback.kind === 'success' ? 'check' : 'warning'} width={15} height={15} />
      <span>{feedback.text}</span>
    </div>
  )

  return (
    <div className="onboarding-shell">
      <main className="onboarding-view">
        <header className="onboarding-header">
          <div className="onboarding-brand"><span className="brand-mark">Y<i /></span><span><strong>Yuri</strong><small>LOCAL-FIRST AGENT</small></span></div>
          <span className="stage-pill">FIRST RUN · SECURE SETUP</span>
        </header>

        <div className="onboarding-layout">
          <section className="onboarding-main" aria-labelledby="onboarding-title">
            <div className="onboarding-eyebrow"><span className="eyebrow-dot" /> FIRST-RUN SETUP</div>
            <h1 id="onboarding-title">Настроим Yuri<span className="title-dot">.</span></h1>
            <p className="onboarding-lead">Подключите модель, чтобы Yuri могла отвечать. Ключ передаётся только в backend-вызов и не сохраняется в renderer.</p>
            <OnboardingStepper activeStep={step} />

            {busy === 'loading' ? (
              <div className="onboarding-loading" role="status"><span className="onboarding-loading__pulse" /> Проверяю состояние первого запуска…</div>
            ) : (
              <>
                {loadError && (
                  <div className="onboarding-feedback onboarding-feedback--error" role="alert">
                    <Icon name="warning" width={15} height={15} />
                    <span>{loadError}</span>
                    <button className="text-button" onClick={() => void load()} type="button">Повторить <Icon name="refresh" width={13} height={13} /></button>
                  </div>
                )}

                {step === 'welcome' && (
                  <section className="onboarding-panel onboarding-panel--welcome">
                    <div className="onboarding-panel__mark"><Icon name="spark" width={23} height={23} /></div>
                    <span className="section-heading__overline">Шаг 1 · локальный профиль</span>
                    <h2>Один короткий тест — и можно начинать.</h2>
                    <p>Сначала сохраним только настройки endpoint. Затем backend выполнит безопасный минимальный probe. Экран не исчезнет, пока успешный результат не будет подтверждён durable onboarding state.</p>
                    <div className="onboarding-facts">
                      <span><Icon name="shield" width={14} height={14} /> deny-by-default permissions</span>
                      <span><Icon name="lock" width={14} height={14} /> secrets stay in keyring</span>
                    </div>
                    <button className="button button--accent" onClick={() => { setFeedback(undefined); setStep('provider') }} type="button">Настроить провайдера <Icon name="chevron-right" width={14} height={14} /></button>
                  </section>
                )}

                {step === 'provider' && (
                  <section className="onboarding-panel onboarding-panel--provider" aria-labelledby="provider-onboarding-title">
                    <div className="onboarding-panel__heading">
                      <div><span className="section-heading__overline">Шаг 2 · connection check</span><h2 id="provider-onboarding-title">Подключите провайдера</h2></div>
                      <span className="onboarding-provider-state"><i /> SETUP</span>
                    </div>
                    <div aria-label="Выбор провайдера" className="onboarding-provider-tabs" role="tablist">
                      <button aria-selected={settings.kind === 'openai-compatible'} className={settings.kind === 'openai-compatible' ? 'onboarding-provider-tab onboarding-provider-tab--active' : 'onboarding-provider-tab'} onClick={() => updateSettings('kind', 'openai-compatible')} role="tab" type="button"><span className="provider-tab__logo">O</span><span><strong>OpenAI-compatible</strong><small>API key · streaming</small></span></button>
                      <button aria-selected={settings.kind === 'codex-app-server'} className={settings.kind === 'codex-app-server' ? 'onboarding-provider-tab onboarding-provider-tab--active' : 'onboarding-provider-tab'} onClick={() => updateSettings('kind', 'codex-app-server')} role="tab" type="button"><span className="provider-tab__logo provider-tab__logo--codex">C</span><span><strong>Codex App Server</strong><small>ChatGPT OAuth</small></span></button>
                    </div>
                    {settings.kind === 'openai-compatible' ? (
                      <>
                        <p className="onboarding-panel__hint">Подходит для OpenAI, локального прокси или другого endpoint с Responses/Chat Completions API. Ключ передаётся только в защищённый provider bridge.</p>
                        <form className="onboarding-form" onSubmit={(event) => { event.preventDefault(); void handleProbe() }}>
                          <label htmlFor="onboarding-base-url"><span>Base URL</span><input autoComplete="url" id="onboarding-base-url" onChange={(event) => updateSettings('baseUrl', event.target.value)} spellCheck={false} type="url" value={settings.baseUrl} /></label>
                          <label htmlFor="onboarding-model"><span>Model</span><input autoComplete="off" id="onboarding-model" onChange={(event) => updateSettings('model', event.target.value)} spellCheck={false} value={settings.model} /></label>
                          <label htmlFor="onboarding-api-key"><span>API key <small>{settings.apiKeyConfigured ? '· сохранён в keyring' : '· optional in preview'}</small></span><input autoComplete="new-password" id="onboarding-api-key" onChange={(event) => setApiKey(event.target.value)} placeholder={settings.apiKeyConfigured ? 'Оставьте пустым, чтобы сохранить текущий' : 'sk-…'} type="password" value={apiKey} /></label>
                          <div className="onboarding-form__actions"><button className="button button--quiet" onClick={() => { setFeedback(undefined); setStep('welcome') }} type="button">Назад</button><button className="button button--accent" disabled={busy === 'testing' || busy === 'oauth'} type="submit">{busy === 'testing' ? 'Сохраняю и проверяю…' : 'Сохранить и проверить'} <Icon name="arrow-up" width={14} height={14} /></button></div>
                        </form>
                      </>
                    ) : (
                      <div className="onboarding-codex">
                        <div className="codex-account__row"><div className="codex-account__avatar">{codex.connected ? '✓' : 'C'}</div><div><strong>{codex.connected ? codex.email : 'Аккаунт ChatGPT не подключён'}</strong><small>{codex.connected ? `${codex.plan ?? 'ChatGPT'} · готов к проверке` : 'Для Codex App Server нужен официальный OAuth-поток.'}</small></div></div>
                        <p className="onboarding-panel__hint">OAuth остаётся явным действием пользователя. Yuri не получает и не показывает токен, а backend сам выполняет probe после подключения.</p>
                        {!codex.connected && <button className="button button--quiet button--wide" disabled={busy === 'oauth' || busy === 'testing'} onClick={() => void handleLogin()} type="button"><Icon name="command" width={15} height={15} />{busy === 'oauth' ? 'Открываю OAuth…' : 'Войти через ChatGPT'}</button>}
                        <div className="onboarding-form__actions"><button className="button button--quiet" onClick={() => { setFeedback(undefined); setStep('welcome') }} type="button">Назад</button><button className="button button--accent" disabled={!codex.connected || busy === 'testing' || busy === 'oauth'} onClick={() => void handleProbe()} type="button">{busy === 'testing' ? 'Проверяю…' : 'Проверить Codex'} <Icon name="arrow-up" width={14} height={14} /></button></div>
                      </div>
                    )}
                    {renderFeedback}
                  </section>
                )}

                {step === 'success' && (
                  <section className="onboarding-panel onboarding-panel--success" aria-labelledby="onboarding-success-title">
                    <div className="onboarding-success-mark"><Icon name="check" width={27} height={27} /></div>
                    <span className="section-heading__overline">Шаг 3 · ready</span>
                    <h2 id="onboarding-success-title">Yuri готова к диалогу.</h2>
                    <p>{feedback?.kind === 'success' ? feedback.text : 'Провайдер отвечает, а состояние первого запуска сохранено.'}</p>
                    <div className="onboarding-success-meta"><span><i /> provider connected</span><span><i /> onboarding persisted</span></div>
                    <button className="button button--accent" onClick={onComplete} type="button">Открыть Chat <Icon name="chevron-right" width={14} height={14} /></button>
                    {feedback?.kind === 'error' && renderFeedback}
                  </section>
                )}
              </>
            )}
          </section>

          <aside className="onboarding-aside">
            <div className="onboarding-aside__orb"><span>Y</span><i /><i /><i /></div>
            <span className="section-heading__overline">LOCAL-FIRST / 01</span>
            <h2>Твои данные остаются твоими.</h2>
            <p>Настройка провайдера не выдаёт доступ к файлам или внешним действиям. Разрешения включаются отдельно и проверяются перед каждым side effect.</p>
            <div className="onboarding-aside__line"><span /> <small>typed bridge smoke</small></div>
            <p className="onboarding-aside__footnote">Нет Wails runtime? UI всё равно запускается в безопасном mock preview, чтобы проверить этот flow локально.</p>
          </aside>
        </div>
      </main>
    </div>
  )
}
