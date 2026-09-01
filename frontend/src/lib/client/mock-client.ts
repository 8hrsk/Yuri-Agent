import type {
  ActivityEvent,
  ActivityListOptions,
  AgentProfile,
  AgentProfileInput,
  AgentPersonalizationUpdate,
  PersonalityPreview,
  PersonalityPreviewScenario,
  PortableAgentProfile,
  AgentPersonalizationProfile,
  ApprovalDecision,
  ApprovalRequest,
  ArchiveSearchRequest,
  ArchiveSearchResponse,
  ChatAttachmentContent,
  ChatEvent,
  ChatRequest,
  ChatHistoryPage,
  ChatTool,
  CodexAccount,
  CodexLogoutResult,
  CodexModel,
  OpenAIModel,
  OpenAIModelSort,
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
  PeerRelationship,
  PeerRelationshipDetail,
  PeerRelationshipListOptions,
  PeerRelationshipVersion,
  PersonaVersion,
  PersonalitySnapshot,
  PluginEnableRequest,
  PluginInstallRequest,
  PluginPackageInspection,
  PluginRecord,
  ProactivitySettings,
  ProviderSettings,
  ProviderOption,
  ProviderSnapshot,
  ProviderTestResult,
  RunResult,
  Schedule,
  ScheduleInput,
  ToolCall,
  UsageLimits,
  WebSearchSettings,
  YuriClient,
} from '../contracts'
import { clonePersonalization } from '../agents'
import { aggregateChatEvent } from '../chat-trace'
import { pluginEnablePayload } from '../plugin-consent'
import { clonePersonalitySnapshot, createStarterPersonalitySnapshot } from '../personality'
import {
  starterActivity,
  starterConversation,
  starterJobRuns,
  starterPeerDialogues,
  starterSchedule,
} from './fixtures'
import { cloneConversation, clonePeerDialogue } from './normalize-conversation'
import { emptyPluginInspection } from './normalize-plugins'
import { makeId, nowIso, sleep } from './primitives'
import { defaultLimits, defaultOnboardingState, defaultProactivitySettings, defaultSettings, defaultWebSearchSettings } from './settings'

function starterPeerRelationship(): PeerRelationshipDetail {
  const now = Date.now()
  const initial: PeerRelationshipVersion = {
    id: 'peer-relationship-version-1', version: 1, operation: 'create', summary: 'Отношение ещё не сформировано.',
    dimensions: {}, opinions: [], reason: 'Создана направленная связь между агентами.', evidence: [],
    createdAt: new Date(now - 1000 * 60 * 19).toISOString(),
  }
  const reflected: PeerRelationshipVersion = {
    id: 'peer-relationship-version-2', version: 2, parentId: initial.id, operation: 'update',
    summary: 'Считает Миру вдумчивой и полезной собеседницей для проверки планов.',
    dimensions: { trust: 0.64, warmth: 0.55, reliability: 0.71 },
    opinions: [{
      id: 'peer-opinion-1', subject: 'Мира', content: 'Она хорошо замечает пробелы в структуре задачи.',
      label: 'opinion', confidence: 0.68, evidence: [], reason: 'Вывод после фонового диалога.',
      createdAt: new Date(now - 1000 * 60 * 17).toISOString(), updatedAt: new Date(now - 1000 * 60 * 17).toISOString(),
    }],
    reason: 'Рефлексия после фонового диалога.', evidence: [],
    createdAt: new Date(now - 1000 * 60 * 17).toISOString(),
  }
  return {
    relationship: {
      observerAgentId: 'agent-yuri', peerAgentId: 'agent-mira', peerName: 'Мира', relationshipId: 'peer-relationship-yuri-mira',
      version: reflected.version, currentVersionId: reflected.id, summary: reflected.summary,
      dimensions: { ...reflected.dimensions }, opinions: reflected.opinions.map((item) => ({ ...item, evidence: [...item.evidence] })),
      reason: reflected.reason, evidence: [], updatedAt: reflected.createdAt,
    },
    versions: [reflected, initial],
  }
}

