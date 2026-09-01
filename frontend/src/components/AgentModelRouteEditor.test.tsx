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

    const { rerender } = render(<AgentModelRouteEditor model="" onChange={onChange} providerId="" />)
    const provider = await screen.findByRole('combobox', { name: 'Provider' })
    expect(screen.getByRole('option', { name: /OpenRouter/ })).toBeInTheDocument()

    fireEvent.change(provider, { target: { value: 'codex' } })
    expect(onChange).toHaveBeenCalledWith('codex', '')
    rerender(<AgentModelRouteEditor model="" onChange={onChange} providerId="codex" />)

    await waitFor(() => expect(screen.getByRole('option', { name: /GPT-5.6/ })).toBeInTheDocument())
    fireEvent.change(screen.getByRole('combobox', { name: 'Codex model' }), { target: { value: 'gpt-5.6' } })
    expect(onChange).toHaveBeenLastCalledWith('codex', 'gpt-5.6')
  })

  it('keeps an explicit installation-wide fallback', async () => {
    ;(window as typeof window & { go?: unknown }).go = { main: { Bridge: { ListConversations: () => [], ListProviders: () => [] } } }
    resetYuriClientForTests()
    render(<AgentModelRouteEditor model="" onChange={vi.fn()} providerId="" />)

    expect(await screen.findByRole('option', { name: /fallback/ })).toHaveValue('')
    expect(screen.getByText(/наследует глобальную настройку/)).toBeInTheDocument()
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

    render(<AgentModelRouteEditor model="vendor/text-only" onChange={vi.fn()} providerId="openrouter" />)

    expect(await screen.findByRole('alert')).toHaveTextContent(/нет поддержки tools/)
    expect(screen.getByText(/обязательные вызовы инструментов будут остановлены/)).toBeInTheDocument()
  })
})
