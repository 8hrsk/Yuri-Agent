import type {
  ActivityEvent,
  ActivityListOptions,
  ApprovalDecision,
  AgentProfile,
  AgentProfileInput,
  ArchiveSearchRequest,
  ArchiveSearchResponse,
  ChatEvent,
  ChatAttachmentContent,
  ChatHistoryPage,
  ChatRequest,
  ChatTool,
  CodexAccount,
  CodexLogoutResult,
  CodexModel,
  Conversation,
  ConversationPageOptions,
  EncryptedBackupInfo,
  EncryptedBackupInput,
  EncryptedBackupInspectInput,
  EncryptedBackupRestoreInput,
  JobRun,
  JobRunListOptions,
  MemoryLifecycleState,
  MemoryListOptions,
  MemoryRecord,
  MemoryScope,
  MemoryUpdate,
  OnboardingResult,
  OnboardingState,
  PeerDialogue,
  PeerDialogueListOptions,
  PersonalitySnapshot,
  PluginEnableRequest,
  PluginInstallRequest,
  PluginPackageInspection,
  PluginRecord,
  ProactivitySettings,
  ProviderSettings,
  ProviderSnapshot,
  ProviderTestResult,
  RunResult,
  Schedule,
  ScheduleInput,
  UsageLimits,
  WebSearchSettings,
  YuriClient,
} from '../contracts'
import { normalizeAgentProfile } from '../agents'
import { pluginEnablePayload } from '../plugin-consent'
import { normalizePersonalitySnapshot } from '../personality'
import { callBridge, callBridgeSafe, findBridgeMethod, subscribeRuntimeEvent } from './bridge'
import { MockYuriClient } from './mock-client'
import { normalizeChatEvent, normalizeChatToolList } from './normalize-chat'
import { normalizeActivityList, normalizePeerDialogueList } from './normalize-collab'
import { normalizeChatHistoryPage, normalizeConversation, normalizeConversationList } from './normalize-conversation'
import { normalizeArchiveResponse, normalizeMemory, normalizeMemoryList } from './normalize-memory'
import { normalizePlugin, normalizePluginInspection, normalizePluginList } from './normalize-plugins'
import {
  normalizeJobRun,
  normalizeJobRunList,
  normalizeSchedule,
  normalizeScheduleList,
  scheduleWire,
} from './normalize-scheduler'
import { blobToBase64, makeId, normalizeBoolean, nowIso, optionalString } from './primitives'
import type { UnknownRecord } from './primitives'
import {
  defaultSettings,
  normalizeEncryptedBackup,
  normalizeOnboardingResult,
  normalizeOnboardingState,
  normalizeProactivitySettings,
  normalizeWebSearchSettings,
  onboardingSettingsWire,
  proactivityWire,
} from './settings'

/**
 * Chat run multiplexer.
 *
 * Every run used to open its own `yuri:chat` subscription and tear it down with
 * `EventsOff('yuri:chat')`, which removed the *other* runs' listeners too, and
 * no listener filtered by `runId`, so overlapping runs also crossed streams.
 * One long-lived bus subscription now fans events out to the run they belong
 * to.
 *
 * Runs cannot announce their id up front — Go mints it and only reveals it in
 * the call's return value, long after the first event has streamed in. So a
 * subscription claims the first run id it plausibly owns: the oldest unclaimed
 * subscription whose conversation matches (an empty conversation id is a
 * wildcard, because a brand-new conversation is created by the same call). Runs
 * start in the order they were subscribed, so oldest-first attributes correctly,
 * and once claimed, a run id routes to exactly one subscription.
 */
const chatEventName = 'yuri:chat'

interface ChatRunSubscription {
  conversationId: string
  runId?: string
  handler: (event: ChatEvent) => void
}

/** Insertion-ordered, which is what makes "oldest unclaimed" well defined. */
const chatRunSubscriptions = new Set<ChatRunSubscription>()
/**
 * Run ids whose subscription is gone. A late straggler must not be handed to an
 * unrelated run that happens to still be unclaimed. Bounded so a long session
 * cannot grow it without limit.
 */
const retiredChatRunIds: string[] = []
const retiredChatRunIdSet = new Set<string>()
const retiredChatRunIdLimit = 64
let chatBusUnsubscribe: (() => void) | undefined

