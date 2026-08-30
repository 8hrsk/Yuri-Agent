import type {
  ArchiveSearchResponse,
  ArchiveSearchResult,
  MemoryContentKind,
  MemoryKind,
  MemoryLifecycleState,
  MemoryRecord,
  MemorySource,
} from '../contracts'
import { clampUnit } from './primitives'
import type { UnknownRecord } from './primitives'

function normalizeMemoryKind(value: unknown): MemoryKind {
  const kind = String(value ?? '').toLowerCase()
  // `user` is accepted only as a decoder alias for early preview payloads;
  // the canonical wire/domain value is `user_model`.
  if (kind === 'user') return 'user_model'
  if (kind === 'core' || kind === 'user_model' || kind === 'episodic' || kind === 'semantic' || kind === 'relationship' || kind === 'procedural') return kind
  return 'semantic'
}

function normalizeMemoryContentKind(value: unknown): MemoryContentKind {
  const kind = String(value ?? '').toLowerCase()
  if (kind === 'fact' || kind === 'opinion' || kind === 'emotion' || kind === 'inference') return kind
  return 'fact'
}

function normalizeMemoryLifecycle(value: unknown): MemoryLifecycleState {
  const state = String(value ?? '').toLowerCase()
  // `forgotten` is an input compatibility alias; canonical soft-forget state
  // is `dormant`, while `deleted` is the tombstone state.
  if (state === 'forgotten') return 'dormant'
  if (state === 'active' || state === 'dormant' || state === 'deleted') return state
  return 'active'
}

function normalizeMemorySource(value: unknown): MemorySource | undefined {
  if (!value || typeof value !== 'object') return undefined
  const source = value as UnknownRecord
  const sourceType = String(source.sourceType ?? source.source_type ?? source.type ?? 'unknown')
  const excerptValue = source.excerpt ?? source.text
  const createdAt = source.createdAt ?? source.created_at
  return {
    sourceType,
    sourceId: source.sourceId || source.source_id ? String(source.sourceId ?? source.source_id) : undefined,
    conversationId: source.conversationId || source.conversation_id ? String(source.conversationId ?? source.conversation_id) : undefined,
    conversationTitle: source.conversationTitle || source.conversation_title ? String(source.conversationTitle ?? source.conversation_title) : undefined,
    messageId: source.messageId || source.message_id ? String(source.messageId ?? source.message_id) : undefined,
    excerpt: excerptValue ? String(excerptValue) : undefined,
    excerptHash: source.excerptHash || source.excerpt_hash ? String(source.excerptHash ?? source.excerpt_hash) : undefined,
    evidenceWeight: source.evidenceWeight !== undefined || source.evidence_weight !== undefined
      ? clampUnit(source.evidenceWeight ?? source.evidence_weight, 0)
      : undefined,
    createdAt: createdAt ? String(createdAt) : undefined,
  }
}

function normalizeMemory(value: unknown): MemoryRecord | undefined {
  if (!value || typeof value !== 'object') return undefined
  const raw = value as UnknownRecord
  const source = raw.memory && typeof raw.memory === 'object' ? raw.memory as UnknownRecord : raw
  const id = String(source.id ?? source.memoryId ?? source.memory_id ?? '')
  if (!id) return undefined
  const contentValue = source.content ?? source.text ?? source.summary ?? ''
  const sourceValues = source.sources ?? source.provenance ?? source.memorySources ?? source.memory_sources
  const sources = Array.isArray(sourceValues)
    ? sourceValues.map(normalizeMemorySource).filter((item): item is MemorySource => Boolean(item))
    : []
  if (sources.length === 0) {
    const conversationId = source.sourceConversationId ?? source.source_conversation_id ?? source.conversationId ?? source.conversation_id
    const messageId = source.sourceMessageId ?? source.source_message_id ?? source.messageId ?? source.message_id
    if (conversationId || messageId) {
      sources.push({
        sourceType: messageId ? 'message' : 'conversation',
        conversationId: conversationId ? String(conversationId) : undefined,
        messageId: messageId ? String(messageId) : undefined,
      })
    }
  }
  const valenceValue = source.valence === undefined ? undefined : Number(source.valence)
  return {
    id,
    kind: normalizeMemoryKind(source.kind ?? source.memoryKind ?? source.memory_kind),
    contentKind: normalizeMemoryContentKind(source.contentKind ?? source.content_kind ?? source.nature ?? source.category),
    content: typeof contentValue === 'string' ? contentValue : JSON.stringify(contentValue),
    confidence: clampUnit(source.confidence, 0),
    salience: clampUnit(source.salience ?? source.importance, 0),
    valence: valenceValue !== undefined && Number.isFinite(valenceValue) ? Math.max(-1, Math.min(1, valenceValue)) : undefined,
    sensitivity: source.sensitivity ? String(source.sensitivity) : undefined,
    lifecycleState: normalizeMemoryLifecycle(source.lifecycleState ?? source.lifecycle_state ?? source.lifecycle ?? source.status),
    pinned: Boolean(source.pinned ?? source.isPinned ?? source.is_pinned),
    accessCount: Math.max(0, Number(source.accessCount ?? source.access_count ?? 0) || 0),
    lastRecalledAt: source.lastRecalledAt || source.last_recalled_at ? String(source.lastRecalledAt ?? source.last_recalled_at) : undefined,
    decayPolicy: source.decayPolicy || source.decay_policy ? String(source.decayPolicy ?? source.decay_policy) : undefined,
    embeddingVersion: source.embeddingVersion || source.embedding_version ? String(source.embeddingVersion ?? source.embedding_version) : undefined,
    createdAt: String(source.createdAt ?? source.created_at ?? ''),
    updatedAt: String(source.updatedAt ?? source.updated_at ?? source.createdAt ?? source.created_at ?? ''),
    sources,
  }
}

