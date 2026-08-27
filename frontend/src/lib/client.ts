import type {
  ArchiveSearchRequest,
  ArchiveSearchResponse,
  ArchiveSearchResult,
  ApprovalRequest,
  ChatEvent,
  ChatRequest,
  CodexAccount,
  Conversation,
  MemoryContentKind,
  MemoryKind,
  MemoryLifecycleState,
  MemoryListOptions,
  MemoryRecord,
  MemorySource,
  MemoryUpdate,
  ProviderSettings,
  ProviderSnapshot,
  RunResult,
  ToolCall,
  UsageLimits,
  YuriClient,
} from './contracts'

type UnknownRecord = Record<string, unknown>
type BridgeMethod = (...args: unknown[]) => unknown

const defaultSettings: ProviderSettings = {
  kind: 'openai-compatible',
  baseUrl: 'https://api.openai.com/v1',
  model: 'gpt-4o-mini',
  apiKeyConfigured: false,
  timeoutSeconds: 90,
  streamResponses: true,
}

const defaultLimits: UsageLimits = {
  plan: 'ChatGPT Plus',
  windowLabel: '5-часовое окно',
  usedPercent: 24,
  resetsAt: 'через 3 ч 18 мин',
  detail: 'Лимиты предоставлены Codex App Server после OAuth-входа.',
}

function nowIso(): string {
  return new Date().toISOString()
}

function makeId(prefix: string): string {
  const suffix = typeof crypto !== 'undefined' && 'randomUUID' in crypto
    ? crypto.randomUUID()
    : Math.random().toString(36).slice(2)

  return `${prefix}-${suffix}`
}

function getNested(root: unknown, path: string[]): unknown {
  let value: unknown = root
  for (const key of path) {
    if (!value || typeof value !== 'object') return undefined
    value = (value as UnknownRecord)[key]
  }
  return value
}

function findBridgeMethod(names: string[]): BridgeMethod | undefined {
  if (typeof window === 'undefined') return undefined

  const candidates = [
    ['go', 'main', 'Bridge'],
    ['go', 'desktop', 'Bridge'],
    ['go', 'app', 'Bridge'],
    ['go', 'Bridge'],
  ]

  for (const path of candidates) {
    const bridge = getNested(window as unknown as UnknownRecord, path)
    if (!bridge || typeof bridge !== 'object') continue

    for (const name of names) {
      const method = (bridge as UnknownRecord)[name]
      if (typeof method === 'function') return method as BridgeMethod
    }
  }

  return undefined
}

function findRuntimeMethod(name: string): BridgeMethod | undefined {
  if (typeof window === 'undefined') return undefined
  const runtime = getNested(window as unknown as UnknownRecord, ['runtime'])
  if (!runtime || typeof runtime !== 'object') return undefined
  const method = (runtime as UnknownRecord)[name]
  return typeof method === 'function' ? method as BridgeMethod : undefined
}

function subscribeRuntimeEvent(name: string, callback: (value: unknown) => void): (() => void) | undefined {
  const on = findRuntimeMethod('EventsOn')
  if (!on) return undefined
  void on(name, callback)
  const off = findRuntimeMethod('EventsOff')
  return off ? () => { void off(name) } : undefined
}

export function subscribeMemoryUpdates(callback: () => void): () => void {
  return subscribeRuntimeEvent('yuri:memory', () => callback()) ?? (() => undefined)
}

