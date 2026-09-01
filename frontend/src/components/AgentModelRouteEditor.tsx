import { useEffect, useMemo, useState, type ReactNode } from 'react'

import { createYuriClient } from '../lib/client'
import type { CodexModel, OpenAIModel, OpenAIModelSort, ProviderOption } from '../lib/contracts'
import { Icon } from './Icon'
import { OpenAIModelPicker } from './OpenAIModelPicker'

type AgentModelRouteEditorProps = {
  providerId: string
  model: string
  fallbackEnabled: boolean
  fallbackProviderId: string
  fallbackModel: string
  disabled?: boolean
  primaryAction?: ReactNode
  onChange: (providerId: string, model: string) => void
  onFallbackChange: (enabled: boolean, providerId: string, model: string) => void
}

export function AgentModelRouteEditor({ providerId, model, fallbackEnabled, fallbackProviderId, fallbackModel, disabled, primaryAction, onChange, onFallbackChange }: AgentModelRouteEditorProps) {
  const client = useMemo(() => createYuriClient(), [])
  const [providers, setProviders] = useState<ProviderOption[]>([])
  const [openAIModels, setOpenAIModels] = useState<OpenAIModel[]>([])
  const [codexModels, setCodexModels] = useState<CodexModel[]>([])
  const [fallbackOpenAIModels, setFallbackOpenAIModels] = useState<OpenAIModel[]>([])
  const [fallbackCodexModels, setFallbackCodexModels] = useState<CodexModel[]>([])
  const [sort, setSort] = useState<OpenAIModelSort>('')
  const [loading, setLoading] = useState(true)
  const [loadingModels, setLoadingModels] = useState(false)
  const [loadingFallbackModels, setLoadingFallbackModels] = useState(false)
  const [error, setError] = useState<string>()
  const selected = providers.find((provider) => provider.id === providerId)
  const fallbackSelected = providers.find((provider) => provider.id === fallbackProviderId)
  const selectedOpenAIModel = selected?.kind === 'openai-compatible'
    ? openAIModels.find((candidate) => candidate.id === model)
    : undefined

  useEffect(() => {
    let mounted = true
    if (typeof client.listProviders !== 'function') {
      setLoading(false)
      return () => { mounted = false }
    }
    void client.listProviders().then((items) => { if (mounted) setProviders(items) }).catch((cause) => {
      if (mounted) setError(cause instanceof Error ? cause.message : 'Не удалось загрузить providers.')
    }).finally(() => { if (mounted) setLoading(false) })
    return () => { mounted = false }
  }, [client])

  const loadModels = async (provider: ProviderOption | undefined, nextSort: OpenAIModelSort = sort) => {
    if (!provider) return
    setLoadingModels(true)
    setError(undefined)
    try {
      if (provider.kind === 'openai-compatible') {
        setSort(nextSort)
        setOpenAIModels(await client.getOpenAIModels(provider.id, nextSort))
      } else if (provider.kind === 'codex-app-server') {
        setCodexModels(await client.getCodexModels())
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Не удалось загрузить модели provider.')
    } finally {
      setLoadingModels(false)
    }
  }

  const loadFallbackModels = async (provider: ProviderOption | undefined) => {
    if (!provider) return
    setLoadingFallbackModels(true)
    setError(undefined)
    try {
      if (provider.kind === 'openai-compatible') setFallbackOpenAIModels(await client.getOpenAIModels(provider.id))
      else if (provider.kind === 'codex-app-server') setFallbackCodexModels(await client.getCodexModels())
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Не удалось загрузить модели fallback provider.')
    } finally {
      setLoadingFallbackModels(false)
    }
  }

  useEffect(() => {
    if (selected) void loadModels(selected)
    // The selected provider identity is the only automatic reload boundary.
    // Sorting is handled explicitly by the picker to avoid duplicate calls.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected?.id])

  useEffect(() => {
    if (fallbackSelected) void loadFallbackModels(fallbackSelected)
    // The selected fallback provider is the only automatic reload boundary.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fallbackSelected?.id])

  const selectProvider = (nextID: string) => {
    const next = providers.find((provider) => provider.id === nextID)
    onChange(nextID, next?.model ?? '')
  }

  const selectFallbackProvider = (nextID: string) => {
    const next = providers.find((provider) => provider.id === nextID)
    onFallbackChange(fallbackEnabled, nextID, next?.model ?? '')
  }

  const toggleFavorite = async (item: OpenAIModel) => {
    if (!selected) return
    await client.setOpenAIModelFavorite(selected.id, item.id, !item.favorite)
    setOpenAIModels((current) => current.map((candidate) => candidate.id === item.id ? { ...candidate, favorite: !candidate.favorite } : candidate))
  }

  return <section className="agent-model-route" aria-label="Маршруты модели агента">
    <div className="agent-model-route__heading">
      <div><span>MODEL ROUTE</span><h4>Provider и модель этого агента</h4></div>
      {providerId ? <small><i className="agent-model-route__dot" /> независимая привязка</small> : <small>наследует глобальную настройку</small>}
    </div>
    <p>Привязка применяется к чату, фоновым ответам и внутренним диалогам этого агента. API keys остаются в системном keyring.</p>
    <label><span>Provider</span><select disabled={disabled || loading} onChange={(event) => selectProvider(event.target.value)} value={providerId}>
      <option value="">Активный provider приложения · default</option>
      {providers.filter((provider) => provider.kind !== 'antigravity').map((provider) => <option key={provider.id} value={provider.id}>{provider.displayName}{provider.enabled ? ' · active default' : ''}</option>)}
    </select></label>
    {selected?.kind === 'openai-compatible' && <>
      <OpenAIModelPicker loading={loadingModels} models={openAIModels} onReload={(nextSort) => void loadModels(selected, nextSort)} onSelect={(nextModel) => onChange(selected.id, nextModel)} onToggleFavorite={(item) => void toggleFavorite(item)} sort={sort} value={model} />
      <label><span>Model ID <small>· можно ввести вручную</small></span><input disabled={disabled} onChange={(event) => onChange(selected.id, event.target.value)} placeholder={selected.model || 'provider/model'} spellCheck={false} value={model} /></label>
      {selectedOpenAIModel?.supportsToolsKnown && !selectedOpenAIModel.supportsTools && <div className="agent-model-route__warning" role="alert"><Icon name="warning" width={13} height={13} /><span><strong>У этой модели нет поддержки tools</strong><small>Агент сможет отвечать текстом, но обязательные вызовы инструментов будут остановлены до отправки provider. Выберите модель с бейджем TOOLS для задач с файлами, web и субагентами.</small></span></div>}
      {selectedOpenAIModel && !selectedOpenAIModel.supportsToolsKnown && <div className="agent-model-route__hint" role="status"><Icon name="spark" width={13} height={13} /><span>Provider не сообщил capability tools для этой модели. Текстовые ответы разрешены; обязательный tool-вызов будет проверен консервативно.</span></div>}
      {model && !loadingModels && openAIModels.length > 0 && !selectedOpenAIModel && <div className="agent-model-route__hint" role="status"><Icon name="spark" width={13} height={13} /><span>Capabilities для введённой вручную модели неизвестны. Проверьте поддержку tools, vision и structured output у provider.</span></div>}
    </>}
    {selected?.kind === 'codex-app-server' && <label><span>Codex model</span><select disabled={disabled || loadingModels} onChange={(event) => onChange(selected.id, event.target.value)} value={model}>
      <option value="">Автоматически · provider default</option>
      {codexModels.map((item) => <option key={item.id} value={item.model}>{item.displayName}{item.isDefault ? ' · default' : ''}</option>)}
    </select></label>}
    {primaryAction && <div className="agent-model-route__primary-action">{primaryAction}</div>}
    <section aria-label="Резервный маршрут агента" className="agent-model-route__fallback">
      <div className="agent-model-route__fallback-heading"><div><span>FALLBACK ROUTE</span><h5>Резервный provider и модель</h5></div><span className={fallbackEnabled ? 'agent-model-route__fallback-status agent-model-route__fallback-status--on' : 'agent-model-route__fallback-status'}>{fallbackEnabled ? 'включён' : 'выключен'}</span></div>
      <p>Если основной маршрут завершился подходящей provider-ошибкой, Yuri может переключиться сюда только до первого видимого токена или tool side effect. Переключение всегда видно в trace и audit.</p>
      <label className="agent-model-route__fallback-toggle"><span>Разрешить переключение</span><button aria-checked={fallbackEnabled} aria-label="Включить резервный маршрут" className={`toggle${fallbackEnabled ? ' toggle--on' : ''}`} disabled={disabled || loading || providers.length === 0} onClick={() => onFallbackChange(!fallbackEnabled, fallbackProviderId, fallbackModel)} role="switch" type="button"><i /></button></label>
      <label><span>Fallback provider</span><select aria-label="Fallback provider" disabled={disabled || loading || providers.length === 0} onChange={(event) => selectFallbackProvider(event.target.value)} value={fallbackProviderId}>
        <option value="">Выберите provider</option>
        {providers.filter((provider) => provider.kind !== 'antigravity').map((provider) => <option key={provider.id} value={provider.id}>{provider.displayName}</option>)}
      </select></label>
      {fallbackSelected?.kind === 'openai-compatible' && <>
        <label><span>Fallback model</span><select aria-label="Fallback model" disabled={disabled || loadingFallbackModels} onChange={(event) => onFallbackChange(fallbackEnabled, fallbackProviderId, event.target.value)} value={fallbackModel}>
          <option value="">Выберите модель</option>
          {fallbackOpenAIModels.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
        </select></label>
        <label><span>Fallback model ID <small>· можно ввести вручную</small></span><input aria-label="Fallback model ID" disabled={disabled} onChange={(event) => onFallbackChange(fallbackEnabled, fallbackProviderId, event.target.value)} placeholder={fallbackSelected.model || 'provider/model'} spellCheck={false} value={fallbackModel} /></label>
      </>}
      {fallbackSelected?.kind === 'codex-app-server' && <label><span>Codex fallback model</span><select aria-label="Codex fallback model" disabled={disabled || loadingFallbackModels} onChange={(event) => onFallbackChange(fallbackEnabled, fallbackProviderId, event.target.value)} value={fallbackModel}>
        <option value="">Автоматически · provider default</option>
        {fallbackCodexModels.map((item) => <option key={item.id} value={item.model}>{item.displayName}{item.isDefault ? ' · default' : ''}</option>)}
      </select></label>}
      {fallbackProviderId && fallbackSelected?.kind !== 'openai-compatible' && fallbackSelected?.kind !== 'codex-app-server' && <label><span>Fallback model ID</span><input aria-label="Fallback model ID" disabled={disabled} onChange={(event) => onFallbackChange(fallbackEnabled, fallbackProviderId, event.target.value)} spellCheck={false} value={fallbackModel} /></label>}
      {!fallbackProviderId && <small className="agent-model-route__fallback-hint">Сначала выберите отдельный fallback provider.</small>}
    </section>
    {error && <div className="agent-model-route__error" role="alert"><Icon name="warning" width={13} height={13} /> {error}</div>}
    {!loading && providers.length === 0 && <div className="agent-model-route__error"><Icon name="warning" width={13} height={13} /> Сначала настройте хотя бы один provider в Settings.</div>}
  </section>
}
