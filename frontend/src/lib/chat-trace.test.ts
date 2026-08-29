import { describe, expect, it } from 'vitest'

import {
  aggregateChatEvent,
  normalizeRunTrace,
  splitRunTraceForTimeline,
  sortRunTraces,
} from './chat-trace'
import type { RunTrace } from './contracts'

describe('chat execution trace', () => {
  it('aggregates thinking, tools, approval, and completion into one timeline', () => {
    const runId = 'run-trace-1'
    const toolCall = {
      id: 'call-1',
      name: 'filesystem.read',
      label: 'Чтение файлов',
      risk: 'low' as const,
      status: 'running' as const,
      args: { path: '/allowed/note.txt' },
    }
    const approval = {
      id: 'approval-1',
      toolCallId: 'call-1',
      title: 'Разрешить действие?',
      explanation: 'Нужно подтверждение.',
      risk: 'medium' as const,
      scope: '/allowed/note.txt',
    }
    let traces = aggregateChatEvent([], { type: 'run.started', runId, createdAt: '2026-08-29T10:00:00Z' })
    traces = aggregateChatEvent(traces, { type: 'run.status', runId, status: 'thinking', label: 'hidden chain of thought', createdAt: '2026-08-29T10:00:01Z' })
    traces = aggregateChatEvent(traces, { type: 'tool.started', runId, toolCall, createdAt: '2026-08-29T10:00:02Z' })
    traces = aggregateChatEvent(traces, { type: 'approval.required', runId, approval, createdAt: '2026-08-29T10:00:03Z' })
    traces = aggregateChatEvent(traces, {
      type: 'tool.updated', runId,
      toolCall: { ...toolCall, status: 'completed', result: 'прочитано', finishedAt: '2026-08-29T10:00:04Z' },
      createdAt: '2026-08-29T10:00:04Z',
    })
    traces = aggregateChatEvent(traces, { type: 'run.completed', runId, status: 'complete', createdAt: '2026-08-29T10:00:05Z' })

    expect(traces).toHaveLength(1)
    expect(traces[0]).toMatchObject({ runId, status: 'complete', startedAt: '2026-08-29T10:00:00Z' })
    expect(traces[0]?.steps.map((step) => step.kind)).toEqual(['thinking', 'tool', 'approval', 'completion'])
    expect(traces[0]?.steps.find((step) => step.kind === 'thinking')).toMatchObject({ label: 'Обработка завершена', status: 'completed' })
    expect(traces[0]?.steps.find((step) => step.kind === 'approval')).toMatchObject({ status: 'approved' })
    expect(JSON.stringify(traces)).not.toContain('hidden chain of thought')
    expect(traces[0]?.steps.find((step) => step.kind === 'tool')).toMatchObject({ toolCall: { result: 'прочитано' } })
  })

  it('expands compact persisted traces with toolCalls into safe renderer steps', () => {
    const trace = normalizeRunTrace({
      id: 'run-history-1',
      kind: 'interactive',
      status: 'completed',
      createdAt: '2026-08-29T09:00:00Z',
      startedAt: '2026-08-29T09:00:01Z',
      finishedAt: '2026-08-29T09:00:04Z',
      failure: '',
      toolCalls: [{
        id: 'call-history-1',
        name: 'filesystem.read',
        risk: 'low',
        status: 'completed',
        args: { path: '/allowed/history.txt' },
        result: 'blob:result',
        startedAt: '2026-08-29T09:00:02Z',
        finishedAt: '2026-08-29T09:00:03Z',
      }],
      reasoning: 'must never be rendered',
    })

    expect(trace).toBeDefined()
    expect(trace?.steps.map((step) => step.kind)).toEqual(['thinking', 'tool', 'completion'])
    expect(trace?.steps.find((step) => step.kind === 'tool')).toMatchObject({ toolCall: { name: 'filesystem.read', result: 'blob:result' } })
    expect(JSON.stringify(trace)).not.toContain('must never be rendered')
  })

  it('sorts historical traces chronologically while preserving equal-time order', () => {
    const makeTrace = (id: string, startedAt: string): RunTrace => ({ id, runId: id, status: 'complete', startedAt, steps: [] })
    const sorted = sortRunTraces([
      makeTrace('later', '2026-08-29T10:00:02Z'),
      makeTrace('same-second-b', '2026-08-29T10:00:01Z'),
      makeTrace('same-second-a', '2026-08-29T10:00:01Z'),
      makeTrace('earlier', '2026-08-29T10:00:00Z'),
    ])
    expect(sorted.map((trace) => trace.id)).toEqual(['earlier', 'same-second-b', 'same-second-a', 'later'])
  })

  it('splits a run into one thinking block and one block per tool call', () => {
    const trace = normalizeRunTrace({
      id: 'trace-1', runId: 'run-1', status: 'completed',
      startedAt: '2026-08-29T03:00:00Z', finishedAt: '2026-08-29T03:00:05Z',
      toolCalls: [
        { id: 'call-1', name: 'filesystem.read', status: 'completed', startedAt: '2026-08-29T03:00:02Z', finishedAt: '2026-08-29T03:00:03Z' },
        { id: 'call-2', name: 'filesystem.read', status: 'completed', startedAt: '2026-08-29T03:00:04Z', finishedAt: '2026-08-29T03:00:05Z' },
      ],
    })
    const fragments = splitRunTraceForTimeline(trace!)
    expect(fragments).toHaveLength(3)
    expect(fragments.map((fragment) => fragment.steps.some((step) => step.kind === 'tool'))).toEqual([false, true, true])
    expect(fragments.map((fragment) => fragment.startedAt)).toEqual([
      '2026-08-29T03:00:00Z', '2026-08-29T03:00:02Z', '2026-08-29T03:00:04Z',
    ])
  })
})