function normalizeChatEvent(value: unknown): ChatEvent | undefined {
  if (!value || typeof value !== 'object') return undefined
  const source = value as UnknownRecord
  const nested = source.data && typeof source.data === 'object' ? source.data as UnknownRecord : source
  const type = String(nested.type ?? nested.eventType ?? '')
  const runId = String(nested.runId ?? nested.run_id ?? '')
  if (!type || !runId) return undefined
  const base = { runId }
  switch (type) {
    case 'run.started':
      return { type, ...base }
    case 'assistant.delta':
      return { type, ...base, messageId: String(nested.messageId ?? nested.message_id ?? ''), delta: String(nested.delta ?? nested.text ?? '') }
    case 'assistant.completed':
      return { type, ...base, messageId: String(nested.messageId ?? nested.message_id ?? '') }
    case 'tool.started':
    case 'tool.updated': {
      const rawTool = nested.toolCall && typeof nested.toolCall === 'object' ? nested.toolCall as UnknownRecord : {}
      const toolCall: ToolCall = {
        id: String(rawTool.id ?? ''),
        name: String(rawTool.name ?? ''),
        label: String(rawTool.label ?? rawTool.name ?? 'Инструмент'),
        risk: (rawTool.risk === 'medium' || rawTool.risk === 'high' || rawTool.risk === 'critical') ? rawTool.risk : 'low',
        status: (rawTool.status === 'completed' || rawTool.status === 'failed' || rawTool.status === 'cancelled' || rawTool.status === 'denied') ? rawTool.status : type === 'tool.updated' ? 'completed' : 'running',
        args: rawTool.args && typeof rawTool.args === 'object' ? rawTool.args as Record<string, unknown> : {},
        result: rawTool.result ? String(rawTool.result) : undefined,
        startedAt: rawTool.startedAt ? String(rawTool.startedAt) : undefined,
        finishedAt: rawTool.finishedAt ? String(rawTool.finishedAt) : undefined,
      }
      return { type, ...base, toolCall }
    }
    case 'approval.required': {
      const rawApproval = nested.approval && typeof nested.approval === 'object' ? nested.approval as UnknownRecord : {}
      return {
        type,
        ...base,
        approval: {
          id: String(rawApproval.id ?? ''),
          toolCallId: String(rawApproval.toolCallId ?? rawApproval.tool_call_id ?? ''),
          title: String(rawApproval.title ?? 'Требуется подтверждение'),
          explanation: String(rawApproval.explanation ?? ''),
          risk: (rawApproval.risk === 'medium' || rawApproval.risk === 'high' || rawApproval.risk === 'critical') ? rawApproval.risk : 'medium',
          scope: String(rawApproval.scope ?? ''),
          expiresAt: rawApproval.expiresAt ? String(rawApproval.expiresAt) : undefined,
        },
      }
    }
    case 'run.status': {
      const status = nested.status
      if (status !== 'thinking' && status !== 'tool_running' && status !== 'waiting_approval' && status !== 'speaking' && status !== 'idle' && status !== 'cancelled' && status !== 'error') return undefined
      return { type, ...base, status, label: String(nested.label ?? '') }
    }
    case 'run.completed': {
      const status = nested.status === 'cancelled' || nested.status === 'error' ? nested.status : 'complete'
      return { type, ...base, status, error: nested.error ? String(nested.error) : undefined }
    }
    default:
      return undefined
  }
}

async function callBridge<T>(names: string[], args: unknown[] = []): Promise<T | undefined> {
  const method = findBridgeMethod(names)
  if (!method) return undefined
  return await method(...args) as T
}

async function callBridgeSafe<T>(names: string[], args: unknown[] = []): Promise<T | undefined> {
  try {
    return await callBridge<T>(names, args)
  } catch {
    return undefined
  }
}

function clampUnit(value: unknown, fallback = 0): number {
  const numeric = Number(value)
  if (!Number.isFinite(numeric)) return fallback
  const normalized = numeric > 1 && numeric <= 100 ? numeric / 100 : numeric
  return Math.max(0, Math.min(1, normalized))
}

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

function cloneConversation(conversation: Conversation): Conversation {
  return {
    ...conversation,
    messages: conversation.messages.map((message) => ({
      ...message,
      toolCall: message.toolCall ? { ...message.toolCall, args: { ...message.toolCall.args } } : undefined,
    })),
  }
}

function starterConversation(): Conversation {
  const createdAt = nowIso()
  return {
    id: 'conversation-welcome',
    title: 'Знакомство с Yuri',
    preview: 'Текстовый vertical slice уже готов к работе.',
    updatedAt: createdAt,
    messages: [
      {
        id: 'message-welcome',
        role: 'assistant',
        content: 'Привет. Я Yuri — твой локальный AI-компаньон. Могу отвечать потоково, объяснять действия и ждать разрешения перед изменением файлов или отправкой данных. С чего начнём?',
        status: 'complete',
        createdAt,
      },
    ],
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => globalThis.setTimeout(resolve, ms))
}