function clonePeerRelationshipDetail(value: PeerRelationshipDetail): PeerRelationshipDetail {
  const cloneOpinions = (opinions: PeerRelationship['opinions']) => opinions.map((item) => ({ ...item, evidence: item.evidence.map((evidence) => ({ ...evidence })) }))
  return {
    relationship: {
      ...value.relationship,
      dimensions: { ...value.relationship.dimensions },
      opinions: cloneOpinions(value.relationship.opinions),
      evidence: value.relationship.evidence.map((item) => ({ ...item })),
    },
    versions: value.versions.map((version) => ({
      ...version,
      dimensions: { ...version.dimensions },
      opinions: cloneOpinions(version.opinions),
      evidence: version.evidence.map((item) => ({ ...item })),
    })),
  }
}

class MockYuriClient implements YuriClient {
  readonly mode = 'mock' as const
  private readonly conversations = new Map<string, Conversation>([[starterConversation().id, starterConversation()]])
  private readonly cancelledRuns = new Set<string>()
  private readonly pendingApprovals = new Map<string, (decision: ApprovalDecision) => void>()
  private provider: ProviderSnapshot = {
    settings: { ...defaultSettings },
    codex: { connected: false },
  }
  private onboarding: OnboardingState = { ...defaultOnboardingState }
  private readonly agents = new Map<string, AgentProfile>()
  private readonly agentPersonalization = new Map<string, AgentPersonalizationProfile>()
  private activeAgentId?: string
  private allowedDirectories: string[] = []
  // The offline preview has no config file to persist to, so dev mode lives
  // for the lifetime of the preview client only. Nothing offline is
  // installable either way; this just keeps the switch from reading back a
  // value the owner did not choose.
  private pluginDevModeEnabled = false
  private schedules: Schedule[] = [starterSchedule()]
  private jobRuns: JobRun[] = starterJobRuns()
  private activity: ActivityEvent[] = starterActivity()
  private peerDialogues: PeerDialogue[] = starterPeerDialogues()
  private peerRelationshipState: PeerRelationshipDetail = starterPeerRelationship()
  private proactivity: ProactivitySettings = { ...defaultProactivitySettings }
  private webSearch: WebSearchSettings = { ...defaultWebSearchSettings }
  private personality: PersonalitySnapshot = createStarterPersonalitySnapshot()
  private readonly personalitySeed: PersonalitySnapshot = createStarterPersonalitySnapshot()

  async listConversations(options: ConversationPageOptions = {}): Promise<Conversation[]> {
    const ordered = [...this.conversations.values()].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
    const offset = Math.max(0, options.offset ?? 0)
    const limit = options.limit && options.limit > 0 ? options.limit : ordered.length
    return ordered.slice(offset, offset + limit).map(cloneConversation)
  }

  /**
   * A page of transcript older than a cursor.
   *
   * This deliberately mirrors the bridge rather than shortcutting: it used to
   * ignore the cursor and answer every request with an empty page and
   * `hasMore: false`, which made it useless as a double — a test exercising
   * "show earlier" against it passed whatever the paging code did. In
   * particular an unknown cursor is an error here too, because the whole point
   * of the backend's cursor handling is that "that id is not in this
   * transcript" and "there is nothing older" are different answers.
   */
  async listMessages(conversationId: string, limit: number, before?: string): Promise<ChatHistoryPage> {
    const conversation = this.conversations.get(conversationId)
    if (!conversation) return { conversationId, messages: [], traces: [], hasMore: false }
    const size = limit > 0 ? limit : conversation.messages.length
    const cursor = before?.trim() ?? ''
    const end = cursor ? conversation.messages.findIndex((message) => message.id === cursor) : conversation.messages.length
    if (end === -1) throw new Error(`Курсор ${cursor} не принадлежит этому диалогу.`)
    const start = Math.max(0, end - size)
    return {
      conversationId,
      messages: conversation.messages.slice(start, end).map((message) => ({
        ...message,
        toolCall: message.toolCall ? { ...message.toolCall, args: { ...message.toolCall.args } } : undefined,
      })),
      traces: [],
      hasMore: start > 0,
    }
  }

  async createConversation(title: string): Promise<Conversation> {
    const normalizedTitle = title.trim()
    const conversation: Conversation = {
      id: makeId('conversation'),
      title: normalizedTitle || 'Новый диалог',
      titleSource: normalizedTitle && normalizedTitle !== 'Новый диалог' ? 'user' : 'default',
      preview: 'Пока нет сообщений',
      updatedAt: nowIso(),
      messages: [],
    }
    this.conversations.set(conversation.id, conversation)
    return cloneConversation(conversation)
  }

