import { useMemo, useState } from 'react'

import type { OpenAIModel, OpenAIModelSort } from '../lib/contracts'
import { Icon } from './Icon'

type OpenAIModelPickerProps = {
  models: OpenAIModel[]
  value: string
  loading: boolean
  sort: OpenAIModelSort
  onReload: (sort: OpenAIModelSort) => void
  onSelect: (model: string) => void
  onToggleFavorite: (model: OpenAIModel) => void
}

const sortOptions: Array<{ value: OpenAIModelSort; label: string }> = [
  { value: '', label: 'По умолчанию' },
  { value: 'pricing-low-to-high', label: 'Сначала дешевле' },
  { value: 'pricing-high-to-low', label: 'Сначала дороже' },
  { value: 'context-high-to-low', label: 'Больше контекст' },
  { value: 'throughput-high-to-low', label: 'Выше скорость' },
  { value: 'latency-low-to-high', label: 'Ниже задержка' },
  { value: 'most-popular', label: 'Популярные' },
  { value: 'newest', label: 'Новые' },
]

function tokenPrice(value?: string): string | undefined {
  if (!value) return undefined
  const price = Number(value)
  if (!Number.isFinite(price)) return undefined
  if (price === 0) return '$0'
  const perMillion = price * 1_000_000
  return `$${perMillion < 0.01 ? perMillion.toPrecision(2) : perMillion.toFixed(perMillion < 1 ? 2 : 1)}`
}

function compactTokens(value: number): string {
  if (!value) return '—'
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(value % 1_000_000 === 0 ? 0 : 1)}M`
  if (value >= 1_000) return `${Math.round(value / 1_000)}K`
  return String(value)
}

export function OpenAIModelPicker({ models, value, loading, sort, onReload, onSelect, onToggleFavorite }: OpenAIModelPickerProps) {
  const [query, setQuery] = useState('')
  const [freeOnly, setFreeOnly] = useState(false)
  const [favoritesOnly, setFavoritesOnly] = useState(false)
  const [toolsOnly, setToolsOnly] = useState(false)

  const visibleModels = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase('ru-RU')
    return models.filter((model) => {
      if (freeOnly && !model.free) return false
      if (favoritesOnly && !model.favorite) return false
      if (toolsOnly && !model.supportsTools) return false
      if (!needle) return true
      return `${model.name} ${model.id} ${model.description ?? ''}`.toLocaleLowerCase('ru-RU').includes(needle)
    })
  }, [favoritesOnly, freeOnly, models, query, toolsOnly])

  return (
    <section aria-label="Каталог моделей" className="model-catalog">
      <div className="model-catalog__toolbar">
        <label className="model-catalog__search">
          <span className="sr-only">Поиск модели</span>
          <Icon name="search" width={13} height={13} />
          <input onChange={(event) => setQuery(event.target.value)} placeholder="Название или model ID" spellCheck={false} value={query} />
        </label>
        <label className="model-catalog__sort">
          <span className="sr-only">Сортировка моделей</span>
          <select disabled={loading} onChange={(event) => onReload(event.target.value as OpenAIModelSort)} value={sort}>
            {sortOptions.map((option) => <option key={option.value || 'default'} value={option.value}>{option.label}</option>)}
          </select>
        </label>
        <button aria-label="Обновить список моделей" className="model-catalog__reload" disabled={loading} onClick={() => onReload(sort)} type="button"><Icon name="refresh" width={13} height={13} /></button>
      </div>
      <div className="model-catalog__filters" aria-label="Фильтры моделей">
        <button aria-pressed={favoritesOnly} className={favoritesOnly ? 'model-filter model-filter--active' : 'model-filter'} onClick={() => setFavoritesOnly((current) => !current)} type="button">★ Избранное</button>
        <button aria-pressed={freeOnly} className={freeOnly ? 'model-filter model-filter--active' : 'model-filter'} onClick={() => setFreeOnly((current) => !current)} type="button">Бесплатные</button>
        <button aria-pressed={toolsOnly} className={toolsOnly ? 'model-filter model-filter--active' : 'model-filter'} onClick={() => setToolsOnly((current) => !current)} type="button">Tools</button>
        <span>{visibleModels.length} / {models.length}</span>
      </div>
      <div className="model-catalog__list" role="listbox" aria-busy={loading} aria-label="Модели OpenRouter">
        {loading ? <div className="model-catalog__empty" role="status">Загружаю каталог OpenRouter…</div> : visibleModels.length === 0 ? <div className="model-catalog__empty">По выбранным фильтрам моделей нет.</div> : visibleModels.map((model) => {
          const selected = model.id === value
          const promptPrice = tokenPrice(model.promptPrice)
          const completionPrice = tokenPrice(model.completionPrice)
          return (
            <div aria-selected={selected} className={selected ? 'model-option model-option--selected' : 'model-option'} key={model.id} role="option">
              <button className="model-option__select" onClick={() => onSelect(model.id)} type="button">
                <span className="model-option__title"><strong>{model.name}</strong><code>{model.id}</code></span>
                <span className="model-option__badges">
                  {model.free && <i className="model-badge model-badge--free">FREE</i>}
                  {model.supportsTools && <i className="model-badge">TOOLS</i>}
                  {model.inputModalities.includes('image') && <i className="model-badge">VISION</i>}
                </span>
                <span className="model-option__meta">
                  <span>Context <strong>{compactTokens(model.contextLength)}</strong></span>
                  {(promptPrice || completionPrice) && <span>In / out <strong>{promptPrice ?? '—'} / {completionPrice ?? '—'} / 1M</strong></span>}
                </span>
                {model.description && <small>{model.description}</small>}
              </button>
              <button aria-label={model.favorite ? `Убрать ${model.name} из избранного` : `Добавить ${model.name} в избранное`} aria-pressed={model.favorite} className={model.favorite ? 'model-option__favorite model-option__favorite--active' : 'model-option__favorite'} onClick={() => onToggleFavorite(model)} type="button">★</button>
            </div>
          )
        })}
      </div>
    </section>
  )
}
