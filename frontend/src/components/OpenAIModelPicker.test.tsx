// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import type { OpenAIModel } from '../lib/contracts'
import { OpenAIModelPicker } from './OpenAIModelPicker'

const models: OpenAIModel[] = [
  { id: 'openrouter/free', name: 'Free Router', contextLength: 200_000, maxCompletionTokens: 16_384, promptPrice: '0', completionPrice: '0', free: true, supportsTools: true, inputModalities: ['text'], outputModalities: ['text'], favorite: true },
  { id: 'vendor/vision', name: 'Vision Pro', contextLength: 128_000, maxCompletionTokens: 8_192, promptPrice: '0.000003', completionPrice: '0.000015', free: false, supportsTools: false, inputModalities: ['text', 'image'], outputModalities: ['text'], favorite: false },
]

describe('OpenAIModelPicker', () => {
  it('filters the catalog and keeps selection and favorites as explicit actions', async () => {
    const user = userEvent.setup()
    const onSelect = vi.fn()
    const onToggleFavorite = vi.fn()
    render(<OpenAIModelPicker loading={false} models={models} onReload={vi.fn()} onSelect={onSelect} onToggleFavorite={onToggleFavorite} sort="" value="" />)

    await user.click(screen.getByRole('button', { name: 'Бесплатные' }))
    expect(screen.getByRole('option', { name: /Free Router/ })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: /Vision Pro/ })).not.toBeInTheDocument()

    await user.click(screen.getByText('Free Router'))
    expect(onSelect).toHaveBeenCalledWith('openrouter/free')
    await user.click(screen.getByRole('button', { name: 'Убрать Free Router из избранного' }))
    expect(onToggleFavorite).toHaveBeenCalledWith(models[0])
  })

  it('asks the backend for OpenRouter-native sorting', async () => {
    const user = userEvent.setup()
    const onReload = vi.fn()
    render(<OpenAIModelPicker loading={false} models={models} onReload={onReload} onSelect={vi.fn()} onToggleFavorite={vi.fn()} sort="" value="openrouter/free" />)

    await user.selectOptions(screen.getByRole('combobox', { name: 'Сортировка моделей' }), 'throughput-high-to-low')
    expect(onReload).toHaveBeenCalledWith('throughput-high-to-low')
    expect(screen.getByRole('option', { name: /Free Router/ })).toHaveAttribute('aria-selected', 'true')
  })
})