  async renameConversation(conversationId: string, title: string): Promise<Conversation | undefined> {
    const normalizedTitle = title.trim()
    if (!normalizedTitle) throw new Error('Название диалога не может быть пустым.')
    const conversation = this.conversations.get(conversationId)
    if (!conversation) throw new Error('Диалог не найден.')
    conversation.title = normalizedTitle
    conversation.titleSource = 'user'
    conversation.updatedAt = nowIso()
    return cloneConversation(conversation)
  }

  async listChatTools(): Promise<ChatTool[]> {
    return [
      {
        id: 'filesystem.read',
        name: 'filesystem.read',
        label: 'Чтение файлов',
        description: 'Читает текстовые файлы из разрешённых директорий.',
        risk: 'low',
        available: true,
        requiresApproval: false,
        capabilities: ['filesystem.read'],
      },
      {
        id: 'filesystem.write',
        name: 'filesystem.write',
        label: 'Изменение файла',
        description: 'Готовит запись в разрешённой директории после подтверждения.',
        risk: 'medium',
        available: true,
        requiresApproval: true,
        capabilities: ['filesystem.write'],
      },
    ]
  }

  async listAgents(): Promise<AgentProfile[]> {
    return [...this.agents.values()].map((agent) => ({ ...agent, backstory: agent.backstory, traits: { ...agent.traits }, active: agent.id === this.activeAgentId }))
  }

  async getActiveAgent(): Promise<AgentProfile | undefined> {
    const agent = this.activeAgentId ? this.agents.get(this.activeAgentId) : undefined
    return agent ? { ...agent, backstory: agent.backstory, traits: { ...agent.traits }, active: true } : undefined
  }

  async getActiveAgentPersonalization(): Promise<AgentPersonalizationProfile | undefined> {
    const value = this.activeAgentId ? this.agentPersonalization.get(this.activeAgentId) : undefined
    return value ? { ...value, ...clonePersonalization(value), temperament: { ...value.temperament } } : undefined
  }

  async updateActiveAgentPersonalization(input: AgentPersonalizationUpdate): Promise<AgentPersonalizationProfile> {
    if (!this.activeAgentId) throw new Error('Активный агент не выбран.')
    const current = this.agentPersonalization.get(this.activeAgentId)
    if (!current) throw new Error('Owner seed не найден.')
    if (input.expectedVersion !== current.version) throw new Error('Owner seed уже изменился. Перезагрузите редактор.')
    const reason = input.reason.trim()
    if (!reason) throw new Error('Укажите причину изменения owner seed.')
    const now = nowIso()
    const next: AgentPersonalizationProfile = {
      ...clonePersonalization(input.personalization),
      agentId: current.agentId,
      schemaVersion: current.schemaVersion,
      version: current.version + 1,
      revisionId: `${current.agentId}:personalization:v${current.version + 1}`,
      operation: 'update',
      reason,
      createdAt: current.createdAt,
      updatedAt: now,
      temperament: { ...input.traits },
    }
    this.agentPersonalization.set(current.agentId, next)
    return { ...next, ...clonePersonalization(next), temperament: { ...next.temperament } }
  }

  async createAgent(input: AgentProfileInput): Promise<AgentProfile> {
    const now = nowIso()
    const agent: AgentProfile = {
      id: makeId('agent'), name: input.name.trim(), age: input.age, gender: input.gender.trim(),
      preferences: input.preferences.trim(), backstory: input.backstory.trim(), traits: { ...input.traits }, active: true,
      providerId: input.providerId.trim(), model: input.model.trim(),
      createdAt: now, updatedAt: now,
    }
    this.agents.set(agent.id, agent)
    this.agentPersonalization.set(agent.id, {
      ...clonePersonalization(input.personalization), agentId: agent.id, schemaVersion: 2, version: 1,
      revisionId: `${agent.id}:personalization:v1`, operation: 'create', reason: 'owner configured personalization profile v2',
      createdAt: now, updatedAt: now, temperament: { ...input.traits },
    })
    this.activeAgentId = agent.id
    this.onboarding = {
      ...this.onboarding,
      agentConfigured: true,
      activeAgentId: agent.id,
      completed: this.onboarding.providerTested,
    }
    return { ...agent, backstory: agent.backstory, traits: { ...agent.traits } }
  }

