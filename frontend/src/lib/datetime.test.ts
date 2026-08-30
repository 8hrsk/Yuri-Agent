import { describe, expect, it } from 'vitest'

import { formatClock, formatDateTime } from './datetime'

const samples = [
  '2026-08-29T10:00:00.000Z',
  '2026-01-01T00:07:00.000Z',
  '2026-12-31T23:59:59.000Z',
  '2026-06-15T13:45:12.500Z',
]

describe('shared Intl formatters', () => {
  it('renders exactly what a per-call formatter rendered', () => {
    for (const sample of samples) {
      const date = new Date(sample)
      // The construction the views used to perform on every list item.
      expect(formatDateTime(date)).toBe(new Intl.DateTimeFormat('ru-RU', { dateStyle: 'medium', timeStyle: 'short' }).format(date))
      expect(formatClock(date)).toBe(new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit' }).format(date))
    }
  })

  it('stays stateless across repeated calls on the shared instances', () => {
    const date = new Date('2026-08-29T10:00:00.000Z')
    const first = `${formatDateTime(date)}|${formatClock(date)}`
    for (let index = 0; index < 5; index += 1) {
      formatDateTime(new Date('2026-02-02T02:02:00.000Z'))
      formatClock(new Date('2026-02-02T02:02:00.000Z'))
    }
    expect(`${formatDateTime(date)}|${formatClock(date)}`).toBe(first)
  })
})