async function blobToBase64(blob: Blob): Promise<string> {
  const buffer = new Uint8Array(await blob.arrayBuffer())
  let binary = ''
  const chunkSize = 0x8000
  for (let offset = 0; offset < buffer.length; offset += chunkSize) {
    binary += String.fromCharCode(...buffer.subarray(offset, offset + chunkSize))
  }
  return btoa(binary)
}

class MockYuriClient implements YuriClient {
  readonly mode = 'mock' as const
  private readonly conversations = new Map<string, Conversation>([[starterConversation().id, starterConversation()]])
  private readonly cancelledRuns = new Set<string>()
  private readonly pendingApprovals = new Map<string, (decision: 'approve' | 'deny') => void>()
  private provider: ProviderSnapshot = {
    settings: { ...defaultSettings },
    codex: { connected: false },
  }
  private allowedDirectories: string[] = []

  async listConversations(): Promise<Conversation[]> {
    return [...this.conversations.values()]
      .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
      .map(cloneConversation)
  }

  async createConversation(title: string): Promise<Conversation> {
    const conversation: Conversation = {
      id: makeId('conversation'),
      title: title || 'Новый диалог',
      preview: 'Пока нет сообщений',
      updatedAt: nowIso(),
      messages: [],
    }
    this.conversations.set(conversation.id, conversation)
    return cloneConversation(conversation)
  }

  async sendMessage(request: ChatRequest, onEvent: (event: ChatEvent) => void): Promise<RunResult> {
    return this.run(request, onEvent)
  }

  async retryLast(request: ChatRequest, onEvent: (event: ChatEvent) => void): Promise<RunResult> {
    return this.run(request, onEvent)
  }

  async cancelRun(runId: string): Promise<void> {
    this.cancelledRuns.add(runId)
    // Wake a run that is waiting for a user decision so cancellation is deterministic.
    for (const [approvalId, resolve] of this.pendingApprovals) {
      this.pendingApprovals.delete(approvalId)
      resolve('deny')
    }
  }

  async approve(approvalId: string, decision: 'approve' | 'deny'): Promise<void> {
    const resolve = this.pendingApprovals.get(approvalId)
    if (!resolve) return
    this.pendingApprovals.delete(approvalId)
    resolve(decision)
  }

  async getProviderSnapshot(): Promise<ProviderSnapshot> {
    return {
      settings: { ...this.provider.settings },
      codex: { ...this.provider.codex, limits: this.provider.codex.limits ? { ...this.provider.codex.limits } : undefined },
    }
  }

  async saveProviderSettings(settings: ProviderSettings, apiKey?: string): Promise<void> {
    this.provider = {
      ...this.provider,
      settings: { ...settings, apiKeyConfigured: settings.apiKeyConfigured || Boolean(apiKey?.trim()) },
    }
  }

  async testProvider(settings: ProviderSettings): Promise<{ ok: boolean; message: string }> {
    await sleep(280)
    if (settings.kind === 'codex-app-server') {
      return { ok: this.provider.codex.connected, message: this.provider.codex.connected ? 'Codex App Server отвечает.' : 'Сначала выполните OAuth-вход.' }
    }
    if (!settings.baseUrl.trim() || !settings.model.trim()) return { ok: false, message: 'Укажите Base URL и модель.' }
    return { ok: true, message: 'Endpoint доступен для потокового запроса.' }
  }

  async loginCodex(): Promise<CodexAccount> {
    await sleep(480)
    this.provider = {
      ...this.provider,
      settings: { ...this.provider.settings, kind: 'codex-app-server' },
      codex: {
        connected: true,
        email: 'you@example.com',
        plan: 'ChatGPT Plus',
        authenticatedAt: nowIso(),
        limits: { ...defaultLimits },
      },
    }
    return { ...this.provider.codex, limits: { ...defaultLimits } }
  }

