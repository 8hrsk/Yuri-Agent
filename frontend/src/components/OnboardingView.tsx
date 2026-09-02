import { useCallback, useEffect, useMemo, useState } from 'react'

import { createYuriClient } from '../lib/client'
import { googleAIStudioSettings, openRouterSettings } from '../lib/client/settings'
import { clearAgentDraft, loadAgentDraft, newAgentDraft } from '../lib/agents'
import { isOnboardingComplete, onboardingStepIndex, onboardingSteps, validateOnboardingProvider, type OnboardingStep } from '../lib/onboarding'
import type { AgentProfileInput, CodexAccount, CodexModel, OpenAIModel, OpenAIModelSort, ProviderSettings, YuriClient } from '../lib/contracts'
import { AgentProfileForm } from './AgentProfileForm'
import { Icon } from './Icon'
import { OpenAIModelPicker } from './OpenAIModelPicker'

type OnboardingViewProps = {
  client?: YuriClient
  onComplete: () => void
}

type Feedback = {
  kind: 'success' | 'error'
  text: string
}

type BusyState = 'loading' | 'agent' | 'oauth' | 'testing' | undefined

const defaultSettings: ProviderSettings = {
  ...openRouterSettings,
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
  const [openAISettings, setOpenAISettings] = useState<ProviderSettings>(defaultSettings)
  const [googleSettings, setGoogleSettings] = useState<ProviderSettings>(googleAIStudioSettings)
  const [openAIModels, setOpenAIModels] = useState<OpenAIModel[]>([])
  const [modelSort, setModelSort] = useState<OpenAIModelSort>('')
  const [loadingModels, setLoadingModels] = useState(false)
  const [apiKey, setApiKey] = useState('')
  const [agentDraft, setAgentDraft] = useState<AgentProfileInput>(() => loadAgentDraft(newAgentDraft({ name: '', preferences: '' })))
  const [codex, setCodex] = useState<CodexAccount>({ connected: false })
  const [codexModels, setCodexModels] = useState<CodexModel[]>([])
  const [busy, setBusy] = useState<BusyState>('loading')
  const [loadError, setLoadError] = useState<string>()
  const [feedback, setFeedback] = useState<Feedback>()

  const load = useCallback(async () => {
    setBusy('loading')
    setLoadError(undefined)
    try {
      const [snapshot, onboarding, agents] = await Promise.all([
        client.getProviderSnapshot(),
        client.getOnboardingState(),
        client.listAgents(),
      ])
      setSettings(snapshot.settings)
      setOpenAISettings(snapshot.openAI ?? defaultSettings)
      setGoogleSettings(snapshot.googleAIStudio ?? googleAIStudioSettings)
      setCodex(snapshot.codex)
      setCodexModels(snapshot.codex.connected ? await client.getCodexModels().catch(() => []) : [])
      if (isOnboardingComplete(onboarding)) {
        onComplete()
        return
      }
      setStep(onboarding.agentConfigured || agents.length > 0 ? 'provider' : 'welcome')
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
    setSettings((current) => {
      const next = { ...current, [key]: value }
      if (next.kind === 'openai-compatible') setOpenAISettings(next)
      if (next.kind === 'google-ai-studio') setGoogleSettings(next)
      return next
    })
    setFeedback(undefined)
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
      setFeedback({ kind: 'success', text: `Ключ сохранён в системном keyring. Выберите одну из ${models.length} моделей.` })
    } catch (cause) {
      setFeedback({ kind: 'error', text: errorMessage(cause, 'Не удалось сохранить ключ или получить каталог моделей.') })
    } finally {
      setLoadingModels(false)
    }
  }

  const handleLoadOpenAIModels = async (sort: OpenAIModelSort) => {
    setModelSort(sort)
    setLoadingModels(true)
    try {
      setOpenAIModels(await client.getOpenAIModels(settings.providerId ?? 'openrouter', sort))
    } catch (cause) {
      setFeedback({ kind: 'error', text: errorMessage(cause, 'Не удалось обновить каталог моделей.') })
    } finally {
      setLoadingModels(false)
    }
  }

  const handleToggleModelFavorite = async (model: OpenAIModel) => {
    const favorite = !model.favorite
    try {
      await client.setOpenAIModelFavorite(settings.providerId ?? 'openrouter', model.id, favorite)
      setOpenAIModels((current) => current.map((item) => item.id === model.id ? { ...item, favorite } : item))
      const favorites = new Set(settings.favoriteModels)
      if (favorite) favorites.add(model.id)
      else favorites.delete(model.id)
      updateSettings('favoriteModels', [...favorites])
    } catch (cause) {
      setFeedback({ kind: 'error', text: errorMessage(cause, 'Не удалось обновить избранное.') })
    }
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

  const handleCreateAgent = async () => {
    setBusy('agent')
    setFeedback(undefined)
    try {
      const agent = await client.createAgent(agentDraft)
      clearAgentDraft()
      setFeedback({ kind: 'success', text: `Агент ${agent.name} создан и выбран как активный.` })
      const onboarding = await client.getOnboardingState()
      if (isOnboardingComplete(onboarding)) onComplete()
      else setStep('provider')
    } catch (cause) {
      setFeedback({ kind: 'error', text: errorMessage(cause, 'Не удалось создать агента.') })
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
        setSettings((current) => ({ ...current, kind: 'codex-app-server', model: current.kind === 'codex-app-server' ? current.model : '' }))
        setCodexModels(await client.getCodexModels().catch(() => []))
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

        <div className={step === 'agent' ? 'onboarding-layout onboarding-layout--agent' : 'onboarding-layout'}>
          <section className="onboarding-main" aria-labelledby="onboarding-title">
            <div className="onboarding-eyebrow"><span className="eyebrow-dot" /> FIRST-RUN SETUP</div>
            <h1 id="onboarding-title">Создадим вашего агента<span className="title-dot">.</span></h1>
            <p className="onboarding-lead">Задайте исходную личность, затем подключите модель. Идентичность хранится локально, а секреты не сохраняются в renderer.</p>
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
                    <h2>Сначала — кто будет рядом с вами.</h2>
                    <p>Создайте именованного агента с собственной исходной личностью. Имя, возраст и гендер остаются под вашим контролем; изменяемые черты смогут развиваться со временем.</p>
                    <div className="onboarding-facts">
                      <span><Icon name="shield" width={14} height={14} /> deny-by-default permissions</span>
                      <span><Icon name="lock" width={14} height={14} /> secrets stay in keyring</span>
                    </div>
                    <button className="button button--accent" onClick={() => { setFeedback(undefined); setStep('agent') }} type="button">Создать агента <Icon name="chevron-right" width={14} height={14} /></button>
                  </section>
                )}

                {step === 'agent' && (
                  <section className="onboarding-panel onboarding-panel--agent" aria-labelledby="agent-onboarding-title">
                    <div className="onboarding-panel__heading"><div><span className="section-heading__overline">Шаг 2 · identity seed</span><h2 id="agent-onboarding-title">Исходная личность агента</h2></div><span className="onboarding-provider-state"><i /> LOCAL</span></div>
                    <p className="onboarding-panel__hint">Эти поля задаёт владелец. Фоновая рефлексия сможет менять только незакреплённые черты, но не базовую идентичность.</p>
                    <AgentProfileForm busy={busy === 'agent'} onBack={() => { setFeedback(undefined); setStep('welcome') }} onChange={setAgentDraft} onSubmit={() => void handleCreateAgent()} value={agentDraft} />
                    {renderFeedback}
                  </section>
                )}

                {step === 'provider' && (
                  <section className="onboarding-panel onboarding-panel--provider" aria-labelledby="provider-onboarding-title">
                    <div className="onboarding-panel__heading">
                      <div><span className="section-heading__overline">Шаг 3 · connection check</span><h2 id="provider-onboarding-title">Подключите провайдера</h2></div>
                      <span className="onboarding-provider-state"><i /> SETUP</span>
                    </div>
                    <div aria-label="Выбор провайдера" className="onboarding-provider-tabs" role="tablist">
                      <button aria-selected={settings.kind === 'openai-compatible'} className={settings.kind === 'openai-compatible' ? 'onboarding-provider-tab onboarding-provider-tab--active' : 'onboarding-provider-tab'} onClick={() => { setSettings(openAISettings); setFeedback(undefined) }} role="tab" type="button"><span className="provider-tab__logo">O</span><span><strong>OpenAI-compatible</strong><small>OpenRouter · API key</small></span></button>
                      <button aria-selected={settings.kind === 'codex-app-server'} className={settings.kind === 'codex-app-server' ? 'onboarding-provider-tab onboarding-provider-tab--active' : 'onboarding-provider-tab'} onClick={() => setSettings((current) => ({ ...current, kind: 'codex-app-server', model: current.kind === 'codex-app-server' ? current.model : '' }))} role="tab" type="button"><span className="provider-tab__logo provider-tab__logo--codex">C</span><span><strong>Codex App Server</strong><small>ChatGPT OAuth</small></span></button>
                      <button aria-selected={settings.kind === 'google-ai-studio'} className={settings.kind === 'google-ai-studio' ? 'onboarding-provider-tab onboarding-provider-tab--active' : 'onboarding-provider-tab'} onClick={() => { setSettings(googleSettings); setFeedback(undefined) }} role="tab" type="button"><span className="provider-tab__logo">G</span><span><strong>Google AI Studio</strong><small>Gemini API key · Free Tier</small></span></button>
                      <button aria-selected={settings.kind === 'antigravity'} className={settings.kind === 'antigravity' ? 'onboarding-provider-tab onboarding-provider-tab--active' : 'onboarding-provider-tab'} onClick={() => updateSettings('kind', 'antigravity')} role="tab" type="button"><span className="provider-tab__logo">A</span><span><strong>Antigravity</strong><small>OAuth unavailable</small></span></button>
                    </div>
                    {settings.kind === 'openai-compatible' || settings.kind === 'google-ai-studio' ? (
                      <>
                        <p className="onboarding-panel__hint">{settings.kind === 'google-ai-studio' ? 'Google AI Studio использует Gemini OpenAI-compatible endpoint. Ключ передаётся только в защищённый provider bridge и сохраняется в системном keyring; Free Tier запросы сериализуются slow mode.' : 'OpenRouter использует OpenAI-compatible Chat Completions API. Ключ передаётся только в защищённый provider bridge и сохраняется в системном keyring.'}</p>
                        <form className="onboarding-form" onSubmit={(event) => { event.preventDefault(); void handleProbe() }}>
                          <label htmlFor="onboarding-base-url"><span>Base URL</span><input autoComplete="url" disabled={settings.kind === 'google-ai-studio'} id="onboarding-base-url" onChange={(event) => updateSettings('baseUrl', event.target.value)} spellCheck={false} type="url" value={settings.baseUrl} /></label>
                          <label htmlFor="onboarding-api-key"><span>API key <small>{settings.apiKeyConfigured ? '· сохранён в keyring' : '· обязателен для каталога'}</small></span><input autoComplete="new-password" id="onboarding-api-key" onChange={(event) => setApiKey(event.target.value)} placeholder={settings.apiKeyConfigured ? 'Оставьте пустым, чтобы сохранить текущий' : settings.kind === 'google-ai-studio' ? 'AIza…' : 'sk-or-v1-…'} type="password" value={apiKey} /></label>
                          <button className="button button--quiet button--wide" disabled={loadingModels || (!apiKey.trim() && !settings.apiKeyConfigured)} onClick={() => void handleConnectOpenAI()} type="button"><Icon name="lock" width={14} height={14} /> {loadingModels ? 'Загружаю модели…' : 'Сохранить ключ и загрузить модели'}</button>
                          {(settings.apiKeyConfigured || openAIModels.length > 0) && <OpenAIModelPicker loading={loadingModels} models={openAIModels} onReload={(sort) => void handleLoadOpenAIModels(sort)} onSelect={(model) => updateSettings('model', model)} onToggleFavorite={(model) => void handleToggleModelFavorite(model)} sort={modelSort} value={settings.model} />}
                          <label htmlFor="onboarding-model"><span>Model <small>· из каталога или вручную</small></span><input autoComplete="off" id="onboarding-model" onChange={(event) => updateSettings('model', event.target.value)} placeholder={settings.kind === 'google-ai-studio' ? 'gemini-2.5-flash' : 'openai/gpt-4.1-mini'} spellCheck={false} value={settings.model} /></label>
                          {settings.kind === 'google-ai-studio' && <label><span>Quota mode</span><select onChange={(event) => updateSettings('quotaMode', event.target.value as ProviderSettings['quotaMode'])} value={settings.quotaMode ?? 'free-tier'}><option value="free-tier">Free Tier · slow mode</option><option value="custom">Custom limits</option><option value="off">Off</option></select></label>}
                          <div className="onboarding-form__actions"><button className="button button--accent" disabled={busy === 'testing' || busy === 'oauth'} type="submit">{busy === 'testing' ? 'Сохраняю и проверяю…' : 'Сохранить и проверить'} <Icon name="arrow-up" width={14} height={14} /></button></div>
                        </form>
                      </>
                    ) : settings.kind === 'codex-app-server' ? (
                      <div className="onboarding-codex">
                        <div className="codex-account__row"><div className="codex-account__avatar">{codex.connected ? '✓' : 'C'}</div><div><strong>{codex.connected ? codex.email : 'Аккаунт ChatGPT не подключён'}</strong><small>{codex.connected ? `${codex.plan ?? 'ChatGPT'} · готов к проверке` : 'Для Codex App Server нужен официальный OAuth-поток.'}</small></div></div>
                        {codex.connected && <label className="codex-model-picker"><span>Модель</span><select aria-label="Модель Codex" onChange={(event) => updateSettings('model', event.target.value)} value={settings.model}><option value="">Автоматически · {codexModels.find((model) => model.isDefault)?.displayName ?? 'модель аккаунта'}</option>{codexModels.map((model) => <option key={model.id} value={model.model}>{model.displayName}{model.isDefault ? ' · default' : ''}</option>)}</select><small>{settings.model ? codexModels.find((model) => model.model === settings.model)?.description : 'Codex выберет модель по умолчанию для вашего аккаунта.'}</small></label>}
                        <p className="onboarding-panel__hint">OAuth остаётся явным действием пользователя. Yuri не получает и не показывает токен, а backend сам выполняет probe после подключения.</p>
                        {!codex.connected && <button className="button button--quiet button--wide" disabled={busy === 'oauth' || busy === 'testing'} onClick={() => void handleLogin()} type="button"><Icon name="command" width={15} height={15} />{busy === 'oauth' ? 'Открываю OAuth…' : 'Войти через ChatGPT'}</button>}
                        <div className="onboarding-form__actions"><button className="button button--accent" disabled={!codex.connected || busy === 'testing' || busy === 'oauth'} onClick={() => void handleProbe()} type="button">{busy === 'testing' ? 'Проверяю…' : 'Проверить Codex'} <Icon name="arrow-up" width={14} height={14} /></button></div>
                      </div>
                    ) : (
                      <div className="onboarding-codex">
                        <div className="codex-account__row"><div className="codex-account__avatar">A</div><div><strong>Antigravity OAuth недоступен</strong><small>unsupported_auth_mode · конфигурация не будет сохранена</small></div></div>
                        <p className="onboarding-panel__hint">Yuri не импортирует токены Gemini CLI, browser cookies и token cache и не имитирует официальный клиент. Интеграция появится только после публикации разрешённого vendor contract.</p>
                        <button className="button button--quiet button--wide" onClick={() => { setSettings(openAISettings); setFeedback(undefined) }} type="button">Использовать API key через совместимый endpoint</button>
                        <div className="onboarding-form__actions" />
                      </div>
                    )}
                    {renderFeedback}
                  </section>
                )}

                {step === 'success' && (
                  <section className="onboarding-panel onboarding-panel--success" aria-labelledby="onboarding-success-title">
                    <div className="onboarding-success-mark"><Icon name="check" width={27} height={27} /></div>
                    <span className="section-heading__overline">Шаг 4 · ready</span>
                    <h2 id="onboarding-success-title">Агент готов к диалогу.</h2>
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
