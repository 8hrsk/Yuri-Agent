// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import type { RunTrace, ToolCall } from '../lib/contracts'
import { ExecutionTrace } from './ChatTimeline'

/**
 * A tool call whose arguments report every read of their payload.
 *
 * `JSON.stringify` walks enumerable getters, so the spy fires exactly when the
 * card serializes the arguments — and stays silent while nothing does. That is
 * the property H-18/M-40 are about: a collapsed trace must not pay for the
 * pretty-printed JSON of a file write it is not showing. A render-count or a
 * timing assertion would not notice a regression to eager serialization; this
 * does.
 */
function spyingToolCall(): { toolCall: ToolCall; serialized: ReturnType<typeof vi.fn> } {
  const serialized = vi.fn()
  const args: Record<string, unknown> = {
    path: '/Users/dev/notes.md',
    get contents() {
      serialized()
      return 'полное содержимое файла'
    },
  }
  return {
    serialized,
    toolCall: {
      id: 'call-1',
      name: 'filesystem.write',
      label: 'Записать файл',
      risk: 'high',
      status: 'completed',
      args,
      result: 'Файл записан',
    },
  }
}

function traceWith(toolCall: ToolCall): RunTrace {
  return {
    id: 'trace-1',
    runId: 'run-1',
    status: 'complete',
    startedAt: '2026-08-29T10:00:00.000Z',
    steps: [
      { id: 'step-1', kind: 'thinking', status: 'completed', createdAt: '2026-08-29T10:00:00.000Z', label: 'Планирую' },
      { id: 'step-2', kind: 'tool', status: 'completed', createdAt: '2026-08-29T10:00:01.000Z', toolCall },
    ],
  }
}

describe('a collapsed execution trace renders nothing (H-18/M-40)', () => {
  it('identifies a nested anonymous run as a subagent', () => {
    const { toolCall } = spyingToolCall()
    render(<ExecutionTrace trace={{ ...traceWith(toolCall), kind: 'subagent', parentRunId: 'run-parent' }} />)

    expect(screen.getByText('Субагент')).toBeInTheDocument()
    expect(document.querySelector('.run-trace__body')).toBeNull()
  })

  it.each([
    ['web.fetch', 'Чтение веб-страницы'],
    ['agent.delegate', 'Субагент'],
  ])('names the %s call while the trace remains collapsed', (name, label) => {
    const { toolCall } = spyingToolCall()
    render(<ExecutionTrace trace={traceWith({ ...toolCall, name, label })} />)

    expect(screen.getByText(label)).toBeInTheDocument()
    expect(document.querySelector('.run-trace__body')).toBeNull()
  })

  it('does not serialize tool arguments until the block is opened', async () => {
    const user = userEvent.setup()
    const { serialized, toolCall } = spyingToolCall()
    render(<ExecutionTrace trace={traceWith(toolCall)} />)

    // Closed: the summary is on screen, the body is not in the DOM at all, and
    // nothing has touched the arguments.
    expect(screen.getByText('Выполнение')).toBeInTheDocument()
    expect(document.querySelector('.run-trace__body')).toBeNull()
    expect(document.querySelector('.tool-card')).toBeNull()
    expect(serialized).not.toHaveBeenCalled()

    await user.click(screen.getByText('Выполнение'))

    // Opened: the steps appear and the arguments are shown in full, exactly
    // once — the memo keeps a re-render from serializing them again.
    expect(document.querySelector('.run-trace__body')).not.toBeNull()
    expect(screen.getByText('Планирую')).toBeInTheDocument()
    expect(screen.getAllByText('Записать файл')).toHaveLength(2)
    const payload = document.querySelector('.tool-card__payload code')
    expect(payload?.textContent).toBe(JSON.stringify({ path: '/Users/dev/notes.md', contents: 'полное содержимое файла' }, null, 2))
    expect(serialized).toHaveBeenCalledTimes(1)
    expect(document.querySelector('details')).toHaveAttribute('open')
  })

  it('does not serialize the same arguments twice when an open card re-renders (M-40)', async () => {
    const user = userEvent.setup()
    const { serialized, toolCall } = spyingToolCall()
    const trace = traceWith(toolCall)
    const { rerender } = render(<ExecutionTrace trace={trace} />)

    await user.click(screen.getByText('Выполнение'))
    expect(serialized).toHaveBeenCalledTimes(1)

    // Every run event rebuilds the trace and its steps around the *same*
    // arguments object, so an open card re-renders many times over one payload.
    // Serializing in the render body made each of those re-renders pay for the
    // whole JSON again.
    rerender(<ExecutionTrace trace={{
      ...trace,
      updatedAt: '2026-08-29T10:00:05.000Z',
      steps: trace.steps.map((step) => step.kind === 'tool' ? { ...step, toolCall: { ...step.toolCall } } : { ...step }),
    }} />)

    expect(screen.getAllByText('Записать файл')).toHaveLength(2)
    expect(serialized).toHaveBeenCalledTimes(1)
  })

  it('takes the body back out of the DOM when the block is closed again', async () => {
    const user = userEvent.setup()
    const { toolCall } = spyingToolCall()
    render(<ExecutionTrace trace={traceWith(toolCall)} />)

    await user.click(screen.getByText('Выполнение'))
    expect(screen.getAllByText('Записать файл')).toHaveLength(2)

    await user.click(screen.getByText('Выполнение'))
    expect(screen.getByText('Записать файл')).toBeInTheDocument()
    expect(document.querySelector('.run-trace__body')).toBeNull()
    expect(document.querySelector('details')).not.toHaveAttribute('open')
  })
})