  async refreshCodexLimits(): Promise<UsageLimits | undefined> {
    if (!this.provider.codex.connected) return undefined
    await sleep(220)
    const limits = { ...defaultLimits, usedPercent: Math.min(defaultLimits.usedPercent + 1, 99) }
    this.provider.codex = { ...this.provider.codex, limits }
    return limits
  }

  async transcribeAudio(blob: Blob): Promise<string> {
    if (blob.size === 0) throw new Error('Голосовой фрагмент пуст.')
    await sleep(180)
    return 'Тестовая расшифровка голосового сообщения'
  }

  async getAllowedDirectories(): Promise<string[]> {
    return [...this.allowedDirectories]
  }

  async saveAllowedDirectories(directories: string[]): Promise<void> {
    this.allowedDirectories = [...directories]
  }

  async listMemories(_options: MemoryListOptions = {}): Promise<MemoryRecord[]> {
    // The mock intentionally has no invented memories. This keeps an offline
    // preview honest while still exercising the empty-state UI.
    return []
  }

  async searchArchive(request: ArchiveSearchRequest): Promise<ArchiveSearchResponse> {
    return { results: [], total: 0, query: request.query }
  }

  async updateMemory(_memoryId: string, _update: MemoryUpdate): Promise<MemoryRecord | undefined> {
    return undefined
  }

  async setMemoryLifecycle(_memoryId: string, _state: MemoryLifecycleState): Promise<MemoryRecord | undefined> {
    return undefined
  }

  async deleteMemory(_memoryId: string): Promise<void> {
    // There is no local preview record to remove.
  }

  private async run(request: ChatRequest, onEvent: (event: ChatEvent) => void): Promise<RunResult> {
    const conversation = this.conversations.get(request.conversationId) ?? starterConversation()
    this.conversations.set(conversation.id, conversation)
    const runId = makeId('run')
    const messageId = makeId('assistant')
    const userMessage: Conversation['messages'][number] = {
      id: makeId('user'),
      role: 'user',
      content: request.text,
      status: 'complete',
      createdAt: nowIso(),
    }
    conversation.messages.push(userMessage)
    conversation.updatedAt = nowIso()
    conversation.preview = request.text
    onEvent({ type: 'run.started', runId })
    onEvent({ type: 'run.status', runId, status: 'thinking', label: 'Yuri формирует ответ…' })
    await sleep(260)
    if (this.cancelledRuns.has(runId)) return this.finishCancelled(runId, onEvent)

    const needsApproval = /(запиш|измени|перезапиши|удали|отправ|созда)/i.test(request.text)
    if (needsApproval) {
      const toolCall: ToolCall = {
        id: makeId('tool'),
        name: 'filesystem.write',
        label: 'Изменение файла',
        risk: 'medium',
        status: 'running',
        args: { path: '~/Documents/notes.txt', operation: 'append', bytes: 86 },
        startedAt: nowIso(),
      }
      onEvent({ type: 'tool.started', runId, toolCall })
      const approval: ApprovalRequest = {
        id: makeId('approval'),
        toolCallId: toolCall.id,
        title: 'Разрешить изменение файла?',
        explanation: 'Yuri подготовила запись в разрешённой директории. Файл ещё не изменён.',
        risk: 'medium',
        scope: '~/Documents/notes.txt · добавить 86 байт',
      }
      const approvalDecision = new Promise<'approve' | 'deny'>((resolve) => this.pendingApprovals.set(approval.id, resolve))
      onEvent({ type: 'approval.required', runId, approval })
      onEvent({ type: 'run.status', runId, status: 'waiting_approval', label: 'Ожидается ваше разрешение' })
      const decision = await approvalDecision
      if (this.cancelledRuns.has(runId)) return this.finishCancelled(runId, onEvent)
      if (decision === 'deny') {
        onEvent({ type: 'tool.updated', runId, toolCall: { ...toolCall, status: 'denied', result: 'Операция отклонена пользователем.', finishedAt: nowIso() } })
        return this.finishError(runId, onEvent, 'Операция отклонена пользователем.')
      }
      onEvent({ type: 'tool.updated', runId, toolCall: { ...toolCall, status: 'completed', result: 'Изменение подготовлено в mock режиме.', finishedAt: nowIso() } })
      await sleep(180)
    }

    const response = needsApproval
      ? 'Готово. Я показала действие и дождалась разрешения перед записью. В mock-режиме файл не меняется, но этот же контракт будет использовать реальный tool runner.'
      : `Принято: «${request.text}»\n\nЯ отвечаю потоково. Когда появится действие с побочным эффектом, покажу его отдельно и попрошу разрешение.`
    const assistantMessage: Conversation['messages'][number] = {
      id: messageId,
      role: 'assistant',
      content: '',
      status: 'streaming',
      createdAt: nowIso(),
      runId,
    }
    conversation.messages.push(assistantMessage)
    for (const chunk of response.split(/(?<=\s)/)) {
      if (this.cancelledRuns.has(runId)) return this.finishCancelled(runId, onEvent)
      onEvent({ type: 'assistant.delta', runId, messageId, delta: chunk })
      await sleep(22)
    }
    onEvent({ type: 'assistant.completed', runId, messageId })
    onEvent({ type: 'run.completed', runId, status: 'complete' })
    conversation.updatedAt = nowIso()
    conversation.preview = response.slice(0, 100)
    return { runId, status: 'complete' }
  }

