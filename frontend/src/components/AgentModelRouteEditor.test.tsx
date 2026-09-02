// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { resetYuriClientForTests } from '../lib/client'
import { AgentModelRouteEditor } from './AgentModelRouteEditor'

describe('AgentModelRouteEditor', () => {
  beforeEach(() => {
    delete (window as typeof window & { go?: unknown }).go
    resetYuriClientForTests()
  })

  it('offers configured providers and loads the selected provider model catalog', async () => {
    const onChange = vi.fn()
    ;(window as typeof window & { go?: unknown }).go = {
      main: {
        Bridge: {
          ListConversations: () => [],
          ListProviders: () => [
            { id: 'codex', kind: 'codex-app-server', displayName: 'Codex OAuth', model: '', enabled: true },
            { id: 'openrouter', kind: 'openai-compatible', displayName: 'OpenRouter', model: 'openrouter/free', enabled: false, hasSecret: true },
          ],
          ListCodexModels: () => [{ id: 'gpt-5.6', model: 'gpt-5.6', displayName: 'GPT-5.6', isDefault: true, inputModalities: ['text'] }],
        },
      },
    }
    resetYuriClientForTests()

    const onFallbackChange = vi.fn()
    const { rerender } = render(<AgentModelRouteEditor fallbackEnabled={false} fallbackModel="" fallbackProviderId="" model="" onChange={onChange} onFallbackChange={onFallbackChange} providerId="" />)
    const provider = await screen.findByRole('combobox', { name: 'Provider' })
    expect(screen.getAllByRole('option', { name: /OpenRouter/ })).toHaveLength(2)

    fireEvent.change(provider, { target: { value: 'codex' } })
    expect(onChange).toHaveBeenCalledWith('codex', '')
    rerender(<AgentModelRouteEditor fallbackEnabled={false} fallbackModel="" fallbackProviderId="" model="" onChange={onChange} onFallbackChange={onFallbackChange} providerId="codex" />)

    await waitFor(() => expect(screen.getByRole('option', { name: /GPT-5.6/ })).toBeInTheDocument())
    fireEvent.change(screen.getByRole('combobox', { name: 'Codex model' }), { target: { value: 'gpt-5.6' } })
    expect(onChange).toHaveBeenLastCalledWith('codex', 'gpt-5.6')
  })

  it('keeps the installation-wide primary provider distinct from the fallback route', async () => {
    ;(window as typeof window & { go?: unknown }).go = { main: { Bridge: { ListConversations: () => [], ListProviders: () => [] } } }
    resetYuriClientForTests()
    render(<AgentModelRouteEditor fallbackEnabled={false} fallbackModel="" fallbackProviderId="" model="" onChange={vi.fn()} onFallbackChange={vi.fn()} providerId="" />)

    expect(await screen.findByRole('option', { name: /Активный provider приложения/ })).toHaveValue('')
    expect(screen.queryByRole('option', { name: /Активный provider приложения.*fallback/i })).not.toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Резервный provider и модель' })).toBeInTheDocument()
    expect(screen.getByText(/наследует глобальную настройку/)).toBeInTheDocument()
  })

  it('exposes the fallback switch and keeps its provider/model changes separate', async () => {
    const onFallbackChange = vi.fn()
    ;(window as typeof window & { go?: unknown }).go = {
      main: {
        Bridge: {
          ListConversations: () => [],
          ListProviders: () => [
            { id: 'openrouter', kind: 'openai-compatible', displayName: 'OpenRouter', model: 'openrouter/free', enabled: true, hasSecret: true },
          ],
        },
      },
    }
    resetYuriClientForTests()

    render(<AgentModelRouteEditor fallbackEnabled={false} fallbackModel="" fallbackProviderId="" model="" onChange={vi.fn()} onFallbackChange={onFallbackChange} providerId="" />)

    const provider = await screen.findByRole('combobox', { name: 'Fallback provider' })
    fireEvent.change(provider, { target: { value: 'openrouter' } })
    expect(onFallbackChange).toHaveBeenCalledWith(false, 'openrouter', 'openrouter/free')

    fireEvent.click(screen.getByRole('switch', { name: 'Включить резервный маршрут' }))
    expect(onFallbackChange).toHaveBeenLastCalledWith(true, '', '')
    expect(screen.getByText(/только до первого видимого токена или tool side effect/)).toBeInTheDocument()
  })

  it('warns when the selected catalog model does not support tools', async () => {
    ;(window as typeof window & { go?: unknown }).go = {
      main: {
        Bridge: {
          ListConversations: () => [],
          ListProviders: () => [
            { id: 'openrouter', kind: 'openai-compatible', displayName: 'OpenRouter', model: 'vendor/text-only', enabled: true, hasSecret: true },
          ],
          ListOpenAIModels: () => [
            { id: 'vendor/text-only', name: 'Text Only', context_length: 32_000, supports_tools: false, input_modalities: ['text'], output_modalities: ['text'] },
          ],
        },
      },
    }
    resetYuriClientForTests()

    render(<AgentModelRouteEditor fallbackEnabled={false} fallbackModel="" fallbackProviderId="" model="vendor/text-only" onChange={vi.fn()} onFallbackChange={vi.fn()} providerId="openrouter" />)

    expect(await screen.findByRole('alert')).toHaveTextContent(/нет поддержки tools/)
    expect(screen.getByText(/обязательные вызовы инструментов будут остановлены/)).toBeInTheDocument()
  })

  it('loads Google AI Studio models for primary and fallback routes', async () => {
    const onChange = vi.fn()
    const onFallbackChange = vi.fn()
    ;(window as typeof window & { go?: unknown }).go = {
      main: {
        Bridge: {
          ListConversations: () => [],
          ListProviders: () => [
            { id: 'google-ai-studio', kind: 'google-ai-studio', displayName: 'Google AI Studio', model: 'gemini-2.5-flash', enabled: true, hasSecret: true },
          ],
          ListOpenAIModels: () => [
            { id: 'gemini-2.5-flash', name: 'Gemini 2.5 Flash', context_length: 1_048_576, input_modalities: ['text'], output_modalities: ['text'] },
          ],
        },
      },
    }
    resetYuriClientForTests()

    render(<AgentModelRouteEditor fallbackEnabled={false} fallbackModel="" fallbackProviderId="" model="gemini-2.5-flash" onChange={onChange} onFallbackChange={onFallbackChange} providerId="google-ai-studio" />)

    expect(await screen.findByText('Gemini 2.5 Flash')).toBeInTheDocument()
    fireEvent.change(screen.getByRole('combobox', { name: 'Fallback provider' }), { target: { value: 'google-ai-studio' } })
    expect(onFallbackChange).toHaveBeenCalledWith(false, 'google-ai-studio', 'gemini-2.5-flash')
  })
})
