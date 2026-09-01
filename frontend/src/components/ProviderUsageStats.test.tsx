// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import type { RunUsageStats } from '../lib/contracts'
import { ProviderUsageStats } from './ProviderUsageStats'

const stats: RunUsageStats = {
  from: '2026-08-01T00:00:00Z',
  to: '2026-09-01T00:00:00Z',
  groups: [
    {
      agentId: 'agent-emily', agentName: 'Emily', providerId: 'openrouter', model: 'vendor/free', runCount: 3,
      statusCounts: { completed: 2, failed: 1 }, failureKinds: { provider: 1 }, inputTokens: 120, outputTokens: 80, totalTokens: 200,
    },
    {
      agentId: 'agent-yuri', agentName: 'Yuri', providerId: 'codex', model: 'gpt-5', runCount: 2,
      statusCounts: { completed: 2 }, failureKinds: {}, inputTokens: 100, outputTokens: 60, totalTokens: 160,
    },
  ],
}

describe('ProviderUsageStats', () => {
  it('shows totals and route rows without cost estimates', () => {
    render(<ProviderUsageStats loading={false} onDaysChange={vi.fn()} onRefresh={vi.fn()} stats={stats} windowDays={30} />)

    expect(screen.getByRole('heading', { name: 'Использование провайдеров' })).toBeInTheDocument()
    expect(screen.getByText('5')).toBeInTheDocument()
    expect(screen.getByText('360')).toBeInTheDocument()
    expect(screen.getAllByText('1')).toHaveLength(2)
    expect(screen.getByText('Emily')).toBeInTheDocument()
    expect(screen.getByText('openrouter')).toBeInTheDocument()
    expect(screen.getByText('vendor/free')).toBeInTheDocument()
    expect(screen.getByText(/Стоимость не показывается/)).toBeInTheDocument()
  })

  it('emits period, refresh and renders loading state', async () => {
    const user = userEvent.setup()
    const onDaysChange = vi.fn()
    const onRefresh = vi.fn()
    const { rerender } = render(<ProviderUsageStats loading onDaysChange={onDaysChange} onRefresh={onRefresh} stats={stats} windowDays={30} />)

    expect(screen.getByRole('status')).toHaveTextContent('Загружаю статистику')
    await user.click(screen.getByRole('button', { name: '7 дней' }))
    expect(onDaysChange).toHaveBeenCalledWith(7)
    rerender(<ProviderUsageStats loading={false} onDaysChange={onDaysChange} onRefresh={onRefresh} stats={stats} windowDays={7} />)
    await user.click(screen.getByRole('button', { name: 'Обновить статистику использования' }))
    expect(onRefresh).toHaveBeenCalledTimes(1)

    rerender(<ProviderUsageStats loading={false} onDaysChange={onDaysChange} onRefresh={onRefresh} stats={{ ...stats, groups: [] }} windowDays={7} />)
    expect(screen.getByText('За выбранный период запусков нет.')).toBeInTheDocument()
  })
})