function retireChatRunId(runId: string): void {
  if (retiredChatRunIdSet.has(runId)) return
  retiredChatRunIdSet.add(runId)
  retiredChatRunIds.push(runId)
  while (retiredChatRunIds.length > retiredChatRunIdLimit) {
    const evicted = retiredChatRunIds.shift()
    if (evicted !== undefined) retiredChatRunIdSet.delete(evicted)
  }
}

function routeChatEvent(event: ChatEvent): ChatRunSubscription | undefined {
  for (const subscription of chatRunSubscriptions) {
    if (subscription.runId === event.runId) return subscription
  }
  if (retiredChatRunIdSet.has(event.runId)) return undefined
  for (const subscription of chatRunSubscriptions) {
    if (subscription.runId !== undefined) continue
    if (subscription.conversationId && event.conversationId && subscription.conversationId !== event.conversationId) continue
    subscription.runId = event.runId
    return subscription
  }
  return undefined
}

function dispatchChatEvent(value: unknown): void {
  const event = normalizeChatEvent(value)
  if (!event) return
  routeChatEvent(event)?.handler(event)
}

/**
 * Subscribes one run to the chat bus. The returned handle releases only this
 * run's listener; the bus subscription itself is dropped when the last run
 * lets go.
 */
function subscribeChatRun(conversationId: string, handler: (event: ChatEvent) => void): { release: () => void } {
  const subscription: ChatRunSubscription = { conversationId, handler }
  chatRunSubscriptions.add(subscription)
  if (!chatBusUnsubscribe) chatBusUnsubscribe = subscribeRuntimeEvent(chatEventName, dispatchChatEvent)
  let released = false
  return {
    release: () => {
      if (released) return
      released = true
      chatRunSubscriptions.delete(subscription)
      if (subscription.runId !== undefined) retireChatRunId(subscription.runId)
      if (chatRunSubscriptions.size === 0) {
        chatBusUnsubscribe?.()
        chatBusUnsubscribe = undefined
      }
    },
  }
}

/**
 * Stable identity for a lifecycle event, used to suppress a replayed copy of an
 * event the live stream already delivered. Every field that Go varies between
 * two events of the same type is folded in; nothing is serialized, so the cost
 * is one join of short strings rather than two `JSON.stringify` passes.
 */
function chatEventKey(event: ChatEvent): string {
  const parts: string[] = [event.type, event.runId, event.conversationId ?? '', event.createdAt ?? '', event.timestamp ?? '']
  switch (event.type) {
    case 'assistant.delta':
      parts.push(event.messageId, event.delta)
      break
    case 'assistant.completed':
      parts.push(event.messageId)
      break
    case 'tool.started':
    case 'tool.updated':
      parts.push(event.toolCall.id, event.toolCall.status, event.toolCall.result ?? '')
      break
    case 'approval.required':
      parts.push(event.approval.id, event.approval.toolCallId)
      break
    case 'run.status':
      parts.push(event.status, event.label)
      break
    case 'run.completed':
      parts.push(event.status, event.error ?? '')
      break
    case 'trace.step':
      parts.push(event.step.id, event.step.status)
      break
    default:
      break
  }
  return parts.join('\u0000')
}

class WailsYuriClient implements YuriClient {
  readonly mode = 'wails' as const

  /**
   * One page of the conversation list.
   *
   * The page bounds are the backend's, not this client's: the bridge is a trust
   * boundary and clamps whatever it is handed, so an omitted field asks for its
   * default rather than restating it here. `offset` is what makes a store
   * larger than one page reachable — without it the sidebar showed the newest
   * page and gave no sign that anything else existed.
   *
   * A page carries metadata only unless `messageLimit` asks otherwise: the
   * sidebar draws a title, a preview and a timestamp, and `listMessages`
   * fetches the one transcript actually opened.
   *
   * A bridge built before paging existed has no `ListConversationsPage`; the
   * older method is used instead, and it still answers with transcripts, which
   * `ChatView` detects and treats as already loaded.
   */
  async listConversations(options: ConversationPageOptions = {}): Promise<Conversation[]> {
    const paged = findBridgeMethod(['ListConversationsPage'])
    const value = paged
      ? await paged({ limit: options.limit ?? 0, offset: options.offset ?? 0, messageLimit: options.messageLimit ?? 0 })
      : await callBridge<unknown>(['ListConversations', 'GetConversations'])
    return normalizeConversationList(value)
  }

