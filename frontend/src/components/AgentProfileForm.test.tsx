// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { useState } from 'react'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { cloneAgentDraft, defaultAgentDraft } from '../lib/agents'
import { resetYuriClientForTests } from '../lib/client'
import type { AgentProfileInput } from '../lib/contracts'
import { AgentProfileForm } from './AgentProfileForm'

function ProfileHarness({ initial = defaultAgentDraft, onSubmit = vi.fn() }: { initial?: AgentProfileInput; onSubmit?: () => void }) {
  const [value, setValue] = useState(() => cloneAgentDraft(initial))
  return <AgentProfileForm onChange={setValue} onSubmit={onSubmit} value={value} />
}

describe('AgentProfileForm', () => {
  beforeEach(() => {
    window.localStorage.clear()
    delete (window as typeof window & { go?: unknown }).go
    resetYuriClientForTests()
  })

  it('offers a short Quick flow with visible owner-controlled presets', () => {
    render(<ProfileHarness />)

    expect(screen.getByRole('heading', { name: 'Кто этот агент?' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Quick' })).toHaveAttribute('aria-pressed', 'true')

    fireEvent.click(screen.getByRole('button', { name: /Продолжить/ }))

    expect(screen.getByRole('heading', { name: 'Какой моделью думает агент?' })).toBeInTheDocument()
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
    expect(screen.getByText(/заминки, самоисправления, многоточия/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /Эмоции/ }))
    expect(screen.getByRole('heading', { name: 'Как возникают и проходят чувства?' })).toBeInTheDocument()
    expect(screen.getByRole('slider', { name: 'Реактивность' })).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: /Триггеры: Страх/ })).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: /Триггеры: Смущение/ })).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: 'Стиль конфликта' })).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: /Фоновая рефлексия/ })).toBeChecked()
    expect(screen.getByRole('spinbutton', { name: /Cooldown устойчивых изменений/ })).toHaveValue(60)
  })

  it('explains that explicit speech habits have roleplay priority', () => {
    render(<ProfileHarness />)
    fireEvent.click(screen.getByRole('button', { name: /Продолжить/ }))
    fireEvent.click(screen.getByRole('button', { name: /Продолжить/ }))

    expect(screen.getByText(/заикание, паузы или характерные обращения/)).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: /Короткое описание/ })).toHaveAttribute('aria-describedby', 'agent-preferences-hint')
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

  it('previews the production personality contract and supports an A/B comparison', async () => {
    const calls: unknown[] = []
    ;(window as typeof window & { go?: unknown }).go = { main: { Bridge: {
      ListConversations: () => [],
      PreviewAgentPersonality: (input: unknown) => {
        calls.push(input)
        const slot = calls.length === 1 ? 'A' : 'B'
        return {
          scenario: 'introduction', scenarioTitle: 'Обычное знакомство', prompt: 'Привет!',
          response: `Ответ ${slot}`, model: 'test-model', compilerCharacters: 1200,
          influences: [{ layer: 'temperament', key: 'shyness', value: 1, direction: 'high' }],
        }
      },
    } } }
    resetYuriClientForTests()
    render(<ProfileHarness />)
    fireEvent.click(screen.getByRole('button', { name: /Review/ }))

    fireEvent.click(screen.getByRole('button', { name: 'Сгенерировать вариант A' }))
    expect(await screen.findByText('Ответ A')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Сравнить с вариантом B' }))
    expect(await screen.findByText('Ответ B')).toBeInTheDocument()
    fireEvent.click(screen.getAllByText(/Что повлияло/)[0])

    expect(screen.getAllByText('shyness')).toHaveLength(2)
    expect(calls).toHaveLength(2)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Обновить вариант B' })).toBeEnabled())
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