  async exportActiveAgentProfile(): Promise<PortableAgentProfile | undefined> {
    return undefined
  }

  async openPortableAgentProfile(): Promise<PortableAgentProfile | undefined> {
    return undefined
  }

  async previewAgentPersonality(_input: AgentProfileInput, _scenario: PersonalityPreviewScenario): Promise<PersonalityPreview | undefined> {
    return undefined
  }

  async setActiveAgent(agentId: string): Promise<AgentProfile> {
    const agent = this.agents.get(agentId)
    if (!agent) throw new Error('Агент не найден.')
    this.activeAgentId = agent.id
    this.onboarding = { ...this.onboarding, activeAgentId: agent.id }
    return { ...agent, backstory: agent.backstory, traits: { ...agent.traits }, active: true }
  }

  async updateActiveAgentModelRoute(providerId: string, model: string): Promise<AgentProfile> {
    if (!this.activeAgentId) throw new Error('Активный агент не выбран.')
    const agent = this.agents.get(this.activeAgentId)
    if (!agent) throw new Error('Агент не найден.')
    const next = { ...agent, providerId: providerId.trim(), model: model.trim(), updatedAt: nowIso() }
    this.agents.set(next.id, next)
    return { ...next, traits: { ...next.traits }, active: true }
  }

  async listProviders(): Promise<ProviderOption[]> {
    const settings = this.provider.settings
    return [{ id: settings.providerId ?? (settings.kind === 'codex-app-server' ? 'codex' : 'openrouter'), kind: settings.kind, displayName: settings.displayName ?? (settings.kind === 'codex-app-server' ? 'Codex App Server' : 'OpenRouter'), model: settings.model, enabled: true, hasSecret: settings.apiKeyConfigured }]
  }

  async sendMessage(request: ChatRequest, onEvent: (event: ChatEvent) => void): Promise<RunResult> {
    return this.run(request, onEvent)
  }

  async retryLast(request: ChatRequest, onEvent: (event: ChatEvent) => void): Promise<RunResult> {
    return this.run(request, onEvent)
  }

  async getChatAttachment(messageId: string, attachmentId: string): Promise<ChatAttachmentContent | undefined> {
    for (const conversation of this.conversations.values()) {
      const attachment = conversation.messages.find((message) => message.id === messageId)?.attachments?.find((item) => item.id === attachmentId)
      if (attachment?.previewDataUrl) return { id: attachment.id, mediaType: attachment.mediaType, dataUrl: attachment.previewDataUrl }
    }
    return undefined
  }

  async openExternalURL(_url: string): Promise<void> {}

  async openLocalPath(_path: string): Promise<void> {}

  async cancelRun(runId: string): Promise<void> {
    this.cancelledRuns.add(runId)
    // Wake a run that is waiting for a user decision so cancellation is deterministic.
    for (const [approvalId, resolve] of this.pendingApprovals) {
      this.pendingApprovals.delete(approvalId)
      resolve('deny')
    }
  }

  async approve(approvalId: string, decision: ApprovalDecision): Promise<void> {
    const resolve = this.pendingApprovals.get(approvalId)
    if (!resolve) return
    this.pendingApprovals.delete(approvalId)
    resolve(decision)
  }

  async getProviderSnapshot(): Promise<ProviderSnapshot> {
    return {
      settings: { ...this.provider.settings },
      openAI: this.provider.openAI ? { ...this.provider.openAI, favoriteModels: [...this.provider.openAI.favoriteModels] } : undefined,
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
      openAI: settings.kind === 'openai-compatible'
        ? { ...settings, apiKeyConfigured: settings.apiKeyConfigured || Boolean(apiKey?.trim()), favoriteModels: [...settings.favoriteModels] }
        : this.provider.openAI,
    }
  }

  async connectOpenAIProvider(settings: ProviderSettings, apiKey?: string): Promise<OpenAIModel[]> {
    const connected = { ...settings, apiKeyConfigured: settings.apiKeyConfigured || Boolean(apiKey?.trim()) }
    if (!connected.apiKeyConfigured) throw new Error('API key обязателен для загрузки каталога.')
    this.provider = { ...this.provider, openAI: connected }
    return this.getOpenAIModels(connected.providerId ?? 'openrouter')
  }

