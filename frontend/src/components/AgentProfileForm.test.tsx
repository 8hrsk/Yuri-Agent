// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { useState } from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { cloneAgentDraft, defaultAgentDraft } from '../lib/agents'
import type { AgentProfileInput } from '../lib/contracts'
import { AgentProfileForm } from './AgentProfileForm'

function ProfileHarness({ initial = defaultAgentDraft, onSubmit = vi.fn() }: { initial?: AgentProfileInput; onSubmit?: () => void }) {
  const [value, setValue] = useState(() => cloneAgentDraft(initial))
  return <AgentProfileForm onChange={setValue} onSubmit={onSubmit} value={value} />
}

describe('AgentProfileForm', () => {
  beforeEach(() => window.localStorage.clear())

  it('offers a short Quick flow with visible owner-controlled presets', () => {
    render(<ProfileHarness />)

    expect(screen.getByRole('heading', { name: 'Кто этот агент?' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Quick' })).toHaveAttribute('aria-pressed', 'true')

    fireEvent.click(screen.getByRole('button', { name: /Продолжить/ }))

    expect(screen.getByRole('heading', { name: 'Выберите отправную точку' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: /Заботливая спутница/ })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: /Застенчивая аналитик/ })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: /Острая цундере/ })).toBeInTheDocument()
  })

  it('exposes the full trait and emotional-dynamics vocabulary in Advanced mode', () => {
    render(<ProfileHarness />)

    fireEvent.click(screen.getByRole('button', { name: 'Advanced' }))
    fireEvent.click(screen.getByRole('button', { name: /Характер/ }))

    const traits = [
      'Теплота', 'Прямота', 'Эмоциональность', 'Игривость', 'Ревнивость', 'Раздражительность',
      'Эмпатия', 'Общительность', 'Стеснительность', 'Тревожность', 'Пугливость', 'Эмоциональная устойчивость',
      'Чувствительность', 'Собственнические чувства', 'Романтичность', 'Инициативность', 'Импульсивность',
      'Упрямство', 'Оптимизм', 'Любопытство', 'Подозрительность', 'Доверчивость', 'Привязанность', 'Формальность', 'Цундере',
    ]
    for (const trait of traits) expect(screen.getByRole('slider', { name: trait })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /Эмоции/ }))
    expect(screen.getByRole('heading', { name: 'Как возникают и проходят чувства?' })).toBeInTheDocument()
    expect(screen.getByRole('slider', { name: 'Реактивность' })).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: /Триггеры: Страх/ })).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: /Триггеры: Смущение/ })).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: 'Стиль конфликта' })).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: /Фоновая рефлексия/ })).toBeChecked()
    expect(screen.getByRole('spinbutton', { name: /Cooldown устойчивых изменений/ })).toHaveValue(60)
  })

  it('keeps free-form and structured backstory in one controlled draft', () => {
    render(<ProfileHarness />)
    fireEvent.click(screen.getByRole('button', { name: /Backstory/ }))

    const narrative = 'Она выросла у моря и любит старые карты.'
    fireEvent.change(screen.getByRole('textbox', { name: /Свободная предыстория/ }), { target: { value: narrative } })
    fireEvent.click(screen.getByRole('button', { name: /Добавить эпизод/ }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Что произошло' }), { target: { value: 'Нашла карту в старом маяке.' } })
    fireEvent.click(screen.getByRole('button', { name: /Review/ }))

    expect(screen.getByText('1 эпизодов')).toBeInTheDocument()
    expect(screen.getByText(narrative)).toBeInTheDocument()
  })

  it('submits only from Review and saves the current draft locally', () => {
    const onSubmit = vi.fn()
    render(<ProfileHarness onSubmit={onSubmit} />)

    fireEvent.change(screen.getByRole('textbox', { name: 'Имя агента' }), { target: { value: 'Emilu' } })
    fireEvent.click(screen.getByRole('button', { name: /Review/ }))

    expect(screen.getByText(/Emilu · 21 · female/)).toBeInTheDocument()
    expect(JSON.parse(window.localStorage.getItem('yuri.agent-profile-draft.v2') ?? '{}')).toMatchObject({ name: 'Emilu' })
    fireEvent.click(screen.getByRole('button', { name: /Создать агента/ }))
    expect(onSubmit).toHaveBeenCalledOnce()
  })

  it('supports native keyboard navigation through the accessible step controls', async () => {
    const user = userEvent.setup()
    render(<ProfileHarness />)

    const advanced = screen.getByRole('button', { name: 'Advanced' })
    advanced.focus()
    await user.keyboard('{Enter}')
    const relationship = screen.getByRole('button', { name: /Отношения/ })
    relationship.focus()
    await user.keyboard('{Enter}')

    expect(screen.getByRole('heading', { name: 'С какой истории начинаются отношения?' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Близкие друзья/ })).toHaveAttribute('aria-pressed')
  })
})
