import { useEffect, useMemo, useState } from 'react'

import { createYuriClient } from '../lib/client'
import type { CodexModel, OpenAIModel, OpenAIModelSort, ProviderOption } from '../lib/contracts'
import { Icon } from './Icon'
import { OpenAIModelPicker } from './OpenAIModelPicker'

type AgentModelRouteEditorProps = {
  providerId: string
  model: string
  disabled?: boolean
  onChange: (providerId: string, model: string) => void
}

export function AgentModelRouteEditor({ providerId, model, disabled, onChange }: AgentModelRouteEditorProps) {
  const client = useMemo(() => createYuriClient(), [])
  const [providers, setProviders] = useState<ProviderOption[]>([])
  const [openAIModels, setOpenAIModels] = useState<OpenAIModel[]>([])
  const [codexModels, setCodexModels] = useState<CodexModel[]>([])
  const [sort, setSort] = useState<OpenAIModelSort>('')
  const [loading, setLoading] = useState(true)
  const [loadingModels, setLoadingModels] = useState(false)
  const [error, setError] = useState<string>()
  const selected = providers.find((provider) => provider.id === providerId)

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

  useEffect(() => {
    if (selected) void loadModels(selected)
    // The selected provider identity is the only automatic reload boundary.
    // Sorting is handled explicitly by the picker to avoid duplicate calls.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected?.id])

  const selectProvider = (nextID: string) => {
    const next = providers.find((provider) => provider.id === nextID)
    onChange(nextID, next?.model ?? '')
  }

  const toggleFavorite = async (item: OpenAIModel) => {
    if (!selected) return
    await client.setOpenAIModelFavorite(selected.id, item.id, !item.favorite)
    setOpenAIModels((current) => current.map((candidate) => candidate.id === item.id ? { ...candidate, favorite: !candidate.favorite } : candidate))
  }

  return <section className="agent-model-route" aria-label="Модель агента">
    <div className="agent-model-route__heading">
      <div><span>MODEL ROUTE</span><h4>Provider и модель этого агента</h4></div>
      {providerId ? <small><i className="agent-model-route__dot" /> независимая привязка</small> : <small>наследует глобальную настройку</small>}
    </div>
    <p>Привязка применяется к чату, фоновым ответам и внутренним диалогам этого агента. API keys остаются в системном keyring.</p>
    <label><span>Provider</span><select disabled={disabled || loading} onChange={(event) => selectProvider(event.target.value)} value={providerId}>
      <option value="">Активный provider приложения · fallback</option>
      {providers.filter((provider) => provider.kind !== 'antigravity').map((provider) => <option key={provider.id} value={provider.id}>{provider.displayName}{provider.enabled ? ' · active default' : ''}</option>)}
    </select></label>
    {selected?.kind === 'openai-compatible' && <>
      <OpenAIModelPicker loading={loadingModels} models={openAIModels} onReload={(nextSort) => void loadModels(selected, nextSort)} onSelect={(nextModel) => onChange(selected.id, nextModel)} onToggleFavorite={(item) => void toggleFavorite(item)} sort={sort} value={model} />
      <label><span>Model ID <small>· можно ввести вручную</small></span><input disabled={disabled} onChange={(event) => onChange(selected.id, event.target.value)} placeholder={selected.model || 'provider/model'} spellCheck={false} value={model} /></label>
    </>}
    {selected?.kind === 'codex-app-server' && <label><span>Codex model</span><select disabled={disabled || loadingModels} onChange={(event) => onChange(selected.id, event.target.value)} value={model}>
      <option value="">Автоматически · provider default</option>
      {codexModels.map((item) => <option key={item.id} value={item.model}>{item.displayName}{item.isDefault ? ' · default' : ''}</option>)}
    </select></label>}
    {error && <div className="agent-model-route__error" role="alert"><Icon name="warning" width={13} height={13} /> {error}</div>}
    {!loading && providers.length === 0 && <div className="agent-model-route__error"><Icon name="warning" width={13} height={13} /> Сначала настройте хотя бы один provider в Settings.</div>}
  </section>
}