  async getOpenAIModels(_providerId: string, sort: OpenAIModelSort = ''): Promise<OpenAIModel[]> {
    const favorites = new Set(this.provider.openAI?.favoriteModels ?? [])
    const models: OpenAIModel[] = [
      { id: 'openai/gpt-4.1-mini', name: 'GPT-4.1 Mini', description: 'Быстрая универсальная модель.', contextLength: 1_047_576, maxCompletionTokens: 32_768, promptPrice: '0.0000004', completionPrice: '0.0000016', free: false, supportsTools: true, inputModalities: ['text', 'image'], outputModalities: ['text'], favorite: favorites.has('openai/gpt-4.1-mini') },
      { id: 'openrouter/free', name: 'OpenRouter Free Models Router', description: 'Маршрутизатор по доступным бесплатным моделям.', contextLength: 200_000, maxCompletionTokens: 16_384, promptPrice: '0', completionPrice: '0', free: true, supportsTools: true, inputModalities: ['text'], outputModalities: ['text'], favorite: favorites.has('openrouter/free') },
      { id: 'anthropic/claude-sonnet-4', name: 'Claude Sonnet 4', description: 'Сильная модель для рассуждений и текста.', contextLength: 200_000, maxCompletionTokens: 64_000, promptPrice: '0.000003', completionPrice: '0.000015', free: false, supportsTools: true, inputModalities: ['text', 'image'], outputModalities: ['text'], favorite: favorites.has('anthropic/claude-sonnet-4') },
    ]
    if (sort === 'context-high-to-low') models.sort((a, b) => b.contextLength - a.contextLength)
    if (sort === 'pricing-low-to-high') models.sort((a, b) => Number(a.promptPrice ?? Infinity) - Number(b.promptPrice ?? Infinity))
    if (sort === 'pricing-high-to-low') models.sort((a, b) => Number(b.promptPrice ?? -1) - Number(a.promptPrice ?? -1))
    return models
  }

  async setOpenAIModelFavorite(_providerId: string, model: string, favorite: boolean): Promise<void> {
    const settings = this.provider.openAI ?? { ...defaultSettings }
    const favorites = new Set(settings.favoriteModels)
    if (favorite) favorites.add(model)
    else favorites.delete(model)
    this.provider = { ...this.provider, openAI: { ...settings, favoriteModels: [...favorites] } }
  }

  async getWebSearchSettings(): Promise<WebSearchSettings> {
    return { ...this.webSearch }
  }

  async saveWebSearchSettings(settings: WebSearchSettings): Promise<void> {
    this.webSearch = { ...settings }
  }

  async testWebSearchSettings(settings: WebSearchSettings): Promise<ProviderTestResult> {
    if (!settings.endpoint.trim()) return { ok: false, message: 'Укажите SearXNG endpoint.' }
    return { ok: true, message: 'SearXNG отвечает; получено результатов: 3.' }
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
      this.onboarding = { ...this.onboarding, completed: this.onboarding.agentConfigured, providerTested: true, completedAt: this.onboarding.agentConfigured ? nowIso() : undefined }
      return { ok: true, message: 'Codex App Server отвечает.' }
    }
    if (!settings.baseUrl.trim() || !settings.model.trim()) return { ok: false, message: 'Укажите Base URL и модель.' }
    this.onboarding = { ...this.onboarding, completed: this.onboarding.agentConfigured, providerTested: true, completedAt: this.onboarding.agentConfigured ? nowIso() : undefined }
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

  async logoutCodex(): Promise<CodexLogoutResult> {
    await sleep(220)
    this.provider = {
      ...this.provider,
      codex: { connected: false },
    }
    this.onboarding = { ...this.onboarding, completed: false, providerTested: false, completedAt: undefined }
    return { disconnected: true, onboarding: { ...this.onboarding } }
  }

