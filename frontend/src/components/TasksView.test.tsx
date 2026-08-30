// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import type { JobRun, Schedule, YuriClient } from '../lib/contracts'
import { TasksView } from './TasksView'

let clientStub: YuriClient

vi.mock('../lib/client', () => ({
  createYuriClient: () => clientStub,
}))

const schedule: Schedule = {
  id: 'sched-1',
  title: 'Утренний отчёт',
  prompt: 'Собери сводку',
  type: 'cron',
  expression: '0 9 * * *',
  timezone: 'Europe/Moscow',
  misfirePolicy: 'skip',
  enabled: true,
  status: 'active',
  deliveryChannel: 'in_app',
  nextRunAt: '2026-08-30T06:00:00.000Z',
}

function jobRun(index: number): JobRun {
  return {
    id: `run-${index}`,
    scheduleId: 'sched-1',
    status: 'completed',
    attempt: 1,
    startedAt: new Date(Date.UTC(2026, 7, 29, 6, index)).toISOString(),
    finishedAt: new Date(Date.UTC(2026, 7, 29, 6, index, 30)).toISOString(),
    durationMs: 30_000,
    triggeredBy: 'schedule',
  }
}

function mountTasks(runCount: number) {
  clientStub = {
    mode: 'mock',
    listSchedules: async () => [schedule],
    listJobRuns: async () => Array.from({ length: runCount }, (_, index) => jobRun(index)),
  } as unknown as YuriClient
  return { user: userEvent.setup(), ...render(<TasksView />) }
}

describe('a collapsed run log stays out of the DOM (H-18)', () => {
  it('renders the rows only once the history is opened', async () => {
    const { user } = mountTasks(100)
    await screen.findByText('Утренний отчёт')

    // Closed: the count is on the summary, but none of the 100 rows exist.
    const summary = screen.getByText(/История запусков/)
    expect(summary).toHaveTextContent('100')
    expect(document.querySelectorAll('.task-history__row')).toHaveLength(0)
    expect(screen.queryByRole('list', { name: 'История запусков' })).not.toBeInTheDocument()

    await user.click(summary)

    await waitFor(() => expect(document.querySelectorAll('.task-history__row')).toHaveLength(100))
    expect(screen.getByRole('list', { name: 'История запусков' })).toBeInTheDocument()

    await user.click(summary)
    expect(document.querySelectorAll('.task-history__row')).toHaveLength(0)
  })
})
