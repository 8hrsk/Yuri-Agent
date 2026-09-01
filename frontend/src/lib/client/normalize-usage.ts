import type { RunUsageStats, RunUsageStatsGroup, RunUsageStatsInput } from '../contracts'
import { optionalNumber, optionalString } from './primitives'
import type { UnknownRecord } from './primitives'

function nonNegativeInteger(value: unknown): number {
  const numeric = Number(value)
  return Number.isFinite(numeric) ? Math.max(0, Math.round(numeric)) : 0
}

function normalizeCountMap(value: unknown): Record<string, number> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  return Object.fromEntries(Object.entries(value as UnknownRecord).flatMap(([key, raw]) => {
    const name = key.trim()
    if (!name) return []
    return [[name, nonNegativeInteger(raw)]]
  }))
}

function normalizeRunUsageGroup(value: unknown): RunUsageStatsGroup | undefined {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
  const source = value as UnknownRecord
  const agentId = optionalString(source, 'agentId', 'agent_id') ?? 'unknown'
  return {
    agentId,
    agentName: optionalString(source, 'agentName', 'agent_name'),
    providerId: optionalString(source, 'providerId', 'provider_id'),
    model: optionalString(source, 'model'),
    runCount: nonNegativeInteger(optionalNumber(source, 'runCount', 'run_count')),
    statusCounts: normalizeCountMap(source.statusCounts ?? source.status_counts),
    failureKinds: normalizeCountMap(source.failureKinds ?? source.failure_kinds),
    inputTokens: nonNegativeInteger(optionalNumber(source, 'inputTokens', 'input_tokens')),
    outputTokens: nonNegativeInteger(optionalNumber(source, 'outputTokens', 'output_tokens')),
    totalTokens: nonNegativeInteger(optionalNumber(source, 'totalTokens', 'total_tokens')),
  }
}

function emptyRunUsageStats(input: RunUsageStatsInput = {}): RunUsageStats {
  return { from: input.from ?? '', to: input.to ?? '', groups: [] }
}

function normalizeRunUsageStats(value: unknown, fallback: RunUsageStatsInput = {}): RunUsageStats {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return emptyRunUsageStats(fallback)
  const source = value as UnknownRecord
  const nested = source.stats && typeof source.stats === 'object' && !Array.isArray(source.stats)
    ? source.stats as UnknownRecord
    : source
  const rawGroups = nested.groups ?? nested.items ?? nested.results
  const groups = Array.isArray(rawGroups)
    ? rawGroups.map(normalizeRunUsageGroup).filter((group): group is RunUsageStatsGroup => Boolean(group))
    : []
  return {
    from: optionalString(nested, 'from', 'fromTime', 'from_time') ?? fallback.from ?? '',
    to: optionalString(nested, 'to', 'toTime', 'to_time') ?? fallback.to ?? '',
    groups,
  }
}

export { emptyRunUsageStats, normalizeRunUsageStats }
