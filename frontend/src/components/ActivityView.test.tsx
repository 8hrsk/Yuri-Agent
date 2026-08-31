// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import type { ProactivitySettings, YuriClient } from '../lib/contracts'
import { ActivityView } from './ActivityView'

const settings: ProactivitySettings = {
  enabled: false,
  quietHoursEnabled: true,
  quietHoursStart: '23:00',
  quietHoursEnd: '07:00',
  timezone: 'Europe/Moscow',
  dailyLimit: 5,
  cooldownMinutes: 30,
  allowLocalNotifications: false,
  autonomousPeerDialogues: false,
  autonomousPeerDailyLimit: 2,
  autonomousPeerCooldownMinutes: 120,
}

let clientStub: YuriClient

vi.mock('../lib/client', () => ({
  createYuriClient: () => clientStub,
  requestBrowserNotificationPermission: async () => 'default',
}))

describe('Activity autonomous peer policy', () => {
  it('keeps autonomous peer dialogue opt-in and persists its bounded limits', async () => {
    const save = vi.fn(async () => undefined)
    clientStub = {
      mode: 'mock',
      getProactivitySettings: async () => settings,
      saveProactivitySettings: save,
      listActivity: async () => [],
    } as unknown as YuriClient
    const user = userEvent.setup()
    render(<ActivityView />)

    const toggle = await screen.findByRole('switch', { name: /Автономные консультации агентов/ })
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-checked', 'true')

    const dailyLimit = screen.getByRole('spinbutton', { name: 'Peer-диалогов в день' })
    const cooldown = screen.getByRole('spinbutton', { name: 'Peer cooldown, минут' })
    fireEvent.change(dailyLimit, { target: { value: '3' } })
    fireEvent.change(cooldown, { target: { value: '90' } })
    await user.click(screen.getByRole('button', { name: 'Сохранить правила' }))

    await waitFor(() => expect(save).toHaveBeenCalledTimes(1))
    expect(save).toHaveBeenCalledWith(expect.objectContaining({
      autonomousPeerDialogues: true,
      autonomousPeerDailyLimit: 3,
      autonomousPeerCooldownMinutes: 90,
    }))
    expect(screen.getByText('Правила проактивности сохранены.')).toBeInTheDocument()
  })

  it('renders persisted personality revisions as layer-aware change cards', async () => {
    clientStub = {
      mode: 'mock',
      getProactivitySettings: async () => settings,
      saveProactivitySettings: async () => undefined,
      listActivity: async () => [{
        id: 'audit-persona-v2', type: 'reflection', status: 'info', title: 'Личность Yuri изменилась',
        detail: 'persona-yuri', source: 'system', createdAt: '2026-08-31T16:00:00Z',
        reason: 'Тёплый подтверждённый разговор', provenance: 'audit:audit-persona-v2',
        layer: 'mutable_persona', operation: 'update', version: 2, evidenceCount: 1,
        changes: [{ key: 'warmth', delta: .15 }, { key: 'irritability', delta: -.04 }],
      }],
    } as unknown as YuriClient
    render(<ActivityView />)

    expect(await screen.findByText('MUTABLE PERSONA')).toBeInTheDocument()
    expect(screen.getByText('Тёплый подтверждённый разговор')).toBeInTheDocument()
    expect(screen.getByText(/теплота/)).toHaveTextContent('+15%')
    expect(screen.getByText(/раздражительность/)).toHaveTextContent('-4%')
    expect(screen.getByText('evidence · 1')).toBeInTheDocument()
  })
})