function normalizeMemoryList(value: unknown): MemoryRecord[] {
  const rawItems = Array.isArray(value)
    ? value
    : value && typeof value === 'object'
      ? ((value as UnknownRecord).items ?? (value as UnknownRecord).memories ?? (value as UnknownRecord).results)
      : undefined
  return Array.isArray(rawItems)
    ? rawItems.map(normalizeMemory).filter((item): item is MemoryRecord => Boolean(item))
    : []
}

function normalizeArchiveResult(value: unknown): ArchiveSearchResult | undefined {
  if (!value || typeof value !== 'object') return undefined
  const raw = value as UnknownRecord
  const id = String(raw.id ?? raw.resultId ?? raw.result_id ?? raw.messageId ?? raw.message_id ?? '')
  const contentValue = raw.content ?? raw.text ?? raw.excerpt ?? raw.snippet ?? ''
  if (!id || !contentValue) return undefined
  const roleValue = String(raw.role ?? '').toLowerCase()
  const role = roleValue === 'user' || roleValue === 'assistant' || roleValue === 'tool' ? roleValue : undefined
  const matchValue = String(raw.matchType ?? raw.match_type ?? '').toLowerCase()
  const matchType = matchValue === 'lexical' || matchValue === 'semantic' || matchValue === 'hybrid' ? matchValue : undefined
  const scoreValue = Number(raw.score ?? raw.rank)
  return {
    id,
    conversationId: raw.conversationId || raw.conversation_id ? String(raw.conversationId ?? raw.conversation_id) : undefined,
    conversationTitle: raw.conversationTitle || raw.conversation_title ? String(raw.conversationTitle ?? raw.conversation_title) : undefined,
    messageId: raw.messageId || raw.message_id ? String(raw.messageId ?? raw.message_id) : undefined,
    role,
    content: String(contentValue),
    snippet: raw.snippet ? String(raw.snippet) : undefined,
    createdAt: raw.createdAt || raw.created_at ? String(raw.createdAt ?? raw.created_at) : undefined,
    score: Number.isFinite(scoreValue) ? scoreValue : undefined,
    matchType,
  }
}

function normalizeArchiveResponse(value: unknown, query: string): ArchiveSearchResponse {
  const rawItems = Array.isArray(value)
    ? value
    : value && typeof value === 'object'
      ? ((value as UnknownRecord).results ?? (value as UnknownRecord).hits ?? (value as UnknownRecord).items)
      : undefined
  const results = Array.isArray(rawItems)
    ? rawItems.map(normalizeArchiveResult).filter((item): item is ArchiveSearchResult => Boolean(item))
    : []
  const raw = value && typeof value === 'object' ? value as UnknownRecord : undefined
  const total = raw && raw.total !== undefined ? Number(raw.total) : results.length
  return { results, total: Number.isFinite(total) ? total : results.length, query }
}

export { normalizeArchiveResponse, normalizeMemory, normalizeMemoryList }