  async getCodexModels(): Promise<CodexModel[]> {
    return [
      {
        id: 'gpt-5.6-sol',
        model: 'gpt-5.6-sol',
        displayName: 'GPT-5.6 Codex',
        description: 'Основная модель для сложных агентных задач.',
        isDefault: true,
        defaultReasoningEffort: 'medium',
        inputModalities: ['text', 'image'],
      },
      {
        id: 'gpt-5.6-terra',
        model: 'gpt-5.6-terra',
        displayName: 'GPT-5.6 Terra',
        description: 'Сбалансированная модель для повседневных задач.',
        isDefault: false,
        defaultReasoningEffort: 'medium',
        inputModalities: ['text', 'image'],
      },
    ]
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

  async updateBackstoryMemory(_memoryId: string, _content: string): Promise<MemoryRecord | undefined> {
    return undefined
  }

  async disableBackstoryMemory(_memoryId: string): Promise<MemoryRecord | undefined> {
    return undefined
  }

  async rehydrateBackstoryMemory(_memoryId: string): Promise<MemoryRecord | undefined> {
    return undefined
  }

  async setMemoryScope(_memoryId: string, _scope: MemoryScope): Promise<MemoryRecord | undefined> {
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

  async inspectPluginPackage(path: string): Promise<PluginPackageInspection> {
    return emptyPluginInspection(path, 'Проверка пакетов доступна после запуска plugin host.')
  }

  async installPlugin(_request: PluginInstallRequest): Promise<PluginRecord | undefined> {
    return undefined
  }

  async pluginDevMode(): Promise<boolean> {
    return this.pluginDevModeEnabled
  }

  async setPluginDevMode(enabled: boolean): Promise<void> {
    this.pluginDevModeEnabled = enabled
  }

  async enablePlugin(request: PluginEnableRequest): Promise<PluginRecord | undefined> {
    // Nothing is granted offline, but the consent list is still validated so
    // the preview cannot accept a request the real bridge would refuse.
    pluginEnablePayload(request)
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

  async listPeerDialogues(options: PeerDialogueListOptions = {}): Promise<PeerDialogue[]> {
    const limit = Math.max(1, Math.min(100, Math.round(options.limit ?? 50)))
    const active = this.activeAgentId ? this.agents.get(this.activeAgentId) : undefined
    const dialogues = this.peerDialogues.map((dialogue) => {
      if (!active) return clonePeerDialogue(dialogue)
      return {
        ...dialogue,
        initiatorAgentId: active.id,
        initiatorName: active.name,
        messages: dialogue.messages.map((message) => ({
          ...message,
          senderAgentId: message.senderAgentId === dialogue.initiatorAgentId ? active.id : message.senderAgentId,
          senderName: message.senderAgentId === dialogue.initiatorAgentId ? active.name : message.senderName,
          recipientAgentId: message.recipientAgentId === dialogue.initiatorAgentId ? active.id : message.recipientAgentId,
          recipientName: message.recipientAgentId === dialogue.initiatorAgentId ? active.name : message.recipientName,
        })),
      }
    })
    return dialogues.slice(0, limit).map(clonePeerDialogue)
  }

  async cancelPeerDialogue(dialogueId: string): Promise<void> {
    const current = this.peerDialogues.find((dialogue) => dialogue.id === dialogueId)
    if (!current) throw new Error('Диалог агентов не найден.')
    if (current.status !== 'queued' && current.status !== 'running') return
    const finishedAt = nowIso()
    this.peerDialogues = this.peerDialogues.map((dialogue) => dialogue.id === dialogueId
      ? { ...dialogue, status: 'cancelled', finishedAt, failure: 'Остановлено пользователем.' }
      : dialogue)
  }

  async listPeerRelationships(options: PeerRelationshipListOptions = {}): Promise<PeerRelationship[]> {
    const limit = Math.max(1, Math.min(100, Math.round(options.limit ?? 50)))
    if (limit < 1) return []
    const detail = clonePeerRelationshipDetail(this.peerRelationshipState)
    const active = this.activeAgentId ? this.agents.get(this.activeAgentId) : undefined
    if (active) detail.relationship.observerAgentId = active.id
    return [detail.relationship]
  }

  async getPeerRelationship(peerAgentId: string): Promise<PeerRelationshipDetail | undefined> {
    if (peerAgentId !== this.peerRelationshipState.relationship.peerAgentId) return undefined
    const detail = clonePeerRelationshipDetail(this.peerRelationshipState)
    const active = this.activeAgentId ? this.agents.get(this.activeAgentId) : undefined
    if (active) detail.relationship.observerAgentId = active.id
    return detail
  }

  async rollbackPeerRelationship(peerAgentId: string, versionId: string): Promise<PeerRelationshipDetail | undefined> {
    if (peerAgentId !== this.peerRelationshipState.relationship.peerAgentId) return undefined
    const target = this.peerRelationshipState.versions.find((version) => version.id === versionId)
    if (!target) throw new Error('Версия отношения не найдена.')
    this.appendPeerRelationshipVersion(target, 'rollback', 'Владелец откатил мнение агента о peer.')
    return this.getPeerRelationship(peerAgentId)
  }

  async resetPeerRelationship(peerAgentId: string): Promise<PeerRelationshipDetail | undefined> {
    if (peerAgentId !== this.peerRelationshipState.relationship.peerAgentId) return undefined
    this.appendPeerRelationshipVersion({
      id: '', version: 0, operation: 'reset', summary: 'Отношение ещё не сформировано.', dimensions: {}, opinions: [],
      reason: 'Владелец сбросил мнение агента о peer.', evidence: [], createdAt: nowIso(),
    }, 'reset', 'Владелец сбросил мнение агента о peer.')
    return this.getPeerRelationship(peerAgentId)
  }

  private appendPeerRelationshipVersion(source: PeerRelationshipVersion, operation: 'rollback' | 'reset', reason: string): void {
    const current = this.peerRelationshipState.relationship
    const createdAt = nowIso()
    const next: PeerRelationshipVersion = {
      ...source,
      id: makeId('peer-relationship-version'),
      version: current.version + 1,
      parentId: current.currentVersionId,
      operation,
      reason,
      dimensions: { ...source.dimensions },
      opinions: source.opinions.map((item) => ({ ...item, evidence: item.evidence.map((evidence) => ({ ...evidence })) })),
      evidence: source.evidence.map((item) => ({ ...item })),
      createdAt,
    }
    this.peerRelationshipState = {
      relationship: {
        ...current,
        version: next.version,
        currentVersionId: next.id,
        summary: next.summary,
        dimensions: { ...next.dimensions },
        opinions: next.opinions.map((item) => ({ ...item, evidence: item.evidence.map((evidence) => ({ ...evidence })) })),
        reason,
        evidence: next.evidence.map((item) => ({ ...item })),
        updatedAt: createdAt,
      },
      versions: [next, ...this.peerRelationshipState.versions],
    }
  }

  async getProactivitySettings(): Promise<ProactivitySettings> {
    return { ...this.proactivity }
  }

  async saveProactivitySettings(input: ProactivitySettings): Promise<void> {
    this.proactivity = {
      ...input,
      dailyLimit: Math.max(0, Math.round(input.dailyLimit)),
      cooldownMinutes: Math.max(0, Math.round(input.cooldownMinutes)),
      autonomousPeerDailyLimit: Math.max(1, Math.min(10, Math.round(input.autonomousPeerDailyLimit))),
      autonomousPeerCooldownMinutes: Math.max(5, Math.min(1440, Math.round(input.autonomousPeerCooldownMinutes))),
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

  async rollbackRelationship(versionId: string): Promise<PersonalitySnapshot | undefined> {
    const target = this.personality.relationship.versions.find((item) => item.id === versionId || String(item.version) === versionId)
    if (!target) return undefined
    const current = this.personality.relationship
    const nextVersion = Math.max(current.version, ...current.versions.map((item) => item.version)) + 1
    const next = {
      ...target,
      id: makeId('relationship-rollback'),
      version: nextVersion,
      parentId: current.versions.find((item) => item.version === current.version)?.id,
      operation: 'rollback',
      reason: `Владелец откатил связь к версии ${target.version}.`,
      createdAt: nowIso(),
      dimensions: { ...target.dimensions },
      diff: Object.fromEntries(Object.keys({ ...target.dimensions, ...Object.fromEntries(current.dimensions.map((item) => [item.id, item.value])) })
        .map((key) => [key, (target.dimensions[key] ?? 0) - (current.dimensions.find((item) => item.id === key)?.value ?? 0)])),
      evidence: target.evidence.map((item) => ({ ...item })),
    }
    this.personality = {
      ...this.personality,
      relationship: {
        ...current,
        version: nextVersion,
        summary: target.summary,
        dimensions: Object.entries(target.dimensions).map(([id, value]) => ({ id, label: current.dimensions.find((item) => item.id === id)?.label ?? id, value })),
        reason: next.reason,
        evidence: next.evidence.map((item) => ({ ...item })),
        versions: [...current.versions, next],
        updatedAt: next.createdAt,
      },
    }
    return this.getPersonaSnapshot()
  }

  async resetRelationship(): Promise<PersonalitySnapshot | undefined> {
    const seed = this.personalitySeed.relationship.versions.find((item) => item.operation === 'create')
      ?? this.personalitySeed.relationship.versions[0]
    if (!seed) return undefined
    const current = this.personality.relationship
    const nextVersion = Math.max(current.version, ...current.versions.map((item) => item.version)) + 1
    const next = {
      ...seed,
      id: makeId('relationship-reset'),
      version: nextVersion,
      parentId: current.versions.find((item) => item.version === current.version)?.id,
      operation: 'reset',
      reason: 'Владелец сбросил связь к relationship seed.',
      createdAt: nowIso(),
      dimensions: { ...seed.dimensions },
      evidence: seed.evidence.map((item) => ({ ...item })),
    }
    this.personality = {
      ...this.personality,
      relationship: {
        ...current,
        version: nextVersion,
        summary: seed.summary,
        dimensions: Object.entries(seed.dimensions).map(([id, value]) => ({ id, label: current.dimensions.find((item) => item.id === id)?.label ?? id, value })),
        opinions: [],
        reason: next.reason,
        evidence: next.evidence.map((item) => ({ ...item })),
        versions: [...current.versions, next],
        updatedAt: next.createdAt,
      },
      opinions: [],
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
    const emit = (event: ChatEvent) => {
      conversation.traces = aggregateChatEvent(conversation.traces ?? [], event, event.createdAt ?? event.timestamp ?? nowIso())
      onEvent(event)
    }
    const messageId = makeId('assistant')
    const userMessage: Conversation['messages'][number] = {
      id: makeId('user'),
      role: 'user',
      content: request.text,
      status: 'complete',
      createdAt: nowIso(),
      attachments: request.attachments?.map(({ dataBase64: _dataBase64, ...attachment }) => ({ ...attachment })),
    }
    conversation.messages.push(userMessage)
    conversation.updatedAt = nowIso()
    conversation.preview = request.text || request.attachments?.map((attachment) => attachment.name).join(', ') || 'Вложение'
    emit({ type: 'run.started', runId })
    emit({ type: 'run.status', runId, status: 'thinking', label: 'Yuri формирует ответ…' })
    await sleep(260)
    if (this.cancelledRuns.has(runId)) return this.finishCancelled(runId, emit)

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
      emit({ type: 'tool.started', runId, toolCall })
      const approval: ApprovalRequest = {
        id: makeId('approval'),
        toolCallId: toolCall.id,
        title: 'Разрешить изменение файла?',
        explanation: 'Yuri подготовила запись в разрешённой директории. Файл ещё не изменён.',
        risk: 'medium',
        scope: '~/Documents/notes.txt · добавить 86 байт',
      }
      const approvalDecision = new Promise<ApprovalDecision>((resolve) => this.pendingApprovals.set(approval.id, resolve))
      emit({ type: 'approval.required', runId, approval })
      emit({ type: 'run.status', runId, status: 'waiting_approval', label: 'Ожидается ваше разрешение' })
      const decision = await approvalDecision
      if (this.cancelledRuns.has(runId)) return this.finishCancelled(runId, emit)
      if (decision === 'deny') {
        emit({ type: 'tool.updated', runId, toolCall: { ...toolCall, status: 'denied', result: 'Операция отклонена пользователем.', finishedAt: nowIso() } })
        return this.finishError(runId, emit, 'Операция отклонена пользователем.')
      }
      emit({ type: 'tool.updated', runId, toolCall: { ...toolCall, status: 'completed', result: 'Изменение подготовлено в mock режиме.', finishedAt: nowIso() } })
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
      if (this.cancelledRuns.has(runId)) return this.finishCancelled(runId, emit)
      emit({ type: 'assistant.delta', runId, messageId, delta: chunk })
      await sleep(22)
    }
    emit({ type: 'assistant.completed', runId, messageId })
    emit({ type: 'run.completed', runId, status: 'complete' })
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

export { MockYuriClient }