  private finishCancelled(runId: string, onEvent: (event: ChatEvent) => void): RunResult {
    onEvent({ type: 'run.completed', runId, status: 'cancelled' })
    return { runId, status: 'cancelled' }
  }

  private finishError(runId: string, onEvent: (event: ChatEvent) => void, error: string): RunResult {
    onEvent({ type: 'run.completed', runId, status: 'error', error })
    return { runId, status: 'error' }
  }
}

class WailsYuriClient implements YuriClient {
  readonly mode = 'wails' as const

  async listConversations(): Promise<Conversation[]> {
    return (await callBridge<Conversation[]>(['ListConversations', 'GetConversations'])) ?? []
  }

  async createConversation(title: string): Promise<Conversation> {
    return (await callBridge<Conversation>(['NewConversation', 'CreateConversation'], [title])) ?? {
      id: makeId('conversation'),
      title: title || 'Новый диалог',
      preview: 'Пока нет сообщений',
      updatedAt: nowIso(),
      messages: [],
    }
  }

  async sendMessage(request: ChatRequest, onEvent: (event: ChatEvent) => void): Promise<RunResult> {
    return this.runWithBridge(['SendMessage', 'StartChat', 'Chat'], request, onEvent)
  }

  async retryLast(request: ChatRequest, onEvent: (event: ChatEvent) => void): Promise<RunResult> {
    return this.runWithBridge(['RetryMessage', 'RetryChat', 'SendMessage'], request, onEvent)
  }

  async cancelRun(runId: string): Promise<void> {
    await callBridge(['CancelRun', 'CancelAgentRun'], [runId])
  }

  async approve(approvalId: string, decision: 'approve' | 'deny'): Promise<void> {
    await callBridge(['ResolveApproval', 'ApproveAction'], [{ approvalId, decision }])
  }

  async getProviderSnapshot(): Promise<ProviderSnapshot> {
    const providers = await callBridgeSafe<unknown>(['ListProviders'])
    const accountResult = await callBridgeSafe<unknown>(['CodexAccount'])
    const account = accountResult && typeof accountResult === 'object' ? normalizeCodexAccount(accountResult as UnknownRecord) : undefined
    const limits = normalizeUsageLimits(await callBridgeSafe<unknown>(['CodexRateLimits']))
    const providerList = Array.isArray(providers) ? providers : []
    const enabledProvider = providerList.find((item): item is UnknownRecord => Boolean(item && typeof item === 'object' && (item as UnknownRecord).enabled))
    const openai = providerList.find((item): item is UnknownRecord => Boolean(item && typeof item === 'object' && ((item as UnknownRecord).kind === 'openai-compatible' || (item as UnknownRecord).type === 'openai-compatible')))
    const selectedOpenAI = enabledProvider && (enabledProvider.kind === 'openai-compatible' || enabledProvider.type === 'openai-compatible') ? enabledProvider : openai
    const settings: ProviderSettings = enabledProvider && (enabledProvider.kind === 'codex-app-server' || enabledProvider.type === 'codex-app-server')
      ? { ...defaultSettings, kind: 'codex-app-server', model: String(enabledProvider.model ?? 'gpt-5-codex') }
      : selectedOpenAI
      ? {
          ...defaultSettings,
          ...selectedOpenAI,
          kind: 'openai-compatible',
          apiKeyConfigured: Boolean(selectedOpenAI.apiKeyConfigured ?? selectedOpenAI.api_key_configured ?? selectedOpenAI.hasSecret),
        }
      : (await callBridge<ProviderSnapshot>(['GetProviderSnapshot', 'GetProviderSettings']))?.settings ?? defaultSettings
    return {
      settings,
      codex: {
        ...(account ?? { connected: false }),
        limits: limits ?? account?.limits,
      },
    }
  }

