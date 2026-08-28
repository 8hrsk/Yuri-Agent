import type {
  ArchiveSearchRequest,
  ArchiveSearchResponse,
  ArchiveSearchResult,
  ActivityEvent,
  ActivityListOptions,
  ActivityStatus,
  ActivityType,
  ApprovalRequest,
  ChatEvent,
  ChatRequest,
  CodexAccount,
  Conversation,
  EncryptedBackupInfo,
  EncryptedBackupInput,
  EncryptedBackupInspectInput,
  EncryptedBackupRestoreInput,
  MemoryContentKind,
  MemoryKind,
  MemoryLifecycleState,
  MemoryListOptions,
  MemoryRecord,
  MemorySource,
  MemoryUpdate,
  OnboardingResult,
  OnboardingState,
  PersonaVersion,
  PersonalitySnapshot,
  DeliveryChannel,
  JobRun,
  JobRunListOptions,
  JobRunStatus,
  MisfirePolicy,
  PluginInstallRequest,
  PluginPackageInspection,
  PluginPermission,
  PluginRecord,
  PluginSignatureStatus,
  PluginStatus,
  PluginTool,
  ProactivitySettings,
  ProviderSettings,
  ProviderSnapshot,
  ProviderTestResult,
  RunResult,
  Schedule,
  ScheduleBudget,
  ScheduleInput,
  ScheduleStatus,
  ScheduleType,
  YuriNotification,
  YuriNotificationType,
  ToolRisk,
  ToolCall,
  UsageLimits,
  YuriClient,
} from './contracts'
import {
  clonePersonalitySnapshot,
  createStarterPersonalitySnapshot,
  normalizePersonalitySnapshot,
} from './personality'

export {
  clonePersonalitySnapshot,
  createStarterPersonalitySnapshot,
  defaultAffectiveState,
  dominantAffectMood,
  mapAvatarState,
  normalizePersonalitySnapshot,
  normalizeAvatarState,
} from './personality'

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

const defaultOnboardingState: OnboardingState = {
  completed: false,
  providerTested: false,
}

const defaultLimits: UsageLimits = {
  plan: 'ChatGPT Plus',
  windowLabel: '5-часовое окно',
  usedPercent: 24,
  resetsAt: 'через 3 ч 18 мин',
  detail: 'Лимиты предоставлены Codex App Server после OAuth-входа.',
}

const defaultProactivitySettings: ProactivitySettings = {
  enabled: false,
  quietHoursEnabled: true,
  quietHoursStart: '23:00',
  quietHoursEnd: '07:00',
  timezone: 'Europe/Moscow',
  dailyLimit: 5,
  cooldownMinutes: 30,
  allowLocalNotifications: true,
}

function nowIso(): string {
  return new Date().toISOString()
}

function normalizeEncryptedBackup(value: unknown): EncryptedBackupInfo | undefined {
  if (!value || typeof value !== 'object') return undefined
  const source = value as UnknownRecord
  const path = optionalString(source, 'path')
  const createdAt = optionalString(source, 'createdAt', 'created_at')
  if (!path || !createdAt) return undefined
  return {
    path,
    createdAt,
    sizeBytes: Math.max(0, optionalNumber(source, 'sizeBytes', 'size_bytes') ?? 0),
    blobCount: Math.max(0, Math.round(optionalNumber(source, 'blobCount', 'blob_count') ?? 0)),
    hasConfig: normalizeBoolean(source.hasConfig ?? source.has_config, false),
    restoredTo: optionalString(source, 'restoredTo', 'restored_to'),
  }
}

function normalizeOnboardingState(value: unknown): OnboardingState {
  if (!value || typeof value !== 'object') return { ...defaultOnboardingState }
  const raw = value as UnknownRecord
  const source = raw.onboarding && typeof raw.onboarding === 'object'
    ? raw.onboarding as UnknownRecord
    : raw
  return {
    completed: normalizeBoolean(source.completed ?? source.complete ?? source.isComplete ?? source.is_complete, false),
    providerTested: normalizeBoolean(
      source.providerTested
        ?? source.provider_tested
        ?? source.providerProbeSucceeded
        ?? source.provider_probe_succeeded
        ?? source.providerCheckPassed
        ?? source.provider_check_passed,
      false,
    ),
    completedAt: optionalString(source, 'completedAt', 'completed_at'),
  }
}

function normalizeOnboardingResult(value: unknown, fallbackState: OnboardingState): OnboardingResult {
  if (!value || typeof value !== 'object') {
    return { ok: false, message: 'Backend не вернул результат onboarding.', state: fallbackState }
  }
  const raw = value as UnknownRecord
  const source = raw.result && typeof raw.result === 'object' ? raw.result as UnknownRecord : raw
  const nestedState = source.state ?? source.onboarding ?? source.onboardingState ?? source.onboarding_state
  const hasInlineState = source.completed !== undefined
    || source.providerTested !== undefined
    || source.provider_tested !== undefined
    || source.providerProbeSucceeded !== undefined
    || source.provider_probe_succeeded !== undefined
  const state = nestedState === undefined
    ? (hasInlineState ? normalizeOnboardingState(source) : fallbackState)
    : normalizeOnboardingState(nestedState)
  return {
    ok: normalizeBoolean(source.ok ?? source.success ?? source.passed, state.completed && state.providerTested),
    message: optionalString(source, 'message', 'detail', 'error') ?? (state.completed && state.providerTested ? 'Провайдер проверен.' : 'Проверка провайдера не завершена.'),
    errorCode: optionalString(source, 'errorCode', 'error_code', 'code'),
    alternative: optionalString(source, 'alternative'),
    state,
  }
}

