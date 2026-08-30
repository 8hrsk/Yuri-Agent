// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { defaultAgentDraft } from '../lib/agents'
import type { AgentProfileInput } from '../lib/contracts'
import { AgentProfileForm } from './AgentProfileForm'

function renderForm(overrides: Partial<AgentProfileInput> = {}) {
  const onChange = vi.fn<(value: AgentProfileInput) => void>()
  const onSubmit = vi.fn()
  const value: AgentProfileInput = {
    ...defaultAgentDraft,
    ...overrides,
    traits: { ...defaultAgentDraft.traits, ...overrides.traits },
  }
  render(<AgentProfileForm onChange={onChange} onSubmit={onSubmit} value={value} />)
  return { onChange, onSubmit }
}

describe('AgentProfileForm', () => {
  it('shows the backstory field and keeps advanced traits collapsed initially', () => {
    renderForm()

    expect(screen.getByRole('textbox', { name: /Предыстория/ })).toBeInTheDocument()
    expect(screen.getByText(/художественная автобиографическая основа агента/)).toBeInTheDocument()
    expect(screen.getByText('Основные черты')).toBeInTheDocument()

    const details = screen.getByText(/Дополнительные черты/).closest('details')
    expect(details).not.toBeNull()
    expect(details).not.toHaveAttribute('open')
  })

  it('renders every agreed starter trait with a short Russian hint', () => {
    renderForm()

    const labels = [
      'Теплота', 'Прямота', 'Эмоциональность', 'Игривость', 'Ревнивость', 'Раздражительность',
      'Эмпатия', 'Общительность', 'Стеснительность', 'Тревожность', 'Пугливость', 'Эмоциональная устойчивость',
      'Чувствительность', 'Собственнические чувства', 'Романтичность', 'Инициативность', 'Импульсивность',
      'Упрямство', 'Оптимизм', 'Любопытство', 'Подозрительность', 'Доверчивость', 'Привязанность', 'Формальность', 'Цундере',
    ]

    for (const label of labels) {
      expect(screen.getByRole('slider', { name: label })).toBeInTheDocument()
    }
    expect(screen.getByText('Склонность испытывать страх перед угрозами и риском.')).toBeInTheDocument()
  })

  it('forwards backstory edits as part of the typed draft', () => {
    const { onChange } = renderForm()
    const backstory = screen.getByRole('textbox', { name: /Предыстория/ })

    fireEvent.change(backstory, { target: { value: 'Она выросла у моря и любит старые карты.' } })

    expect(onChange).toHaveBeenLastCalledWith(expect.objectContaining({
      backstory: 'Она выросла у моря и любит старые карты.',
    }))
  })
})