  async saveProviderSettings(settings: ProviderSettings, apiKey?: string): Promise<void> {
    if (settings.kind === 'openai-compatible') {
      await callBridge(['SaveOpenAIProvider'], [{
        id: 'openai',
        displayName: 'OpenAI-compatible',
        baseUrl: settings.baseUrl,
        model: settings.model,
        apiKey,
        enabled: true,
      }])
      return
    }
    await callBridge(['SaveCodexProvider', 'SaveProviderSettings', 'SetProviderSettings'], [{
      id: 'codex',
      displayName: 'Codex App Server',
      model: '',
      binary: 'codex',
      enabled: true,
    }])
  }

  async testProvider(settings: ProviderSettings): Promise<{ ok: boolean; message: string }> {
    return (await callBridge<{ ok: boolean; message: string }>(['TestProvider', 'ProbeProvider'], [settings])) ?? { ok: false, message: 'Backend не вернул результат проверки.' }
  }

  async loginCodex(): Promise<CodexAccount> {
    const login = await callBridge<UnknownRecord>(['StartCodexLogin', 'LoginCodex', 'StartCodexOAuth', 'ConnectCodexAccount'], ['browser'])
    if (login && (login.authUrl || login.authURL || login.verificationUrl)) {
      const loginUrl = String(login.authUrl ?? login.authURL ?? login.verificationUrl)
      if (typeof window !== 'undefined') window.open(loginUrl, '_blank', 'noopener,noreferrer')
      return {
        connected: false,
        loginUrl,
        verificationUrl: login.verificationUrl ? String(login.verificationUrl) : undefined,
        userCode: login.userCode ? String(login.userCode) : undefined,
      }
    }
    return login ? normalizeCodexAccount(login) : { connected: false }
  }

  async refreshCodexLimits(): Promise<UsageLimits | undefined> {
    const result = await callBridge<unknown>(['CodexRateLimits', 'RefreshCodexLimits', 'GetCodexUsage'])
    return normalizeUsageLimits(result)
  }

  async transcribeAudio(blob: Blob): Promise<string> {
    const result = await callBridge<{ text?: string }>(['TranscribeAudio'], [{
      audioBase64: await blobToBase64(blob),
      filename: 'recording.webm',
      contentType: blob.type || 'audio/webm',
      language: 'ru',
    }])
    const text = result?.text?.trim()
    if (!text) throw new Error('STT provider не вернул текст.')
    return text
  }

  async getAllowedDirectories(): Promise<string[]> {
    return (await callBridge<string[]>(['AllowedDirectories'])) ?? []
  }

  async saveAllowedDirectories(directories: string[]): Promise<void> {
    await callBridge(['SaveAllowedDirectories'], [directories])
  }

  async listMemories(options: MemoryListOptions = {}): Promise<MemoryRecord[]> {
    const wireOptions = {
      ...options,
      lifecycle: options.lifecycleState,
      includeDormant: options.lifecycleState === 'dormant' || options.lifecycleState === 'all',
      includeDeleted: options.lifecycleState === 'all',
    }
    const result = await callBridge<unknown>(['ListMemories'], [wireOptions])
    return normalizeMemoryList(result)
  }