  async listMessages(conversationId: string, limit: number, before?: string): Promise<ChatHistoryPage> {
    const value = await callBridge<unknown>(
      ['ListMessages', 'ListConversationMessages'],
      [conversationId, limit, before ?? ''],
    )
    return normalizeChatHistoryPage(value, conversationId)
  }

  async createConversation(title: string): Promise<Conversation> {
    const created = normalizeConversation(await callBridge<unknown>(['NewConversation', 'CreateConversation'], [title]))
    return created ?? {
      id: makeId('conversation'),
      title: title.trim() || 'Новый диалог',
      titleSource: title.trim() && title.trim() !== 'Новый диалог' ? 'user' : 'default',
      preview: 'Пока нет сообщений',
      updatedAt: nowIso(),
      messages: [],
    }
  }

  async renameConversation(conversationId: string, title: string): Promise<Conversation | undefined> {
    const normalizedTitle = title.trim()
    if (!normalizedTitle) throw new Error('Название диалога не может быть пустым.')
    if (!findBridgeMethod(['RenameConversation', 'UpdateConversationTitle'])) throw new Error('Backend не поддерживает переименование диалогов.')
    const result = await callBridge<unknown>(['RenameConversation', 'UpdateConversationTitle'], [{
      id: conversationId,
      conversationId,
      title: normalizedTitle,
    }])
    const conversation = normalizeConversation(result)
    // Older bridges return only `error` from RenameConversation. The side
    // effect has still completed, so ChatView can apply the local user-owned
    // title while newer bridges return the canonical conversation shape.
    return conversation
  }

  async listChatTools(): Promise<ChatTool[]> {
    return normalizeChatToolList(await callBridgeSafe<unknown>(['ListChatTools', 'ListTools', 'GetChatTools']))
  }

  async listAgents(): Promise<AgentProfile[]> {
    const value = await callBridgeSafe<unknown>(['ListAgents', 'ListAgentProfiles'])
    if (!Array.isArray(value)) return []
    return value.map(normalizeAgentProfile).filter((agent): agent is AgentProfile => Boolean(agent))
  }

  async getActiveAgent(): Promise<AgentProfile | undefined> {
    return normalizeAgentProfile(await callBridgeSafe<unknown>(['GetActiveAgent', 'GetActiveAgentProfile']))
  }

  async createAgent(input: AgentProfileInput): Promise<AgentProfile> {
    const result = normalizeAgentProfile(await callBridge<unknown>(['CreateAgent', 'CreateAgentProfile'], [input]))
    if (!result) throw new Error('Backend не вернул созданного агента.')
    return result
  }

  async setActiveAgent(agentId: string): Promise<AgentProfile> {
    const result = normalizeAgentProfile(await callBridge<unknown>(['SetActiveAgent', 'SelectAgent'], [{ id: agentId }]))
    if (!result) throw new Error('Backend не подтвердил выбор агента.')
    return result
  }

  async sendMessage(request: ChatRequest, onEvent: (event: ChatEvent) => void): Promise<RunResult> {
    return this.runWithBridge(['SendMessage', 'StartChat', 'Chat'], request, onEvent)
  }

  async retryLast(request: ChatRequest, onEvent: (event: ChatEvent) => void): Promise<RunResult> {
    return this.runWithBridge(['RetryMessage', 'RetryChat', 'SendMessage'], request, onEvent)
  }

  async getChatAttachment(messageId: string, attachmentId: string): Promise<ChatAttachmentContent | undefined> {
    const value = await callBridgeSafe<unknown>(['GetChatAttachment', 'ReadChatAttachment'], [{ messageId, attachmentId }])
    if (!value || typeof value !== 'object') return undefined
    const source = value as UnknownRecord
    const id = optionalString(source, 'id', 'attachmentId', 'attachment_id')
    const dataUrl = optionalString(source, 'dataUrl', 'data_url')
    if (!id || !dataUrl) return undefined
    return {
      id,
      mediaType: optionalString(source, 'mediaType', 'media_type') ?? 'application/octet-stream',
      dataUrl,
    }
  }

  async openExternalURL(url: string): Promise<void> {
    await callBridge(['OpenExternalURL'], [url])
  }

  async openLocalPath(path: string): Promise<void> {
    await callBridge(['OpenLocalPath'], [path])
  }

