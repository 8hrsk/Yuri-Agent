import { describe, expect, it } from 'vitest'

import {
  aggregateChatEvent,
  buildChatTimeline,
  mergeStreamingMessages,
  normalizeApproval,
  normalizeRunTrace,
  splitRunTraceForTimeline,
  sortRunTraces,
} from './chat-trace'
import type { ChatEvent, ChatMessage, RunTrace } from './contracts'

describe('chat execution trace', () => {
  it('keeps filesystem access scope and persistence choices at the renderer boundary', () => {
    expect(normalizeApproval({
      id: 'approval-fs', toolCallId: 'call-fs', risk: 'low',
      kind: 'filesystem_access', path: '/Users/owner/note.txt',
      permissionRoot: '/Users/owner', canRemember: true,
    })).toMatchObject({
      kind: 'filesystem_access', path: '/Users/owner/note.txt',
      permissionRoot: '/Users/owner', canRemember: true,
    })
  })

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

  it('restores an explicit provider fallback from compact persisted history', () => {
    const trace = normalizeRunTrace({
      id: 'run-history-fallback',
      status: 'completed',
      startedAt: '2026-09-01T09:00:00Z',
      finishedAt: '2026-09-01T09:00:03Z',
      providerId: 'fallback-provider',
      model: 'fallback-model',
      fallback: {
        fromProviderId: 'primary-provider',
        fromModel: 'primary-model',
        toProviderId: 'fallback-provider',
        toModel: 'fallback-model',
        reason: 'Основной маршрут завершился provider-ошибкой',
        createdAt: '2026-09-01T09:00:01Z',
        upstreamError: 'secret provider body',
      },
    })

    expect(trace?.steps.map((step) => step.kind)).toEqual(['thinking', 'fallback', 'completion'])
    expect(trace?.steps.find((step) => step.kind === 'fallback')).toMatchObject({
      fromProviderId: 'primary-provider',
      toProviderId: 'fallback-provider',
      reason: 'Основной маршрут завершился provider-ошибкой',
    })
    expect(JSON.stringify(trace)).not.toContain('secret provider body')
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

  it('retains anonymous child provenance for live and persisted traces', () => {
    const live = aggregateChatEvent([], {
      type: 'run.started',
      runId: 'run-child',
      runKind: 'subagent',
      parentRunId: 'run-parent',
      conversationId: 'conversation-1',
      createdAt: '2026-08-29T10:00:00Z',
    })
    expect(live[0]).toMatchObject({ kind: 'subagent', parentRunId: 'run-parent' })

    const persisted = normalizeRunTrace({
      id: 'run-child',
      kind: 'subagent',
      parentRunId: 'run-parent',
      status: 'completed',
      createdAt: '2026-08-29T10:00:00Z',
      toolCalls: [],
    })
    expect(persisted).toMatchObject({ kind: 'subagent', parentRunId: 'run-parent' })
  })

  it('keeps immutable provider/model attribution and terminal token usage', () => {
    let live = aggregateChatEvent([], {
      type: 'run.started', runId: 'run-route', providerId: 'openrouter', model: 'model/free',
      createdAt: '2026-09-01T10:00:00Z',
    })
    live = aggregateChatEvent(live, {
      type: 'run.completed', runId: 'run-route', status: 'complete', providerId: 'openrouter', model: 'model/free',
      inputTokens: 120, outputTokens: 30, totalTokens: 150, createdAt: '2026-09-01T10:00:01Z',
    })
    expect(live[0]).toMatchObject({ providerId: 'openrouter', model: 'model/free', inputTokens: 120, outputTokens: 30, totalTokens: 150 })

    const persisted = normalizeRunTrace({
      id: 'run-history-route', status: 'completed', createdAt: '2026-09-01T09:00:00Z',
      providerId: 'codex', model: 'gpt-5.6', inputTokens: 80, outputTokens: 20, totalTokens: 100,
    })
    expect(persisted).toMatchObject({ providerId: 'codex', model: 'gpt-5.6', inputTokens: 80, outputTokens: 20, totalTokens: 100 })
  })

  it('keeps provider-neutral failure metadata for live and persisted traces', () => {
    let live = aggregateChatEvent([], {
      type: 'run.started', runId: 'run-failure', createdAt: '2026-09-01T10:00:00Z',
    })
    live = aggregateChatEvent(live, {
      type: 'run.completed', runId: 'run-failure', status: 'error', error: 'Провайдер ограничил частоту запросов',
      failureKind: 'rate_limit', retryable: true, retryAfterSeconds: 17, createdAt: '2026-09-01T10:00:01Z',
    })
    expect(live[0]).toMatchObject({
      status: 'error', failureKind: 'rate_limit', retryable: true, retryAfterSeconds: 17,
    })

    const persisted = normalizeRunTrace({
      id: 'run-history-failure', status: 'failed', failure: 'Лимит исчерпан', failureKind: 'quota_exhausted',
      retryable: false, retryAfterSeconds: 0, createdAt: '2026-09-01T09:00:00Z',
    })
    expect(persisted).toMatchObject({ status: 'error', failureKind: 'quota_exhausted', retryable: false })
  })

  it('records a route fallback as a visible, secret-free trace step', () => {
    let traces = aggregateChatEvent([], {
      type: 'run.started', runId: 'run-fallback', providerId: 'primary', model: 'model/main',
      createdAt: '2026-09-01T10:00:00Z',
    })
    traces = aggregateChatEvent(traces, {
      type: 'run.fallback', runId: 'run-fallback',
      fromProviderId: 'primary', fromModel: 'model/main',
      toProviderId: 'backup', toModel: 'model/reserve',
      reason: 'Основной маршрут временно недоступен',
      // An untrusted bridge payload must not be copied into the public trace.
      ...( { apiKey: 'sk-test-secret', credentials: { token: 'secret' } } as Record<string, unknown>),
      createdAt: '2026-09-01T10:00:01Z',
    } as ChatEvent)

    const trace = traces[0]!
    expect(trace.steps.map((step) => step.kind)).toEqual(['thinking', 'fallback'])
    expect(trace.steps.find((step) => step.kind === 'fallback')).toMatchObject({
      kind: 'fallback', status: 'completed', label: 'Переключение маршрута',
      fromProviderId: 'primary', fromModel: 'model/main',
      toProviderId: 'backup', toModel: 'model/reserve',
      reason: 'Основной маршрут временно недоступен',
    })
    expect(JSON.stringify(trace)).not.toContain('sk-test-secret')
    expect(JSON.stringify(trace)).not.toContain('credentials')

    const fallbackFragment = splitRunTraceForTimeline(trace).find((fragment) => fragment.steps[0]?.kind === 'fallback')
    expect(fallbackFragment).toMatchObject({
      id: 'trace:run-fallback:fallback:run-fallback:fallback:1',
      steps: [{ kind: 'fallback', label: 'Переключение маршрута' }],
    })
  })

  it('normalizes persisted fallback steps without retaining unknown payload fields', () => {
    const trace = normalizeRunTrace({
      id: 'trace-persisted-fallback', runId: 'run-persisted-fallback', status: 'completed',
      startedAt: '2026-09-01T09:00:00Z',
      steps: [{
        type: 'run.fallback', createdAt: '2026-09-01T09:00:01Z',
        from_provider_id: 'primary', from_model: 'model/main',
        to_provider_id: 'backup', to_model: 'model/reserve',
        reason: 'Резервный маршрут выбран', apiKey: 'sk-persisted-secret',
      }],
    })

    expect(trace?.steps).toEqual([{
      id: 'run-persisted-fallback:step:0', kind: 'fallback', status: 'completed',
      label: 'Переключение маршрута',
      fromProviderId: 'primary', fromModel: 'model/main',
      toProviderId: 'backup', toModel: 'model/reserve',
      reason: 'Резервный маршрут выбран',
      createdAt: '2026-09-01T09:00:01Z', finishedAt: '2026-09-01T09:00:01Z',
    }])
    expect(JSON.stringify(trace)).not.toContain('sk-persisted-secret')
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

function message(id: string, role: ChatMessage['role'], createdAt: string, status: ChatMessage['status'] = 'complete'): ChatMessage {
  return { id, role, content: id, status, createdAt }
}

describe('chat timeline assembly', () => {
  const history: ChatMessage[] = [
    message('user-1', 'user', '2026-08-29T10:00:00Z'),
    message('assistant-1', 'assistant', '2026-08-29T10:00:05Z'),
    message('user-2', 'user', '2026-08-29T10:00:06Z'),
    // A tool message never becomes its own row; it is shown inside the trace.
    message('tool-1', 'tool', '2026-08-29T10:00:07Z'),
  ]

  function traceFor(runId: string, startedAt: string, toolAt = '2026-08-29T10:00:08Z'): RunTrace {
    return normalizeRunTrace({
      id: `trace-${runId}`, runId, status: 'completed',
      startedAt, finishedAt: toolAt,
      toolCalls: [{ id: `call-${runId}`, name: 'filesystem.read', status: 'completed', startedAt: toolAt, finishedAt: toolAt }],
    })!
  }

  it('orders messages and trace blocks chronologically and drops tool messages', () => {
    const timeline = buildChatTimeline(history, [traceFor('run-1', '2026-08-29T10:00:01Z')])
    expect(timeline.map((entry) => entry.key)).toEqual([
      'user-1',
      'trace-trace-run-1:thinking',
      'assistant-1',
      'user-2',
      'trace-trace-run-1:tool:call-run-1',
    ])
    const traceEntries = timeline.filter((entry) => entry.kind === 'trace')
    expect(traceEntries[0]).not.toHaveProperty('showRecovery')
    expect(traceEntries[1]).toMatchObject({ showRecovery: true })
    expect(traceEntries[1]).not.toHaveProperty('recoveryMessageId', 'user-1')
  })

  it('anchors retry only to the latest user turn, never to an older branch', () => {
    const latestTrace = traceFor('run-latest', '2026-08-29T10:00:07Z', '2026-08-29T10:00:08Z')
    const timeline = buildChatTimeline(history, [latestTrace])
    const recovery = timeline.filter((entry) => entry.kind === 'trace' && entry.showRecovery).at(-1)
    expect(recovery).toMatchObject({ recoveryMessageId: 'user-2' })
  })

  it('places a trace between the question and the answer that share its timestamp', () => {
    const sameSecond = [
      message('user-3', 'user', '2026-08-29T11:00:00Z'),
      message('assistant-3', 'assistant', '2026-08-29T11:00:00Z'),
    ]
    const timeline = buildChatTimeline(sameSecond, [traceFor('run-2', '2026-08-29T11:00:00Z', '2026-08-29T11:00:02Z')])
    expect(timeline.map((entry) => entry.key)).toEqual([
      'user-3',
      'trace-trace-run-2:thinking',
      'assistant-3',
      'trace-trace-run-2:tool:call-run-2',
    ])
  })

  it('merges a streaming answer exactly where a full rebuild would place it', () => {
    const traces = [traceFor('run-3', '2026-08-29T10:00:01Z')]
    const streaming = [message('assistant-live', 'assistant', '2026-08-29T10:00:06Z', 'streaming')]

    const merged = mergeStreamingMessages(buildChatTimeline(history, traces), streaming)
    const rebuilt = buildChatTimeline([...history, ...streaming], traces)

    expect(merged.map((entry) => entry.key)).toEqual(rebuilt.map((entry) => entry.key))
  })

  it('merges a late-arriving streaming answer after everything already shown', () => {
    const traces = [traceFor('run-4', '2026-08-29T10:00:01Z')]
    const streaming = [message('assistant-live', 'assistant', '2026-08-29T12:00:00Z', 'streaming')]
    const merged = mergeStreamingMessages(buildChatTimeline(history, traces), streaming)
    expect(merged.at(-1)?.key).toBe('assistant-live')
    expect(merged).toHaveLength(buildChatTimeline(history, traces).length + 1)
  })

  it('returns the very same timeline when nothing is streaming', () => {
    const base = buildChatTimeline(history, [])
    expect(mergeStreamingMessages(base, [])).toBe(base)
  })

  it('keeps untouched traces and their split blocks referentially stable', () => {
    const first = aggregateChatEvent([], { type: 'run.started', runId: 'run-a', createdAt: '2026-08-29T10:00:00Z' })
    const withSecond = aggregateChatEvent(first, { type: 'run.started', runId: 'run-b', createdAt: '2026-08-29T10:00:10Z' })
    const fragmentsBefore = splitRunTraceForTimeline(withSecond[0]!)

    const afterEvent = aggregateChatEvent(withSecond, { type: 'run.completed', runId: 'run-b', status: 'complete', createdAt: '2026-08-29T10:00:11Z' })

    // Only the trace the event belongs to may be replaced, and the fragments of
    // the other one must survive identity comparison so `React.memo` can skip
    // re-rendering it.
    expect(afterEvent[0]).toBe(withSecond[0])
    expect(afterEvent[1]).not.toBe(withSecond[1])
    expect(splitRunTraceForTimeline(afterEvent[0]!)).toBe(fragmentsBefore)
  })
})

/**
 * Golden pin for M-43.
 *
 * The aggregation was optimized (memoized time keys, ordered fast paths, one
 * normalization pass instead of a full `cloneStep` map per event). The whole
 * point of that work is that it is *output-identical*, so the exact tree — step
 * order, ids, derived statuses, derived labels, and the timestamps carried onto
 * each step — is pinned here rather than left to be re-derived by whatever the
 * implementation happens to do next. A faster reduction that changes any of
 * this is a regression, not an optimization.
 */
describe('trace reduction golden shape (M-43)', () => {
  const events = [
    { type: 'run.started', runId: 'run-1', createdAt: '2026-08-29T10:00:00.000Z' },
    { type: 'run.status', runId: 'run-1', status: 'thinking', createdAt: '2026-08-29T10:00:01.000Z' },
    {
      type: 'tool.started',
      runId: 'run-1',
      createdAt: '2026-08-29T10:00:02.000Z',
      toolCall: { id: 'call-1', name: 'fs_write', status: 'running', args: { path: '/tmp/a.txt' }, startedAt: '2026-08-29T10:00:02.000Z' },
    },
    {
      type: 'approval.required',
      runId: 'run-1',
      createdAt: '2026-08-29T10:00:03.000Z',
      approval: { id: 'appr-1', toolCallId: 'call-1', title: 'Записать файл', explanation: 'детали', risk: 'high', scope: 'filesystem.write /tmp/a.txt' },
    },
    {
      type: 'tool.updated',
      runId: 'run-1',
      createdAt: '2026-08-29T10:00:04.000Z',
      toolCall: { id: 'call-1', name: 'fs_write', status: 'completed', args: { path: '/tmp/a.txt' }, result: 'ok', startedAt: '2026-08-29T10:00:02.000Z', finishedAt: '2026-08-29T10:00:04.000Z' },
    },
    { type: 'run.completed', runId: 'run-1', status: 'complete', createdAt: '2026-08-29T10:00:05.000Z' },
  ] as ChatEvent[]

  function reduceAll(): RunTrace[] {
    let traces: RunTrace[] = []
    for (const event of events) traces = aggregateChatEvent(traces, event)
    return traces
  }

  it('reduces a tool run with approval into exactly this tree', () => {
    const traces = reduceAll()
    expect(traces).toHaveLength(1)
    const trace = traces[0]!
    expect({ runId: trace.runId, status: trace.status, startedAt: trace.startedAt, updatedAt: trace.updatedAt, finishedAt: trace.finishedAt }).toEqual({
      runId: 'run-1',
      status: 'complete',
      startedAt: '2026-08-29T10:00:00.000Z',
      updatedAt: '2026-08-29T10:00:05.000Z',
      finishedAt: '2026-08-29T10:00:05.000Z',
    })
    expect(trace.steps.map((step) => ({
      id: step.id,
      kind: step.kind,
      status: step.status,
      label: (step as { label?: string }).label,
      createdAt: step.createdAt,
      finishedAt: step.finishedAt,
    }))).toEqual([
      // Thinking closes when the first tool starts, not when the run ends.
      { id: 'run-1:thinking', kind: 'thinking', status: 'completed', label: 'Обработка завершена', createdAt: '2026-08-29T10:00:00.000Z', finishedAt: '2026-08-29T10:00:02.000Z' },
      // The tool keeps its original createdAt across the update.
      { id: 'run-1:tool:call-1', kind: 'tool', status: 'completed', label: undefined, createdAt: '2026-08-29T10:00:02.000Z', finishedAt: '2026-08-29T10:00:04.000Z' },
      // A completed tool call resolves its approval to `approved`.
      { id: 'run-1:approval:appr-1', kind: 'approval', status: 'approved', label: undefined, createdAt: '2026-08-29T10:00:03.000Z', finishedAt: '2026-08-29T10:00:04.000Z' },
      { id: 'run-1:completion', kind: 'completion', status: 'complete', label: 'Запуск завершён', createdAt: '2026-08-29T10:00:05.000Z', finishedAt: '2026-08-29T10:00:05.000Z' },
    ])
  })

  it('carries the tool payload through unchanged', () => {
    const step = reduceAll()[0]!.steps.find((candidate) => candidate.kind === 'tool')
    expect(step?.kind).toBe('tool')
    if (step?.kind !== 'tool') return
    expect(step.toolCall).toEqual({
      id: 'call-1',
      name: 'fs_write',
      status: 'completed',
      args: { path: '/tmp/a.txt' },
      result: 'ok',
      startedAt: '2026-08-29T10:00:02.000Z',
      finishedAt: '2026-08-29T10:00:04.000Z',
    })
  })

  it('splits into exactly these rendered blocks', () => {
    const fragments = reduceAll().flatMap(splitRunTraceForTimeline)
    expect(fragments.map((fragment) => ({ id: fragment.id, steps: fragment.steps.map((step) => step.id) }))).toEqual([
      { id: 'trace:run-1:thinking', steps: ['run-1:thinking'] },
      // The tool block reopens a thinking step of its own before the call.
      { id: 'trace:run-1:tool:call-1', steps: ['run-1:thinking:call-1', 'run-1:tool:call-1', 'run-1:approval:appr-1'] },
    ])
  })

  it('never writes through the traces it was handed', () => {
    // Dropping the defensive `steps.map(cloneStep)` on every event is only safe
    // while nothing mutates a step in place. Freezing makes an in-place write
    // throw in strict mode rather than silently corrupting shared state.
    const deepFreeze = (value: unknown): void => {
      if (!value || typeof value !== 'object' || Object.isFrozen(value)) return
      Object.freeze(value)
      for (const nested of Object.values(value as Record<string, unknown>)) deepFreeze(nested)
    }

    let traces: RunTrace[] = []
    for (const event of events) {
      deepFreeze(traces)
      traces = aggregateChatEvent(traces, event)
    }

    // The frozen inputs still produced the golden tree.
    expect(traces[0]?.steps.map((step) => step.id)).toEqual([
      'run-1:thinking',
      'run-1:tool:call-1',
      'run-1:approval:appr-1',
      'run-1:completion',
    ])
  })
})