  async searchArchive(request: ArchiveSearchRequest): Promise<ArchiveSearchResponse> {
    const result = await callBridge<unknown>(['SearchArchive'], [request])
    return normalizeArchiveResponse(result, request.query)
  }

  async updateMemory(memoryId: string, update: MemoryUpdate): Promise<MemoryRecord | undefined> {
    const result = await callBridge<unknown>(['UpdateMemory'], [{ id: memoryId, memoryId, ...update }])
    return normalizeMemory(result)
  }

  async setMemoryLifecycle(memoryId: string, state: MemoryLifecycleState): Promise<MemoryRecord | undefined> {
    const result = await callBridge<unknown>(['SetMemoryLifecycle'], [{ id: memoryId, memoryId, state, lifecycle: state, lifecycleState: state }])
    return normalizeMemory(result)
  }

  async deleteMemory(memoryId: string): Promise<void> {
    await callBridge(['DeleteMemory'], [{ id: memoryId, memoryId }])
  }

  private async runWithBridge(names: string[], request: ChatRequest, onEvent: (event: ChatEvent) => void): Promise<RunResult> {
    // Wails v2 currently exposes a single ChatRequest object. The adapter keeps
    // the request typed so the binding can evolve without changing the UI.
    const seen = new Set<string>()
    const handleLiveEvent = (value: unknown) => {
      const event = normalizeChatEvent(value)
      if (!event) return
      const key = JSON.stringify(event)
      if (seen.has(key)) return
      seen.add(key)
      onEvent(event)
    }
    const unsubscribe = subscribeRuntimeEvent('yuri:chat', handleLiveEvent)
    try {
      const result = await callBridge<RunResult | { runId?: string; status?: RunResult['status']; events?: ChatEvent[] }>(names, [request])
      if (!result) return { runId: makeId('run'), status: 'error' }
      if ('events' in result && Array.isArray(result.events)) result.events.forEach(handleLiveEvent)
      return { runId: result.runId ?? makeId('run'), status: result.status ?? 'complete' }
    } finally {
      unsubscribe?.()
    }
  }
}

function normalizeCodexAccount(value: UnknownRecord): CodexAccount {
  const accountValue = value.account && typeof value.account === 'object' ? value.account as UnknownRecord : value
  const email = accountValue.email ? String(accountValue.email) : undefined
  const plan = accountValue.planType ? String(accountValue.planType) : accountValue.plan ? String(accountValue.plan) : undefined
  return { connected: Boolean(accountValue.connected ?? accountValue.authenticated ?? email), email, plan }
}

function normalizeUsageLimits(value: unknown): UsageLimits | undefined {
  if (!value || typeof value !== 'object') return undefined
  const record = value as UnknownRecord
  const source = record.rateLimits && typeof record.rateLimits === 'object' ? record.rateLimits as UnknownRecord : record
  const primary = source.primary && typeof source.primary === 'object' ? source.primary as UnknownRecord : source
  const usedPercent = Number(primary.usedPercent ?? source.usedPercent)
  if (!Number.isFinite(usedPercent)) return undefined
  const resetValue = Number(primary.resetsAt ?? source.resetsAt)
  const resetsAt = Number.isFinite(resetValue) && resetValue > 0
    ? new Date(resetValue > 10_000_000_000 ? resetValue : resetValue * 1000).toLocaleString('ru-RU', { dateStyle: 'short', timeStyle: 'short' })
    : 'неизвестно'
  return {
    plan: String(source.planType ?? 'ChatGPT'),
    windowLabel: primary.windowDurationMins ? String(primary.windowDurationMins) + ' мин окно' : 'Текущее окно',
    usedPercent: Math.max(0, Math.min(100, usedPercent)),
    resetsAt,
    detail: 'Лимиты получены из Codex App Server. Yuri не читает и не хранит OAuth-токены.',
  }
}

let client: YuriClient | undefined

export function createYuriClient(): YuriClient {
  if (client) return client
  client = findBridgeMethod(['ListConversations', 'GetConversations', 'SendMessage', 'StartChat'])
    ? new WailsYuriClient()
    : new MockYuriClient()
  return client
}

export function resetYuriClientForTests(): void {
  client = undefined
}