  async cancelRun(runId: string): Promise<void> {
    await callBridge(['CancelRun', 'CancelAgentRun'], [runId])
  }

  async approve(approvalId: string, decision: ApprovalDecision): Promise<void> {
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
      ? { ...defaultSettings, kind: 'codex-app-server', model: String(enabledProvider.model ?? '') }
      : selectedOpenAI
      ? {
          ...defaultSettings,
          ...selectedOpenAI,
          kind: 'openai-compatible',
          apiKeyConfigured: Boolean(selectedOpenAI.apiKeyConfigured ?? selectedOpenAI.api_key_configured ?? selectedOpenAI.hasSecret),
        }
      : defaultSettings
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
      model: settings.model,
      binary: 'codex',
      enabled: true,
    }])
  }

  async getWebSearchSettings(): Promise<WebSearchSettings> {
    return normalizeWebSearchSettings(await callBridge<unknown>(['GetWebSearchSettings']))
  }

  async saveWebSearchSettings(settings: WebSearchSettings): Promise<void> {
    await callBridge(['SaveWebSearchSettings'], [{
      enabled: settings.enabled,
      provider: settings.provider,
      endpoint: settings.endpoint,
      defaultResultLimit: settings.defaultResultLimit,
    }])
  }

  async testWebSearchSettings(settings: WebSearchSettings): Promise<ProviderTestResult> {
    return (await callBridge<ProviderTestResult>(['TestWebSearchSettings'], [{
      enabled: settings.enabled,
      provider: settings.provider,
      endpoint: settings.endpoint,
      defaultResultLimit: settings.defaultResultLimit,
    }])) ?? { ok: false, message: 'Backend не вернул результат проверки SearXNG.' }
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
      return normalized.state.completed && normalized.state.providerTested && normalized.state.agentConfigured
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

  async logoutCodex(): Promise<CodexLogoutResult> {
    const method = findBridgeMethod(['CodexLogout', 'LogoutCodex', 'DisconnectCodexAccount'])
    if (!method) throw new Error('Backend не поддерживает выход из Codex App Server.')
    const value = await method()
    if (!value || typeof value !== 'object') {
      throw new Error('Backend не подтвердил выход из Codex App Server.')
    }
    const result = value as UnknownRecord
    const disconnected = normalizeBoolean(result.disconnected ?? result.loggedOut ?? result.logged_out, false)
    if (!disconnected) throw new Error('Codex App Server не подтвердил выход.')
    return {
      disconnected: true,
      onboarding: normalizeOnboardingState(result.onboarding ?? result.state),
    }
  }

  async getCodexModels(): Promise<CodexModel[]> {
    return normalizeCodexModels(await callBridgeSafe<unknown>(['CodexModels', 'ListCodexModels']))
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

  async setMemoryScope(memoryId: string, scope: MemoryScope): Promise<MemoryRecord | undefined> {
    return normalizeMemory(await callBridge<unknown>(['SetMemoryScope'], [{ id: memoryId, memoryId, scope }]))
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

  // PluginPathRequest carries only the path. Signature verification is not
  // bypassable per request, so there is no dev-mode argument to send: the
  // backend answers with `installable`, already resolved against the
  // persisted switch.
  async inspectPluginPackage(path: string): Promise<PluginPackageInspection> {
    const result = await callBridge<unknown>(['InspectPluginPackage', 'InspectPlugin'], [{ path }])
    return normalizePluginInspection(result, path)
  }

  async installPlugin(request: PluginInstallRequest): Promise<PluginRecord | undefined> {
    const result = await callBridge<unknown>(['InstallPlugin', 'InstallPluginPackage'], [{ path: request.path }])
    return normalizePlugin(result)
  }

  // Bridge.PluginDevMode() bool / Bridge.SetPluginDevMode(enabled bool) error.
  // Both take scalars, so the flag travels positionally rather than wrapped in
  // a request object.
  async pluginDevMode(): Promise<boolean> {
    return Boolean(await callBridge<boolean>(['PluginDevMode']))
  }

  async setPluginDevMode(enabled: boolean): Promise<void> {
    await callBridge(['SetPluginDevMode'], [enabled])
  }

  async enablePlugin(request: PluginEnableRequest): Promise<PluginRecord | undefined> {
    // pluginEnablePayload throws before the call when a consent would produce
    // an unbounded grant without its own confirmation.
    return normalizePlugin(await callBridge<unknown>(['EnablePlugin'], [pluginEnablePayload(request)]))
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

  async listPeerDialogues(options: PeerDialogueListOptions = {}): Promise<PeerDialogue[]> {
    return normalizePeerDialogueList(await callBridge<unknown>(['ListPeerDialogues'], [options]))
  }

  async cancelPeerDialogue(dialogueId: string): Promise<void> {
    await callBridge(['CancelPeerDialogue'], [{ id: dialogueId }])
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

  /**
   * `ChatRunResult.Events` is a replay of the run for callers that have no live
   * stream at all; Go records an event in it only after dispatching the same
   * event on the bus, and it deliberately leaves `assistant.delta` out (see
   * `chatEmitter.record`). So when the live stream produced anything for this
   * run, the replay carries nothing new and is suppressed; when it produced
   * nothing, the replay is the only delivery and every event is forwarded.
   *
   * Lifecycle events are still matched by a cheap composite key rather than
   * trusted wholesale, so a backend that returns more than it dispatched still
   * renders exactly once. Deltas never enter that map — on a long answer they
   * are the overwhelming majority of the traffic, and keying them was what made
   * the old `JSON.stringify` dedup both hot and unbounded.
   */
  private async runWithBridge(names: string[], request: ChatRequest, onEvent: (event: ChatEvent) => void): Promise<RunResult> {
    // Wails v2 currently exposes a single ChatRequest object. The adapter keeps
    // the request typed so the binding can evolve without changing the UI.
    const liveKeys = new Map<string, number>()
    let liveEvents = 0
    const subscription = subscribeChatRun(request.conversationId, (event) => {
      liveEvents += 1
      if (event.type !== 'assistant.delta') {
        const key = chatEventKey(event)
        liveKeys.set(key, (liveKeys.get(key) ?? 0) + 1)
      }
      onEvent(event)
    })
    try {
      const result = await callBridge<RunResult | { runId?: string; status?: RunResult['status']; events?: ChatEvent[] }>(names, [request])
      if (!result) return { runId: makeId('run'), status: 'error' }
      if ('events' in result && Array.isArray(result.events)) {
        for (const value of result.events) {
          const event = normalizeChatEvent(value)
          if (!event) continue
          if (event.type === 'assistant.delta') {
            if (liveEvents > 0) continue
            onEvent(event)
            continue
          }
          const key = chatEventKey(event)
          const liveCount = liveKeys.get(key) ?? 0
          if (liveCount > 0) {
            liveKeys.set(key, liveCount - 1)
            continue
          }
          onEvent(event)
        }
      }
      return { runId: result.runId ?? makeId('run'), status: result.status ?? 'complete' }
    } finally {
      subscription.release()
    }
  }
}

function normalizeCodexModels(value: unknown): CodexModel[] {
  if (!Array.isArray(value)) return []
  return value.flatMap((item) => {
    if (!item || typeof item !== 'object') return []
    const source = item as UnknownRecord
    const model = optionalString(source, 'model')
    if (!model) return []
    const rawModalities = source.inputModalities ?? source.input_modalities
    return [{
      id: optionalString(source, 'id') ?? model,
      model,
      displayName: optionalString(source, 'displayName', 'display_name') ?? model,
      description: optionalString(source, 'description'),
      isDefault: normalizeBoolean(source.isDefault ?? source.is_default, false),
      defaultReasoningEffort: optionalString(source, 'defaultReasoningEffort', 'default_reasoning_effort'),
      inputModalities: Array.isArray(rawModalities) ? rawModalities.map(String) : [],
    }]
  })
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
  client = findBridgeMethod(['ListConversations', 'GetConversations', 'SendMessage', 'StartChat', 'ListChatTools', 'AllowedDirectories', 'GetOnboardingState', 'GetFirstRunState', 'CompleteOnboarding', 'CompleteFirstRun'])
    ? new WailsYuriClient()
    : new MockYuriClient()
  return client
}

export function resetYuriClientForTests(): void {
  client = undefined
  // The chat bus subscription is bound to whichever `window.runtime` was in
  // place when it opened, so a suite that swaps the runtime must drop it too.
  chatRunSubscriptions.clear()
  retiredChatRunIds.length = 0
  retiredChatRunIdSet.clear()
  chatBusUnsubscribe?.()
  chatBusUnsubscribe = undefined
}