function onboardingSettingsWire(settings: ProviderSettings): UnknownRecord {
  return {
    kind: settings.kind,
    baseUrl: settings.baseUrl,
    model: settings.model,
    timeoutSeconds: settings.timeoutSeconds,
    streamResponses: settings.streamResponses,
    apiKeyConfigured: settings.apiKeyConfigured,
  }
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

/** Reflection emits a fresh, already-versioned snapshot; the renderer never mutates it locally. */
export function subscribePersonaUpdates(callback: (snapshot: PersonalitySnapshot) => void): () => void {
  const cleanups = ['yuri:persona', 'yuri:personality', 'yuri:relationship'].map((eventName) => subscribeRuntimeEvent(eventName, (value) => {
    callback(normalizePersonalitySnapshot(value))
  })).filter((cleanup): cleanup is () => void => Boolean(cleanup))
  return () => cleanups.forEach((cleanup) => cleanup())
}

export const subscribePersonalityUpdates = subscribePersonaUpdates

function normalizeNotificationType(value: unknown): YuriNotificationType {
  const type = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (type === 'task.completed' || type === 'task_complete') return 'task.completed'
  if (type === 'background.completed' || type === 'background_complete') return 'background.completed'
  if (type === 'plugin.event' || type === 'plugin') return 'plugin.event'
  if (type === 'rule.triggered' || type === 'rule') return 'rule.triggered'
  if (type === 'agent.message' || type === 'message') return 'agent.message'
  return 'unknown'
}

function normalizeNotification(value: unknown): YuriNotification | undefined {
  if (!value || typeof value !== 'object') return undefined
  const rawValue = value as UnknownRecord
  const envelope = rawValue.data && typeof rawValue.data === 'object' ? rawValue.data as UnknownRecord : rawValue
  const source = envelope.notification && typeof envelope.notification === 'object' ? envelope.notification as UnknownRecord : envelope
  const id = optionalString(source, 'id', 'notificationId', 'notification_id', 'eventId', 'event_id')
  const title = optionalString(source, 'title', 'subject')
  const body = optionalString(source, 'body', 'message', 'detail', 'text')
  if (!id || !title || !body) return undefined
  const permissionValue = optionalString(source, 'permission', 'nativePermission', 'native_permission')
  const permission = permissionValue === 'default' || permissionValue === 'granted' || permissionValue === 'denied'
    ? permissionValue
    : undefined
  return {
    id,
    type: normalizeNotificationType(source.type ?? source.notificationType ?? source.notification_type),
    title,
    body,
    createdAt: optionalString(source, 'createdAt', 'created_at', 'timestamp') ?? nowIso(),
    allowNative: normalizeBoolean(source.allowNative ?? source.allow_native, false),
    permission,
    conversationId: optionalString(source, 'conversationId', 'conversation_id'),
    deepLink: optionalString(source, 'deepLink', 'deep_link'),
  }
}

function normalizeBoolean(value: unknown, fallback: boolean): boolean {
  if (typeof value === 'boolean') return value
  if (typeof value === 'number') return value !== 0
  if (typeof value === 'string') {
    const normalized = value.trim().toLowerCase()
    if (normalized === 'true' || normalized === 'yes' || normalized === '1') return true
    if (normalized === 'false' || normalized === 'no' || normalized === '0') return false
  }
  return fallback
}

export function subscribeNotifications(callback: (notification: YuriNotification) => void): () => void {
  return subscribeRuntimeEvent('yuri:notification', (value) => {
    const notification = normalizeNotification(value)
    if (notification) callback(notification)
  }) ?? (() => undefined)
}

export function canUseNativeNotification(notification: YuriNotification): boolean {
  return notification.allowNative
    && typeof Notification !== 'undefined'
    && Notification.permission === 'granted'
    && (notification.permission === undefined || notification.permission === 'granted')
}

/**
 * This helper never requests permission on its own. Call it only from an
 * explicit user gesture, such as enabling local notifications in Activity.
 */
export async function requestBrowserNotificationPermission(): Promise<NotificationPermission | undefined> {
  if (typeof Notification === 'undefined') return undefined
  if (Notification.permission !== 'default') return Notification.permission
  return Notification.requestPermission()
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

function normalizePluginRisk(value: unknown): ToolRisk {
  const risk = String(value ?? '').toLowerCase()
  if (risk === 'medium' || risk === 'high' || risk === 'critical') return risk
  return 'low'
}

function normalizePluginStatus(value: unknown, enabled = false, running = false): PluginStatus {
  const status = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (running || status === 'running' || status === 'started' || status === 'healthy') return 'running'
  if (status === 'crashed' || status === 'crash') return 'crashed'
  if (status === 'error' || status === 'failed' || status === 'failure') return 'error'
  if (status === 'stopped' || status === 'idle') return 'stopped'
  if (status === 'disabled' || status === 'off') return 'disabled'
  if (status === 'enabled' || status === 'on' || enabled) return 'enabled'
  if (status === 'installed' || status === 'validated') return 'installed'
  return 'unknown'
}

function normalizePluginSignature(value: unknown, devMode = false): PluginSignatureStatus {
  const status = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (status === 'signed' || status === 'valid' || status === 'verified' || status === 'trusted') return 'signed'
  if (status === 'unsigned' || status === 'unverified' || status === 'none') return devMode ? 'dev' : 'unsigned'
  if (status === 'dev' || status === 'development') return 'dev'
  if (status === 'invalid' || status === 'rejected' || status === 'tampered') return 'invalid'
  return devMode ? 'dev' : 'unknown'
}

function normalizeStringList(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.map((item) => {
    if (typeof item === 'string' || typeof item === 'number') return String(item)
    if (!item || typeof item !== 'object') return ''
    const record = item as UnknownRecord
    return String(record.name ?? record.id ?? record.type ?? '')
  }).filter(Boolean)
}

function normalizePluginPermission(value: unknown): PluginPermission | undefined {
  if (typeof value === 'string') {
    return { capability: value, granted: false }
  }
  if (!value || typeof value !== 'object') return undefined
  const raw = value as UnknownRecord
  const capability = String(raw.capability ?? raw.name ?? raw.id ?? '')
  if (!capability) return undefined
  const scopeValue = raw.scope ?? raw.scopeJson ?? raw.scope_json
  const scope = scopeValue === undefined
    ? undefined
    : typeof scopeValue === 'string' ? scopeValue : JSON.stringify(scopeValue)
  const expiry = raw.grantExpiresAt ?? raw.grant_expires_at ?? raw.expiresAt ?? raw.expires_at
  return {
    capability,
    scope,
    description: raw.description || raw.reason ? String(raw.description ?? raw.reason) : undefined,
    risk: raw.risk === undefined ? undefined : normalizePluginRisk(raw.risk),
    granted: Boolean(raw.granted ?? raw.approved ?? raw.enabled ?? raw.allowed),
    grantExpiresAt: expiry ? String(expiry) : undefined,
  }
}

function normalizePluginTool(value: unknown): PluginTool | undefined {
  if (typeof value === 'string') {
    return { id: value, name: value, risk: 'low' }
  }
  if (!value || typeof value !== 'object') return undefined
  const raw = value as UnknownRecord
  const id = String(raw.id ?? raw.toolId ?? raw.tool_id ?? raw.name ?? '')
  if (!id) return undefined
  return {
    id,
    name: String(raw.name ?? raw.label ?? id),
    description: raw.description || raw.summary ? String(raw.description ?? raw.summary) : undefined,
    risk: normalizePluginRisk(raw.risk ?? raw.riskLevel ?? raw.risk_level),
  }
}

function normalizePlugin(value: unknown, devMode = false): PluginRecord | undefined {
  if (!value || typeof value !== 'object') return undefined
  const rawValue = value as UnknownRecord
  const source = rawValue.plugin && typeof rawValue.plugin === 'object' ? rawValue.plugin as UnknownRecord : rawValue
  const id = String(source.id ?? source.pluginId ?? source.plugin_id ?? '')
  if (!id) return undefined
  const enabled = Boolean(source.enabled ?? source.isEnabled ?? source.is_enabled)
  const running = Boolean(source.running ?? source.isRunning ?? source.is_running)
  const status = normalizePluginStatus(source.status ?? source.state, enabled, running)
  const permissionValues = source.permissions ?? source.requestedPermissions ?? source.requested_permissions ?? source.capabilities
  const toolValues = source.tools ?? source.toolDescriptors ?? source.tool_descriptors
  const minCore = source.minCoreVersion ?? source.min_core_version
  const maxCore = source.maxCoreVersion ?? source.max_core_version
  const coreVersionRange = source.coreVersionRange || source.core_version_range
    ? String(source.coreVersionRange ?? source.core_version_range)
    : minCore || maxCore
      ? `${minCore ? `>= ${String(minCore)}` : ''}${minCore && maxCore ? ' · ' : ''}${maxCore ? `<= ${String(maxCore)}` : ''}`
      : undefined
  const installedAt = source.installedAt ?? source.installed_at
  const updatedAt = source.updatedAt ?? source.updated_at
  const permissions = Array.isArray(permissionValues)
    ? permissionValues.map((item) => normalizePluginPermission(item)).filter((item): item is PluginPermission => Boolean(item))
    : []
  const tools = Array.isArray(toolValues)
    ? toolValues.map((item) => normalizePluginTool(item)).filter((item): item is PluginTool => Boolean(item))
    : []
  return {
    id,
    name: String(source.name ?? source.displayName ?? source.display_name ?? id),
    version: String(source.version ?? 'unknown'),
    publisher: source.publisher ? String(source.publisher) : undefined,
    description: source.description ? String(source.description) : undefined,
    protocolVersion: source.protocolVersion || source.protocol_version ? String(source.protocolVersion ?? source.protocol_version) : undefined,
    coreVersionRange,
    enabled,
    running,
    status,
    installPath: source.installPath || source.install_path ? String(source.installPath ?? source.install_path) : undefined,
    signatureStatus: normalizePluginSignature(source.signatureStatus ?? source.signature_status ?? source.signature, devMode),
    checksum: source.checksum ? String(source.checksum) : undefined,
    repositoryUrl: source.repositoryUrl || source.repository_url ? String(source.repositoryUrl ?? source.repository_url) : undefined,
    releaseTag: source.releaseTag || source.release_tag ? String(source.releaseTag ?? source.release_tag) : undefined,
    sourceCommit: source.sourceCommit || source.source_commit ? String(source.sourceCommit ?? source.source_commit) : undefined,
    permissions,
    tools,
    eventSources: normalizeStringList(source.eventSources ?? source.event_sources),
    lastError: source.lastError || source.last_error || source.error ? String(source.lastError ?? source.last_error ?? source.error) : undefined,
    installedAt: installedAt ? String(installedAt) : undefined,
    updatedAt: updatedAt ? String(updatedAt) : undefined,
  }
}

function normalizePluginList(value: unknown): PluginRecord[] {
  const rawItems = Array.isArray(value)
    ? value
    : value && typeof value === 'object'
      ? ((value as UnknownRecord).items ?? (value as UnknownRecord).plugins ?? (value as UnknownRecord).results)
      : undefined
  return Array.isArray(rawItems)
    ? rawItems.map((item) => normalizePlugin(item)).filter((item): item is PluginRecord => Boolean(item))
    : []
}

function emptyPluginInspection(path: string, message: string): PluginPackageInspection {
  return {
    path,
    valid: false,
    compatible: false,
    signatureStatus: 'unknown',
    warnings: [],
    errors: [message],
  }
}

function normalizePluginInspection(value: unknown, path: string, devMode = false): PluginPackageInspection {
  if (!value || typeof value !== 'object') return emptyPluginInspection(path, 'Backend не вернул результат проверки пакета.')
  const raw = value as UnknownRecord
  const source = raw.inspection && typeof raw.inspection === 'object' ? raw.inspection as UnknownRecord : raw
  const manifest = normalizePlugin(source.manifest ?? source.plugin ?? source.metadata, devMode)
  const warnings = normalizeStringList(source.warnings ?? source.warning)
  const errors = normalizeStringList(source.errors ?? source.error)
  return {
    path: String(source.path ?? path),
    valid: Boolean(source.valid ?? source.isValid ?? source.is_valid ?? manifest),
    compatible: Boolean(source.compatible ?? source.isCompatible ?? source.is_compatible ?? manifest),
    manifest,
    signatureStatus: normalizePluginSignature(source.signatureStatus ?? source.signature_status ?? manifest?.signatureStatus, devMode),
    checksum: source.checksum ? String(source.checksum) : manifest?.checksum,
    warnings,
    errors,
    installable: source.installable === undefined ? undefined : Boolean(source.installable),
    requiresDevMode: source.requiresDevMode === undefined && source.requires_dev_mode === undefined
      ? undefined
      : Boolean(source.requiresDevMode ?? source.requires_dev_mode),
  }
}

function normalizeScheduleType(value: unknown): ScheduleType {
  const type = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (type === 'once' || type === 'one_shot' || type === 'oneoff' || type === 'one_time') return 'once'
  if (type === 'interval' || type === 'repeating' || type === 'periodic') return 'interval'
  if (type === 'cron' || type === 'crontab') return 'cron'
  return 'once'
}

function normalizeScheduleStatus(value: unknown, enabled: boolean): ScheduleStatus {
  const status = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (!value) return enabled ? 'active' : 'paused'
  if (status === 'paused' || status === 'disabled' || status === 'off') return 'paused'
  if (status === 'completed' || status === 'complete' || status === 'finished') return 'completed'
  if (status === 'error' || status === 'failed' || status === 'failure') return 'error'
  if (status === 'active' || status === 'enabled' || status === 'running' || enabled) return 'active'
  return 'unknown'
}

function normalizeMisfirePolicy(value: unknown): MisfirePolicy {
  const policy = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  return policy === 'run_once' || policy === 'runonce' || policy === 'fire_once' || policy === 'execute_once'
    ? 'run_once'
    : 'skip'
}

function normalizeDeliveryChannel(value: unknown): DeliveryChannel {
  const channel = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  return channel === 'notification' || channel === 'local_notification' || channel === 'desktop' ? 'notification' : 'in_app'
}

function optionalString(source: UnknownRecord, ...keys: string[]): string | undefined {
  for (const key of keys) {
    const value = source[key]
    if (value !== undefined && value !== null && String(value).trim() !== '') return String(value)
  }
  return undefined
}

function optionalNumber(source: UnknownRecord, ...keys: string[]): number | undefined {
  for (const key of keys) {
    const value = source[key]
    if (value === undefined || value === null || value === '') continue
    const number = Number(value)
    if (Number.isFinite(number)) return number
  }
  return undefined
}

function normalizeBudget(value: unknown): ScheduleBudget | undefined {
  if (!value || typeof value !== 'object') return undefined
  const source = value as UnknownRecord
  const maxDurationSeconds = optionalNumber(source, 'maxDurationSeconds', 'max_duration_seconds', 'timeoutSeconds', 'timeout_seconds')
  const maxTokens = optionalNumber(source, 'maxTokens', 'max_tokens', 'tokenLimit', 'token_limit')
  const maxToolCalls = optionalNumber(source, 'maxToolCalls', 'max_tool_calls', 'toolLimit', 'tool_limit')
  if (maxDurationSeconds === undefined && maxTokens === undefined && maxToolCalls === undefined) return undefined
  return {
    maxDurationSeconds,
    maxTokens,
    maxToolCalls,
  }
}

function normalizeSchedule(value: unknown): Schedule | undefined {
  if (!value || typeof value !== 'object') return undefined
  const rawValue = value as UnknownRecord
  const source = rawValue.schedule && typeof rawValue.schedule === 'object' ? rawValue.schedule as UnknownRecord : rawValue
  const id = optionalString(source, 'id', 'scheduleId', 'schedule_id')
  if (!id) return undefined
  const type = normalizeScheduleType(source.type ?? source.scheduleType ?? source.schedule_type ?? source.kind)
  const enabledValue = source.enabled ?? source.isEnabled ?? source.is_enabled
  const statusValue = source.status ?? source.state
  const status = normalizeScheduleStatus(statusValue, Boolean(enabledValue))
  const enabled = enabledValue === undefined ? status === 'active' : Boolean(enabledValue)
  const intervalSeconds = optionalNumber(source, 'intervalSeconds', 'interval_seconds', 'interval', 'everySeconds', 'every_seconds')
  const expression = optionalString(source, 'expression', 'cron', 'cronExpression', 'cron_expression')
  const runAt = optionalString(source, 'runAt', 'run_at', 'startAt', 'start_at', 'scheduledAt', 'scheduled_at', 'executeAt', 'execute_at')
  const budget = normalizeBudget(source.budget ?? source.limits)
  let payloadPrompt = ''
  const payload = source.payloadJson ?? source.payload_json
  if (typeof payload === 'string' && payload.trim() !== '') {
    try {
      const parsed = JSON.parse(payload) as unknown
      if (parsed && typeof parsed === 'object') {
        payloadPrompt = optionalString(parsed as UnknownRecord, 'prompt', 'instruction', 'task', 'description') ?? ''
      }
    } catch {
      // An opaque scheduler payload is allowed; the agent layer may decode it.
    }
  }
  return {
    id,
    title: optionalString(source, 'title', 'name', 'label') ?? 'Без названия',
    prompt: optionalString(source, 'prompt', 'instruction', 'task', 'description') ?? payloadPrompt,
    type,
    runAt,
    intervalSeconds,
    expression,
    timezone: optionalString(source, 'timezone', 'timeZone', 'time_zone') ?? (Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'),
    misfirePolicy: normalizeMisfirePolicy(source.misfirePolicy ?? source.misfire_policy ?? source.misfire),
    enabled,
    status,
    nextRunAt: optionalString(source, 'nextRunAt', 'next_run_at', 'nextRun'),
    lastRunAt: optionalString(source, 'lastRunAt', 'last_run_at', 'lastRun'),
    deliveryChannel: normalizeDeliveryChannel(source.deliveryChannel ?? source.delivery_channel ?? source.channel),
    budget,
    createdAt: optionalString(source, 'createdAt', 'created_at'),
    updatedAt: optionalString(source, 'updatedAt', 'updated_at'),
    lastError: optionalString(source, 'lastError', 'last_error', 'error'),
  }
}

function normalizeScheduleList(value: unknown): Schedule[] {
  const rawItems = Array.isArray(value)
    ? value
    : value && typeof value === 'object'
      ? ((value as UnknownRecord).items ?? (value as UnknownRecord).schedules ?? (value as UnknownRecord).results)
      : undefined
  return Array.isArray(rawItems)
    ? rawItems.map(normalizeSchedule).filter((item): item is Schedule => Boolean(item))
    : []
}

function normalizeJobRunStatus(value: unknown): JobRunStatus {
  const status = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (status === 'queued' || status === 'pending' || status === 'scheduled') return 'queued'
  if (status === 'running' || status === 'started' || status === 'active') return 'running'
  if (status === 'completed' || status === 'complete' || status === 'success' || status === 'succeeded') return 'completed'
  if (status === 'failed' || status === 'error' || status === 'failure') return 'failed'
  if (status === 'cancelled' || status === 'canceled' || status === 'aborted') return 'cancelled'
  if (status === 'skipped' || status === 'misfired') return 'skipped'
  return 'unknown'
}

function normalizeTriggeredBy(value: unknown): JobRun['triggeredBy'] {
  const trigger = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (trigger === 'schedule' || trigger === 'scheduled' || trigger === 'cron') return 'schedule'
  if (trigger === 'manual' || trigger === 'user') return 'manual'
  if (trigger === 'recovery' || trigger === 'restart' || trigger === 'misfire') return 'recovery'
  return 'unknown'
}

function normalizeJobRun(value: unknown): JobRun | undefined {
  if (!value || typeof value !== 'object') return undefined
  const rawValue = value as UnknownRecord
  const source = rawValue.run && typeof rawValue.run === 'object' ? rawValue.run as UnknownRecord : rawValue
  const id = optionalString(source, 'id', 'runId', 'run_id', 'jobRunId', 'job_run_id')
  const scheduleId = optionalString(source, 'scheduleId', 'schedule_id')
  if (!id || !scheduleId) return undefined
  return {
    id,
    scheduleId,
    scheduleTitle: optionalString(source, 'scheduleTitle', 'schedule_title', 'title'),
    status: normalizeJobRunStatus(source.status ?? source.state),
    attempt: Math.max(1, Math.round(optionalNumber(source, 'attempt', 'attemptNumber', 'attempt_number') ?? 1)),
    startedAt: optionalString(source, 'startedAt', 'started_at'),
    finishedAt: optionalString(source, 'finishedAt', 'finished_at', 'completedAt', 'completed_at'),
    durationMs: optionalNumber(source, 'durationMs', 'duration_ms', 'elapsedMs', 'elapsed_ms'),
    error: optionalString(source, 'error', 'lastError', 'last_error'),
    summary: optionalString(source, 'summary', 'result', 'resultRef', 'result_ref', 'message'),
    triggeredBy: normalizeTriggeredBy(source.triggeredBy ?? source.triggered_by ?? source.trigger),
  }
}

function normalizeJobRunList(value: unknown): JobRun[] {
  const rawItems = Array.isArray(value)
    ? value
    : value && typeof value === 'object'
      ? ((value as UnknownRecord).items ?? (value as UnknownRecord).runs ?? (value as UnknownRecord).jobRuns ?? (value as UnknownRecord).job_runs ?? (value as UnknownRecord).results)
      : undefined
  return Array.isArray(rawItems)
    ? rawItems.map(normalizeJobRun).filter((item): item is JobRun => Boolean(item))
    : []
}

function normalizeActivityType(value: unknown): ActivityType {
  const type = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (type === 'job' || type === 'job_run' || type === 'schedule' || type === 'scheduler') return 'job'
  if (type === 'proactive' || type === 'proactivity' || type === 'notification') return 'proactive'
  if (type === 'reflection' || type === 'self_reflection') return 'reflection'
  if (type === 'memory' || type === 'memory_write') return 'memory'
  if (type === 'system' || type === 'audit') return 'system'
  return 'unknown'
}

function normalizeActivityStatus(value: unknown): ActivityStatus {
  const status = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (status === 'queued' || status === 'pending') return 'queued'
  if (status === 'running' || status === 'started' || status === 'active') return 'running'
  if (status === 'completed' || status === 'complete' || status === 'success' || status === 'succeeded') return 'completed'
  if (status === 'failed' || status === 'error' || status === 'failure') return 'failed'
  if (status === 'cancelled' || status === 'canceled' || status === 'aborted') return 'cancelled'
  if (status === 'skipped' || status === 'misfired') return 'skipped'
  if (status === 'blocked' || status === 'denied' || status === 'suppressed') return 'blocked'
  if (status === 'info' || status === 'informational') return 'info'
  return 'unknown'
}

function normalizeActivity(value: unknown): ActivityEvent | undefined {
  if (!value || typeof value !== 'object') return undefined
  const rawValue = value as UnknownRecord
  const source = rawValue.event && typeof rawValue.event === 'object' ? rawValue.event as UnknownRecord : rawValue
  const id = optionalString(source, 'id', 'eventId', 'event_id', 'auditId', 'audit_id')
  if (!id) return undefined
  return {
    id,
    type: normalizeActivityType(source.type ?? source.activityType ?? source.activity_type ?? source.category),
    status: normalizeActivityStatus(source.status ?? source.state ?? source.resultStatus ?? source.result_status),
    title: optionalString(source, 'title', 'name', 'action', 'summary') ?? 'Событие Yuri',
    detail: optionalString(source, 'detail', 'description', 'message', 'result'),
    source: optionalString(source, 'source', 'actor', 'origin'),
    scheduleId: optionalString(source, 'scheduleId', 'schedule_id'),
    runId: optionalString(source, 'runId', 'run_id', 'jobRunId', 'job_run_id'),
    createdAt: optionalString(source, 'createdAt', 'created_at', 'timestamp', 'occurredAt', 'occurred_at') ?? nowIso(),
    durationMs: optionalNumber(source, 'durationMs', 'duration_ms', 'elapsedMs', 'elapsed_ms'),
    reason: optionalString(source, 'reason', 'why'),
    provenance: optionalString(source, 'provenance', 'originDetail', 'origin_detail'),
  }
}

function normalizeActivityList(value: unknown): ActivityEvent[] {
  const rawItems = Array.isArray(value)
    ? value
    : value && typeof value === 'object'
      ? ((value as UnknownRecord).items ?? (value as UnknownRecord).events ?? (value as UnknownRecord).activities ?? (value as UnknownRecord).results)
      : undefined
  return Array.isArray(rawItems)
    ? rawItems.map(normalizeActivity).filter((item): item is ActivityEvent => Boolean(item))
    : []
}

function scheduleWire(input: ScheduleInput): UnknownRecord {
  const payloadJson = JSON.stringify({ prompt: input.prompt })
  return {
    ...input,
    name: input.title,
    kind: input.type,
    startAt: input.runAt,
    start_at: input.runAt,
    payloadJson,
    payload_json: payloadJson,
    scheduleType: input.type,
    schedule_type: input.type,
    run_at: input.runAt,
    interval_seconds: input.intervalSeconds,
    cronExpression: input.expression,
    cron_expression: input.expression,
    misfire_policy: input.misfirePolicy,
    delivery_channel: input.deliveryChannel,
  }
}

function proactivityWire(input: ProactivitySettings): UnknownRecord {
  return {
    ...input,
    globalEnabled: input.enabled,
    global_enabled: input.enabled,
    quiet_hours_enabled: input.quietHoursEnabled,
    quiet_hours_start: input.quietHoursStart,
    quiet_hours_end: input.quietHoursEnd,
    daily_limit: input.dailyLimit,
    cooldown_minutes: input.cooldownMinutes,
    allow_local_notifications: input.allowLocalNotifications,
  }
}

function normalizeProactivitySettings(value: unknown): ProactivitySettings {
  if (!value || typeof value !== 'object') return { ...defaultProactivitySettings }
  const rawValue = value as UnknownRecord
  const source = rawValue.settings && typeof rawValue.settings === 'object' ? rawValue.settings as UnknownRecord : rawValue
  const timezone = optionalString(source, 'timezone', 'timeZone', 'time_zone') ?? defaultProactivitySettings.timezone
  const dailyLimit = optionalNumber(source, 'dailyLimit', 'daily_limit', 'maxPerDay', 'max_per_day')
  const cooldownMinutes = optionalNumber(source, 'cooldownMinutes', 'cooldown_minutes', 'cooldown')
  return {
    enabled: source.enabled === undefined && source.globalEnabled === undefined && source.global_enabled === undefined
      ? defaultProactivitySettings.enabled
      : Boolean(source.enabled ?? source.globalEnabled ?? source.global_enabled),
    quietHoursEnabled: source.quietHoursEnabled === undefined && source.quiet_hours_enabled === undefined && source.quietHours === undefined
      ? defaultProactivitySettings.quietHoursEnabled
      : Boolean(source.quietHoursEnabled ?? source.quiet_hours_enabled ?? source.quietHours),
    quietHoursStart: optionalString(source, 'quietHoursStart', 'quiet_hours_start', 'quietStart', 'quiet_start') ?? defaultProactivitySettings.quietHoursStart,
    quietHoursEnd: optionalString(source, 'quietHoursEnd', 'quiet_hours_end', 'quietEnd', 'quiet_end') ?? defaultProactivitySettings.quietHoursEnd,
    timezone,
    dailyLimit: Math.max(0, Math.round(dailyLimit ?? defaultProactivitySettings.dailyLimit)),
    cooldownMinutes: Math.max(0, Math.round(cooldownMinutes ?? defaultProactivitySettings.cooldownMinutes)),
    allowLocalNotifications: source.allowLocalNotifications === undefined && source.allow_local_notifications === undefined && source.notificationsEnabled === undefined
      ? defaultProactivitySettings.allowLocalNotifications
      : Boolean(source.allowLocalNotifications ?? source.allow_local_notifications ?? source.notificationsEnabled),
  }
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

function starterSchedule(): Schedule {
  const now = Date.now()
  return {
    id: 'schedule-daily-briefing',
    title: 'Утренняя сводка',
    prompt: 'Собери краткую сводку важных задач и событий на сегодня.',
    type: 'cron',
    expression: '0 9 * * 1-5',
    timezone: 'Europe/Moscow',
    misfirePolicy: 'run_once',
    enabled: true,
    status: 'active',
    nextRunAt: new Date(now + 1000 * 60 * 90).toISOString(),
    lastRunAt: new Date(now - 1000 * 60 * 60 * 18).toISOString(),
    deliveryChannel: 'notification',
    budget: { maxDurationSeconds: 180, maxTokens: 1800, maxToolCalls: 8 },
    createdAt: new Date(now - 1000 * 60 * 60 * 24 * 6).toISOString(),
    updatedAt: new Date(now - 1000 * 60 * 30).toISOString(),
  }
}

function starterJobRuns(): JobRun[] {
  const now = Date.now()
  return [
    {
      id: 'job-run-briefing-1',
      scheduleId: 'schedule-daily-briefing',
      scheduleTitle: 'Утренняя сводка',
      status: 'completed',
      attempt: 1,
      startedAt: new Date(now - 1000 * 60 * 60 * 18 - 1000 * 34).toISOString(),
      finishedAt: new Date(now - 1000 * 60 * 60 * 18).toISOString(),
      durationMs: 34000,
      summary: 'Сводка подготовлена и показана в приложении.',
      triggeredBy: 'schedule',
    },
    {
      id: 'job-run-briefing-2',
      scheduleId: 'schedule-daily-briefing',
      scheduleTitle: 'Утренняя сводка',
      status: 'skipped',
      attempt: 1,
      startedAt: new Date(now - 1000 * 60 * 60 * 42).toISOString(),
      finishedAt: new Date(now - 1000 * 60 * 60 * 42).toISOString(),
      durationMs: 0,
      summary: 'Пропущено: приложение было закрыто в quiet hours.',
      triggeredBy: 'recovery',
    },
  ]
}

function starterActivity(): ActivityEvent[] {
  const now = Date.now()
  return [
    {
      id: 'activity-job-briefing-1',
      type: 'job',
      status: 'completed',
      title: 'Утренняя сводка завершена',
      detail: 'Сводка подготовлена и показана в приложении.',
      source: 'scheduler',
      scheduleId: 'schedule-daily-briefing',
      runId: 'job-run-briefing-1',
      createdAt: new Date(now - 1000 * 60 * 60 * 18).toISOString(),
      durationMs: 34000,
      provenance: 'schedule-daily-briefing',
    },
    {
      id: 'activity-system-ready',
      type: 'system',
      status: 'info',
      title: 'Фоновый worker готов',
      detail: 'Durable scheduler восстановил расписания после запуска Yuri.',
      source: 'application',
      createdAt: new Date(now - 1000 * 60 * 42).toISOString(),
      provenance: 'startup recovery',
    },
    {
      id: 'activity-proactive-preview',
      type: 'proactive',
      status: 'skipped',
      title: 'Уведомление отложено',
      detail: 'Триггер попал в quiet hours и будет пересмотрен позднее.',
      source: 'proactivity policy',
      createdAt: new Date(now - 1000 * 60 * 12).toISOString(),
      reason: 'quiet hours',
      provenance: 'local policy gate',
    },
  ]
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
  private onboarding: OnboardingState = { ...defaultOnboardingState }
  private allowedDirectories: string[] = []
  private schedules: Schedule[] = [starterSchedule()]
  private jobRuns: JobRun[] = starterJobRuns()
  private activity: ActivityEvent[] = starterActivity()
  private proactivity: ProactivitySettings = { ...defaultProactivitySettings }
  private personality: PersonalitySnapshot = createStarterPersonalitySnapshot()
  private readonly personalitySeed: PersonalitySnapshot = createStarterPersonalitySnapshot()

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
    if (settings.kind === 'antigravity') {
      throw new Error('Antigravity OAuth недоступен без официального integration contract.')
    }
    this.provider = {
      ...this.provider,
      settings: { ...settings, apiKeyConfigured: settings.apiKeyConfigured || Boolean(apiKey?.trim()) },
    }
  }

  async testProvider(settings: ProviderSettings): Promise<ProviderTestResult> {
    await sleep(280)
    if (settings.kind === 'antigravity') {
      return {
        ok: false,
        message: 'Antigravity OAuth недоступен: официальный разрешённый contract для стороннего приложения отсутствует.',
        errorCode: 'unsupported_auth_mode',
        alternative: 'openai-compatible-api-key',
      }
    }
    if (settings.kind === 'codex-app-server') {
      if (!this.provider.codex.connected) return { ok: false, message: 'Сначала выполните OAuth-вход.' }
      this.onboarding = { ...this.onboarding, completed: true, providerTested: true, completedAt: nowIso() }
      return { ok: true, message: 'Codex App Server отвечает.' }
    }
    if (!settings.baseUrl.trim() || !settings.model.trim()) return { ok: false, message: 'Укажите Base URL и модель.' }
    this.onboarding = { ...this.onboarding, completed: true, providerTested: true, completedAt: nowIso() }
    return { ok: true, message: 'Endpoint доступен для потокового запроса.' }
  }

  async getOnboardingState(): Promise<OnboardingState> {
    return { ...this.onboarding }
  }

  async completeOnboarding(settings: ProviderSettings, apiKey?: string): Promise<OnboardingResult> {
    if (settings.kind === 'antigravity') {
      const probe = await this.testProvider(settings)
      return { ...probe, state: await this.getOnboardingState() }
    }
    await this.saveProviderSettings(settings, apiKey)
    const probe = await this.testProvider(settings)
    const state = await this.getOnboardingState()
    return { ...probe, state }
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

  async createEncryptedBackup(input: EncryptedBackupInput): Promise<EncryptedBackupInfo | undefined> {
    if (input.passphrase.length < 12) throw new Error('Пароль backup должен содержать не менее 12 символов.')
    await sleep(220)
    return {
      path: input.path || '/tmp/yuri-preview.yuribackup',
      createdAt: nowIso(),
      sizeBytes: 4096,
      blobCount: input.includeBlobs ? 2 : 0,
      hasConfig: true,
    }
  }

  async validateEncryptedBackup(input: EncryptedBackupInspectInput): Promise<EncryptedBackupInfo | undefined> {
    if (input.passphrase.length < 12) throw new Error('Пароль backup должен содержать не менее 12 символов.')
    return { path: input.path || '/tmp/yuri-preview.yuribackup', createdAt: nowIso(), sizeBytes: 4096, blobCount: 0, hasConfig: true }
  }

  async restoreEncryptedBackup(input: EncryptedBackupRestoreInput): Promise<EncryptedBackupInfo | undefined> {
    const inspected = await this.validateEncryptedBackup(input)
    return inspected ? { ...inspected, restoredTo: input.targetDirectory || '/tmp/yuri-restored-preview' } : undefined
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

  async listPlugins(): Promise<PluginRecord[]> {
    // Keep the offline preview honest: plugins are installed by the local
    // process supervisor and are not invented by the browser mock.
    return []
  }

  async inspectPluginPackage(path: string, _devMode = false): Promise<PluginPackageInspection> {
    return emptyPluginInspection(path, 'Проверка пакетов доступна после запуска plugin host.')
  }

  async installPlugin(_request: PluginInstallRequest): Promise<PluginRecord | undefined> {
    return undefined
  }

  async enablePlugin(_pluginId: string): Promise<PluginRecord | undefined> {
    return undefined
  }

  async disablePlugin(_pluginId: string): Promise<PluginRecord | undefined> {
    return undefined
  }

  async uninstallPlugin(_pluginId: string): Promise<void> {
    // There is no local preview plugin to remove.
  }

  async startPlugin(_pluginId: string): Promise<PluginRecord | undefined> {
    return undefined
  }

  async stopPlugin(_pluginId: string): Promise<PluginRecord | undefined> {
    return undefined
  }

  async listSchedules(): Promise<Schedule[]> {
    return this.schedules.map((schedule) => ({ ...schedule, budget: schedule.budget ? { ...schedule.budget } : undefined }))
  }

  async createSchedule(input: ScheduleInput): Promise<Schedule | undefined> {
    const title = input.title.trim()
    const prompt = input.prompt.trim()
    if (!title || !prompt) throw new Error('Укажите название и инструкцию задачи.')
    const now = nowIso()
    const schedule: Schedule = {
      id: makeId('schedule'),
      title,
      prompt,
      type: input.type,
      runAt: input.runAt,
      intervalSeconds: input.intervalSeconds,
      expression: input.expression,
      timezone: input.timezone,
      misfirePolicy: input.misfirePolicy,
      enabled: input.enabled ?? true,
      status: input.enabled === false ? 'paused' : 'active',
      nextRunAt: input.runAt ?? new Date(Date.now() + Math.max(60, input.intervalSeconds ?? 3600) * 1000).toISOString(),
      deliveryChannel: input.deliveryChannel ?? 'in_app',
      budget: input.budget ? { ...input.budget } : undefined,
      createdAt: now,
      updatedAt: now,
    }
    this.schedules = [schedule, ...this.schedules]
    this.appendActivity({
      id: makeId('activity'),
      type: 'job',
      status: 'info',
      title: `Создана задача «${title}»`,
      detail: 'Расписание добавлено в локальный worker.',
      source: 'scheduler',
      scheduleId: schedule.id,
      createdAt: now,
      provenance: 'user configuration',
    })
    return { ...schedule, budget: schedule.budget ? { ...schedule.budget } : undefined }
  }

  async updateSchedule(input: ScheduleInput): Promise<Schedule | undefined> {
    if (!input.id) return undefined
    const current = this.schedules.find((schedule) => schedule.id === input.id)
    if (!current) return undefined
    const now = nowIso()
    const enabled = input.enabled ?? current.enabled
    const next: Schedule = {
      ...current,
      title: input.title.trim() || current.title,
      prompt: input.prompt.trim() || current.prompt,
      type: input.type,
      runAt: input.runAt,
      intervalSeconds: input.intervalSeconds,
      expression: input.expression,
      timezone: input.timezone,
      misfirePolicy: input.misfirePolicy,
      enabled,
      status: enabled ? 'active' : 'paused',
      deliveryChannel: input.deliveryChannel ?? current.deliveryChannel,
      budget: input.budget ? { ...input.budget } : undefined,
      updatedAt: now,
    }
    this.schedules = this.schedules.map((schedule) => schedule.id === next.id ? next : schedule)
    return { ...next, budget: next.budget ? { ...next.budget } : undefined }
  }

  async setScheduleEnabled(scheduleId: string, enabled: boolean): Promise<Schedule | undefined> {
    const current = this.schedules.find((schedule) => schedule.id === scheduleId)
    if (!current) return undefined
    const next = { ...current, enabled, status: enabled ? 'active' as const : 'paused' as const, updatedAt: nowIso() }
    this.schedules = this.schedules.map((schedule) => schedule.id === scheduleId ? next : schedule)
    this.appendActivity({
      id: makeId('activity'),
      type: 'job',
      status: 'info',
      title: enabled ? `Возобновлена задача «${current.title}»` : `Поставлена на паузу «${current.title}»`,
      source: 'scheduler',
      scheduleId,
      createdAt: nowIso(),
      provenance: 'user action',
    })
    return { ...next, budget: next.budget ? { ...next.budget } : undefined }
  }

  async runScheduleNow(scheduleId: string): Promise<JobRun | undefined> {
    const schedule = this.schedules.find((item) => item.id === scheduleId)
    if (!schedule) return undefined
    const startedAt = nowIso()
    const run: JobRun = {
      id: makeId('job-run'),
      scheduleId,
      scheduleTitle: schedule.title,
      status: 'running',
      attempt: 1,
      startedAt,
      triggeredBy: 'manual',
    }
    this.jobRuns = [run, ...this.jobRuns]
    await sleep(180)
    const finishedAt = nowIso()
    const completed: JobRun = {
      ...run,
      status: 'completed',
      finishedAt,
      durationMs: Math.max(1, new Date(finishedAt).getTime() - new Date(startedAt).getTime()),
      summary: 'Задача выполнена в mock режиме.',
    }
    this.jobRuns = this.jobRuns.map((item) => item.id === run.id ? completed : item)
    this.schedules = this.schedules.map((item) => item.id === scheduleId ? { ...item, lastRunAt: finishedAt, updatedAt: finishedAt } : item)
    this.appendActivity({
      id: makeId('activity'),
      type: 'job',
      status: 'completed',
      title: `Задача «${schedule.title}» выполнена`,
      detail: completed.summary,
      source: 'scheduler',
      scheduleId,
      runId: completed.id,
      createdAt: finishedAt,
      durationMs: completed.durationMs,
      provenance: 'manual run',
    })
    return { ...completed }
  }

  async cancelJobRun(runId: string): Promise<JobRun | undefined> {
    const current = this.jobRuns.find((run) => run.id === runId)
    if (!current || (current.status !== 'queued' && current.status !== 'running')) return undefined
    const finishedAt = nowIso()
    const cancelled: JobRun = {
      ...current,
      status: 'cancelled',
      finishedAt,
      durationMs: current.startedAt ? Math.max(0, new Date(finishedAt).getTime() - new Date(current.startedAt).getTime()) : 0,
      summary: 'Запуск остановлен пользователем.',
    }
    this.jobRuns = this.jobRuns.map((run) => run.id === runId ? cancelled : run)
    this.appendActivity({
      id: makeId('activity'),
      type: 'job',
      status: 'cancelled',
      title: 'Фоновый запуск остановлен',
      detail: cancelled.summary,
      source: 'scheduler',
      scheduleId: cancelled.scheduleId,
      runId: cancelled.id,
      createdAt: finishedAt,
      provenance: 'user action',
    })
    return { ...cancelled }
  }

  async deleteSchedule(scheduleId: string): Promise<void> {
    const schedule = this.schedules.find((item) => item.id === scheduleId)
    this.schedules = this.schedules.filter((item) => item.id !== scheduleId)
    if (schedule) {
      this.appendActivity({
        id: makeId('activity'),
        type: 'job',
        status: 'info',
        title: `Удалена задача «${schedule.title}»`,
        source: 'scheduler',
        scheduleId,
        createdAt: nowIso(),
        provenance: 'user action',
      })
    }
  }

  async listJobRuns(options: JobRunListOptions = {}): Promise<JobRun[]> {
    const limit = Math.max(1, Math.min(100, Math.round(options.limit ?? 30)))
    return this.jobRuns
      .filter((run) => !options.scheduleId || run.scheduleId === options.scheduleId)
      .slice(0, limit)
      .map((run) => ({ ...run }))
  }

  async getProactivitySettings(): Promise<ProactivitySettings> {
    return { ...this.proactivity }
  }

  async saveProactivitySettings(input: ProactivitySettings): Promise<void> {
    this.proactivity = {
      ...input,
      dailyLimit: Math.max(0, Math.round(input.dailyLimit)),
      cooldownMinutes: Math.max(0, Math.round(input.cooldownMinutes)),
    }
    this.appendActivity({
      id: makeId('activity'),
      type: 'system',
      status: 'info',
      title: 'Обновлена политика проактивности',
      detail: this.proactivity.enabled ? 'Проактивные уведомления разрешены.' : 'Проактивные уведомления выключены.',
      source: 'proactivity policy',
      createdAt: nowIso(),
      provenance: 'user configuration',
    })
  }

  async listActivity(options: ActivityListOptions = {}): Promise<ActivityEvent[]> {
    const limit = Math.max(1, Math.min(100, Math.round(options.limit ?? 50)))
    return this.activity
      .filter((event) => !options.type || options.type === 'all' || event.type === options.type)
      .filter((event) => !options.status || options.status === 'all' || event.status === options.status)
      .slice(0, limit)
      .map((event) => ({ ...event }))
  }

  async getPersonaSnapshot(): Promise<PersonalitySnapshot> {
    return clonePersonalitySnapshot(this.personality)
  }

  async setPersonaAutoEvolution(enabled: boolean): Promise<PersonalitySnapshot | undefined> {
    this.personality = { ...this.personality, autoEvolution: Boolean(enabled) }
    return this.getPersonaSnapshot()
  }

  async setPersonaTraitPinned(traitId: string, pinned: boolean): Promise<PersonalitySnapshot | undefined> {
    const trait = this.personality.traits.find((item) => item.id === traitId)
    if (!trait) return undefined
    const pinnedTraits = new Set(this.personality.pinnedTraits)
    if (pinned) pinnedTraits.add(traitId)
    else pinnedTraits.delete(traitId)
    this.personality = {
      ...this.personality,
      pinnedTraits: [...pinnedTraits],
      traits: this.personality.traits.map((item) => item.id === traitId ? { ...item, pinned } : { ...item }),
    }
    return this.getPersonaSnapshot()
  }

  async rollbackPersona(versionId: string): Promise<PersonalitySnapshot | undefined> {
    const version = this.personality.versions.find((item) => item.id === versionId || String(item.version) === versionId)
    if (!version) return undefined
    this.personality = {
      ...this.personality,
      currentVersion: version.version,
      currentVersionId: version.id,
      traits: version.traits.map((trait) => ({ ...trait, pinned: this.personality.pinnedTraits.includes(trait.id) })),
      lastReflectionAt: nowIso(),
    }
    return this.getPersonaSnapshot()
  }

  async resetPersona(): Promise<PersonalitySnapshot | undefined> {
    const reset = clonePersonalitySnapshot(this.personalitySeed)
    // Keep the previous versions visible in the local preview: reset is an
    // append-only state change, not a deletion of the persona history.
    const resetVersion: PersonaVersion = {
      ...reset.versions[0],
      id: makeId('persona-reset'),
      reason: 'Сброс к исходному identity seed.',
      createdAt: nowIso(),
    }
    this.personality = {
      ...reset,
      versions: [...this.personality.versions, resetVersion],
      currentVersion: resetVersion.version,
      currentVersionId: resetVersion.id,
      autoEvolution: this.personality.autoEvolution,
      lastReflectionAt: nowIso(),
    }
    return this.getPersonaSnapshot()
  }

  async getPersonalitySnapshot(): Promise<PersonalitySnapshot> {
    return this.getPersonaSnapshot()
  }

  async setPersonalityAutoEvolution(enabled: boolean): Promise<PersonalitySnapshot | undefined> {
    return this.setPersonaAutoEvolution(enabled)
  }

  async setPersonalityTraitPinned(traitId: string, pinned: boolean): Promise<PersonalitySnapshot | undefined> {
    return this.setPersonaTraitPinned(traitId, pinned)
  }

  async rollbackPersonality(versionId: string): Promise<PersonalitySnapshot | undefined> {
    return this.rollbackPersona(versionId)
  }

  async resetPersonality(): Promise<PersonalitySnapshot | undefined> {
    return this.resetPersona()
  }

  private appendActivity(event: ActivityEvent): void {
    this.activity = [event, ...this.activity].slice(0, 100)
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
    const providerList = Array.isArray(providers) ? providers : []
    const enabledProvider = providerList.find((item): item is UnknownRecord => Boolean(item && typeof item === 'object' && (item as UnknownRecord).enabled))
    const codexConfigured = providerList.some((item) => Boolean(item && typeof item === 'object' && ((item as UnknownRecord).kind === 'codex-app-server' || (item as UnknownRecord).type === 'codex-app-server')))
    const accountResult = codexConfigured ? await callBridgeSafe<unknown>(['CodexAccount']) : undefined
    const account = accountResult && typeof accountResult === 'object' ? normalizeCodexAccount(accountResult as UnknownRecord) : undefined
    const limits = codexConfigured ? normalizeUsageLimits(await callBridgeSafe<unknown>(['CodexRateLimits'])) : undefined
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
    if (settings.kind === 'antigravity') {
      throw new Error('Antigravity OAuth недоступен без официального integration contract.')
    }
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

  async testProvider(settings: ProviderSettings): Promise<ProviderTestResult> {
    return (await callBridge<ProviderTestResult>(['TestProvider', 'ProbeProvider'], [settings])) ?? { ok: false, message: 'Backend не вернул результат проверки.' }
  }

  async getOnboardingState(): Promise<OnboardingState> {
    return normalizeOnboardingState(await callBridgeSafe<unknown>(['GetOnboardingState', 'GetFirstRunState', 'OnboardingState']))
  }

  async completeOnboarding(settings: ProviderSettings, apiKey?: string): Promise<OnboardingResult> {
    const payload: UnknownRecord = {
      settings: onboardingSettingsWire(settings),
    }
    if (apiKey?.trim()) payload.apiKey = apiKey

    const completeMethod = findBridgeMethod(['CompleteOnboarding', 'CompleteFirstRun'])
    if (completeMethod) {
      const result = await completeMethod(payload)
      const state = await this.getOnboardingState()
      const normalized = normalizeOnboardingResult(result, state)
      return normalized.state.completed && normalized.state.providerTested
        ? normalized
        : { ...normalized, ok: false, message: normalized.message || 'Onboarding state не сохранён.', state }
    }

    if (settings.kind === 'antigravity') {
      const probe = await this.testProvider(settings)
      return { ...probe, state: await this.getOnboardingState() }
    }

    // Older bridges can still perform the provider save and probe. They must
    // persist completion from TestProvider itself; the renderer has no setter.
    await this.saveProviderSettings(settings, apiKey)
    const probe = await this.testProvider(settings)
    const state = await this.getOnboardingState()
    if (!probe.ok) return { ...probe, state }
    if (!state.completed || !state.providerTested) {
      return {
        ok: false,
        message: 'Провайдер отвечает, но onboarding state не сохранён. Повторите попытку после обновления backend.',
        state,
      }
    }
    return { ...probe, state }
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

  async createEncryptedBackup(input: EncryptedBackupInput): Promise<EncryptedBackupInfo | undefined> {
    return normalizeEncryptedBackup(await callBridge<unknown>(['CreateEncryptedBackup'], [input]))
  }

  async validateEncryptedBackup(input: EncryptedBackupInspectInput): Promise<EncryptedBackupInfo | undefined> {
    return normalizeEncryptedBackup(await callBridge<unknown>(['ValidateEncryptedBackup'], [input]))
  }

  async restoreEncryptedBackup(input: EncryptedBackupRestoreInput): Promise<EncryptedBackupInfo | undefined> {
    return normalizeEncryptedBackup(await callBridge<unknown>(['RestoreEncryptedBackup'], [input]))
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

  async listPlugins(): Promise<PluginRecord[]> {
    return normalizePluginList(await callBridge<unknown>(['ListPlugins']))
  }

  async inspectPluginPackage(path: string, devMode = false): Promise<PluginPackageInspection> {
    const result = await callBridge<unknown>(['InspectPluginPackage', 'InspectPlugin'], [{ path, devMode, allowUnsigned: devMode }])
    return normalizePluginInspection(result, path, devMode)
  }

  async installPlugin(request: PluginInstallRequest): Promise<PluginRecord | undefined> {
    const result = await callBridge<unknown>(['InstallPlugin', 'InstallPluginPackage'], [{ ...request, allowUnsigned: request.devMode }])
    return normalizePlugin(result, request.devMode)
  }

  async enablePlugin(pluginId: string): Promise<PluginRecord | undefined> {
    return normalizePlugin(await callBridge<unknown>(['EnablePlugin'], [{ id: pluginId, pluginId }]))
  }

  async disablePlugin(pluginId: string): Promise<PluginRecord | undefined> {
    return normalizePlugin(await callBridge<unknown>(['DisablePlugin'], [{ id: pluginId, pluginId }]))
  }

  async uninstallPlugin(pluginId: string): Promise<void> {
    await callBridge(['UninstallPlugin'], [{ id: pluginId, pluginId }])
  }

  async startPlugin(pluginId: string): Promise<PluginRecord | undefined> {
    return normalizePlugin(await callBridge<unknown>(['StartPlugin'], [{ id: pluginId, pluginId }]))
  }

  async stopPlugin(pluginId: string): Promise<PluginRecord | undefined> {
    return normalizePlugin(await callBridge<unknown>(['StopPlugin'], [{ id: pluginId, pluginId }]))
  }

  async listSchedules(): Promise<Schedule[]> {
    return normalizeScheduleList(await callBridge<unknown>(['ListSchedules']))
  }

  async createSchedule(input: ScheduleInput): Promise<Schedule | undefined> {
    return normalizeSchedule(await callBridge<unknown>(['CreateSchedule'], [scheduleWire(input)]))
  }

  async updateSchedule(input: ScheduleInput): Promise<Schedule | undefined> {
    return normalizeSchedule(await callBridge<unknown>(['UpdateSchedule'], [scheduleWire(input)]))
  }

  async setScheduleEnabled(scheduleId: string, enabled: boolean): Promise<Schedule | undefined> {
    return normalizeSchedule(await callBridge<unknown>(['SetScheduleEnabled'], [{ id: scheduleId, scheduleId, enabled }]))
  }

  async runScheduleNow(scheduleId: string): Promise<JobRun | undefined> {
    return normalizeJobRun(await callBridge<unknown>(['RunScheduleNow'], [{ id: scheduleId, scheduleId }]))
  }

  async cancelJobRun(runId: string): Promise<JobRun | undefined> {
    return normalizeJobRun(await callBridge<unknown>(['CancelJobRun'], [{ id: runId, runId }]))
  }

  async deleteSchedule(scheduleId: string): Promise<void> {
    await callBridge(['DeleteSchedule'], [{ id: scheduleId, scheduleId }])
  }

  async listJobRuns(options: JobRunListOptions = {}): Promise<JobRun[]> {
    return normalizeJobRunList(await callBridge<unknown>(['ListJobRuns'], [options]))
  }

  async getProactivitySettings(): Promise<ProactivitySettings> {
    return normalizeProactivitySettings(await callBridge<unknown>(['GetProactivitySettings']))
  }

  async saveProactivitySettings(input: ProactivitySettings): Promise<void> {
    await callBridge(['SaveProactivitySettings'], [proactivityWire(input)])
  }

  async listActivity(options: ActivityListOptions = {}): Promise<ActivityEvent[]> {
    return normalizeActivityList(await callBridge<unknown>(['ListActivity'], [options]))
  }

  async getPersonaSnapshot(): Promise<PersonalitySnapshot> {
    const result = await callBridge<unknown>([
      'GetPersonalitySnapshot',
      'GetPersonaSnapshot',
      'GetPersonality',
      'GetPersona',
      'GetRelationshipState',
    ])
    return normalizePersonalitySnapshot(result)
  }

  async setPersonaAutoEvolution(enabled: boolean): Promise<PersonalitySnapshot | undefined> {
    const result = await callBridge<unknown>([
      'SetPersonaAutoEvolution',
      'SetPersonalityAutoEvolution',
      'SavePersonaSettings',
    ], [{ enabled, autoEvolution: enabled, auto_evolution: enabled }])
    return result === undefined ? this.getPersonaSnapshot() : normalizePersonalitySnapshot(result)
  }

  async setPersonaTraitPinned(traitId: string, pinned: boolean): Promise<PersonalitySnapshot | undefined> {
    const result = await callBridge<unknown>([
      'SetPersonaTraitPinned',
      'SetPersonalityTraitPinned',
      'PinPersonaTrait',
    ], [{ id: traitId, traitId, trait_id: traitId, pinned, isPinned: pinned, is_pinned: pinned }])
    return result === undefined ? this.getPersonaSnapshot() : normalizePersonalitySnapshot(result)
  }

  async rollbackPersona(versionId: string): Promise<PersonalitySnapshot | undefined> {
    const result = await callBridge<unknown>([
      'RollbackPersona',
      'RollbackPersonality',
      'RollbackPersonaVersion',
    ], [{ id: versionId, versionId, version_id: versionId }])
    return result === undefined ? this.getPersonaSnapshot() : normalizePersonalitySnapshot(result)
  }

  async resetPersona(): Promise<PersonalitySnapshot | undefined> {
    const result = await callBridge<unknown>([
      'ResetPersona',
      'ResetPersonality',
      'ResetPersonaToSeed',
    ], [{}])
    return result === undefined ? this.getPersonaSnapshot() : normalizePersonalitySnapshot(result)
  }

  async getPersonalitySnapshot(): Promise<PersonalitySnapshot> {
    return this.getPersonaSnapshot()
  }

  async setPersonalityAutoEvolution(enabled: boolean): Promise<PersonalitySnapshot | undefined> {
    return this.setPersonaAutoEvolution(enabled)
  }

  async setPersonalityTraitPinned(traitId: string, pinned: boolean): Promise<PersonalitySnapshot | undefined> {
    return this.setPersonaTraitPinned(traitId, pinned)
  }

  async rollbackPersonality(versionId: string): Promise<PersonalitySnapshot | undefined> {
    return this.rollbackPersona(versionId)
  }

  async resetPersonality(): Promise<PersonalitySnapshot | undefined> {
    return this.resetPersona()
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
  client = findBridgeMethod(['ListConversations', 'GetConversations', 'SendMessage', 'StartChat', 'GetOnboardingState', 'GetFirstRunState', 'CompleteOnboarding', 'CompleteFirstRun'])
    ? new WailsYuriClient()
    : new MockYuriClient()
  return client
}

export function resetYuriClientForTests(): void {
  client = undefined
}
