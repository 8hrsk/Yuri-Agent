export type MessageRole = 'user' | 'assistant' | 'tool'

export type MessageStatus = 'complete' | 'streaming' | 'cancelled' | 'error'

export type RunStatus = 'idle' | 'thinking' | 'tool_running' | 'waiting_approval' | 'speaking' | 'cancelled' | 'error'

export type ToolRisk = 'low' | 'medium' | 'high' | 'critical'

export type ToolStatus = 'pending' | 'running' | 'completed' | 'failed' | 'cancelled' | 'denied'

export interface ToolCall {
  id: string
  name: string
  label: string
  risk: ToolRisk
  status: ToolStatus
  args: Record<string, unknown>
  result?: string
  startedAt?: string
  finishedAt?: string
}

export interface ApprovalRequest {
  id: string
  toolCallId: string
  title: string
  explanation: string
  risk: ToolRisk
  scope: string
  expiresAt?: string
}

export interface ChatMessage {
  id: string
  role: MessageRole
  content: string
  status: MessageStatus
  createdAt: string
  runId?: string
  toolCall?: ToolCall
}

export interface Conversation {
  id: string
  title: string
  preview: string
  updatedAt: string
  messages: ChatMessage[]
}

/**
 * A memory belongs to Yuri's single local profile, not to an individual
 * conversation. Lifecycle is deliberately explicit: dormant records are
 * excluded from normal recall, while deleted records are soft tombstones.
 */
export type MemoryKind = 'core' | 'user_model' | 'episodic' | 'semantic' | 'relationship' | 'procedural'

export type MemoryContentKind = 'fact' | 'opinion' | 'emotion' | 'inference'

export type MemoryLifecycleState = 'active' | 'dormant' | 'deleted'

export interface MemorySource {
  sourceType: string
  sourceId?: string
  conversationId?: string
  conversationTitle?: string
  messageId?: string
  excerpt?: string
  excerptHash?: string
  evidenceWeight?: number
  createdAt?: string
}

export interface MemoryRecord {
  id: string
  kind: MemoryKind
  contentKind: MemoryContentKind
  content: string
  confidence: number
  salience: number
  valence?: number
  sensitivity?: string
  lifecycleState: MemoryLifecycleState
  pinned: boolean
  accessCount: number
  lastRecalledAt?: string
  decayPolicy?: string
  embeddingVersion?: string
  createdAt: string
  updatedAt: string
  sources: MemorySource[]
}

export interface MemoryListOptions {
  lifecycleState?: MemoryLifecycleState | 'all'
  kind?: MemoryKind | 'all'
  query?: string
  limit?: number
  offset?: number
}

export interface MemoryUpdate {
  content?: string
  kind?: MemoryKind
  contentKind?: MemoryContentKind
  confidence?: number
  salience?: number
  valence?: number
  pinned?: boolean
}

export type PluginStatus = 'installed' | 'enabled' | 'running' | 'stopped' | 'crashed' | 'error' | 'disabled' | 'unknown'

export type PluginSignatureStatus = 'signed' | 'unsigned' | 'invalid' | 'dev' | 'unknown'

export interface PluginPermission {
  capability: string
  scope?: string
  description?: string
  risk?: ToolRisk
  granted: boolean
  grantExpiresAt?: string
}

export interface PluginTool {
  id: string
  name: string
  description?: string
  risk: ToolRisk
}

export interface PluginRecord {
  id: string
  name: string
  version: string
  publisher?: string
  description?: string
  protocolVersion?: string
  coreVersionRange?: string
  enabled: boolean
  running: boolean
  status: PluginStatus
  installPath?: string
  signatureStatus: PluginSignatureStatus
  checksum?: string
  repositoryUrl?: string
  releaseTag?: string
  sourceCommit?: string
  permissions: PluginPermission[]
  tools: PluginTool[]
  eventSources: string[]
  lastError?: string
  installedAt?: string
  updatedAt?: string
}

export interface PluginPackageInspection {
  path: string
  valid: boolean
  compatible: boolean
  manifest?: PluginRecord
  signatureStatus: PluginSignatureStatus
  checksum?: string
  warnings: string[]
  errors: string[]
  installable?: boolean
  requiresDevMode?: boolean
}

export type ScheduleType = 'once' | 'interval' | 'cron'

export type ScheduleStatus = 'active' | 'paused' | 'completed' | 'error' | 'unknown'

export type MisfirePolicy = 'skip' | 'run_once'

export type DeliveryChannel = 'in_app' | 'notification'

export interface ScheduleBudget {
  maxDurationSeconds?: number
  maxTokens?: number
  maxToolCalls?: number
}

export interface Schedule {
  id: string
  title: string
  prompt: string
  type: ScheduleType
  /** ISO instant used by one-shot schedules. */
  runAt?: string
  /** Interval duration in seconds. */
  intervalSeconds?: number
  /** Standard five-field cron expression. */
  expression?: string
  timezone: string
  misfirePolicy: MisfirePolicy
  enabled: boolean
  status: ScheduleStatus
  nextRunAt?: string
  lastRunAt?: string
  deliveryChannel: DeliveryChannel
  budget?: ScheduleBudget
  createdAt?: string
  updatedAt?: string
  lastError?: string
}

export interface ScheduleInput {
  id?: string
  title: string
  prompt: string
  type: ScheduleType
  runAt?: string
  intervalSeconds?: number
  expression?: string
  timezone: string
  misfirePolicy: MisfirePolicy
  enabled?: boolean
  deliveryChannel?: DeliveryChannel
  budget?: ScheduleBudget
}

export type JobRunStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled' | 'skipped' | 'unknown'

export interface JobRun {
  id: string
  scheduleId: string
  scheduleTitle?: string
  status: JobRunStatus
  attempt: number
  startedAt?: string
  finishedAt?: string
  durationMs?: number
  error?: string
  summary?: string
  triggeredBy?: 'schedule' | 'manual' | 'recovery' | 'unknown'
}

export interface JobRunListOptions {
  scheduleId?: string
  limit?: number
}

export interface ProactivitySettings {
  enabled: boolean
  quietHoursEnabled: boolean
  quietHoursStart: string
  quietHoursEnd: string
  timezone: string
  dailyLimit: number
  cooldownMinutes: number
  allowLocalNotifications: boolean
}

export type ActivityType = 'job' | 'proactive' | 'system' | 'reflection' | 'memory' | 'unknown'

export type ActivityStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled' | 'skipped' | 'blocked' | 'info' | 'unknown'

export interface ActivityEvent {
  id: string
  type: ActivityType
  status: ActivityStatus
  title: string
  detail?: string
  source?: string
  scheduleId?: string
  runId?: string
  createdAt: string
  durationMs?: number
  reason?: string
  provenance?: string
}

export interface ActivityListOptions {
  limit?: number
  type?: ActivityType | 'all'
  status?: ActivityStatus | 'all'
}

/**
 * The renderer only ever receives bounded, versioned persona state.  These
 * contracts deliberately keep the subjective relationship model separate
 * from factual memories and from the immutable policy layer.
 */
export type AvatarState = 'idle' | 'listening' | 'thinking' | 'speaking' | 'tool_running' | 'error'

export type PersonaTraitId =
  | 'emotionality'
  | 'jealousy'
  | 'tsundere'
  | 'directness'
  | 'romance'
  | 'topic_boundaries'
  | 'warmth'
  | 'curiosity'
  | string

export interface PersonaTrait {
  id: PersonaTraitId
  label: string
  /** Normalized value in the inclusive 0..1 range. */
  value: number
  /** Reflection/UI bounds; values outside this interval are rejected by the decoder. */
  min: number
  max: number
  pinned: boolean
  description?: string
  updatedAt?: string
}

export interface PersonaEvidence {
  id?: string
  sourceType: string
  sourceId?: string
  conversationId?: string
  conversationTitle?: string
  messageId?: string
  runId?: string
  excerpt?: string
  excerptHash?: string
  provenance?: string
  weight?: number
  userConfirmed?: boolean
  createdAt?: string
}

export type SubjectiveLabel = 'opinion' | 'inference'

export interface SubjectiveOpinion {
  id: string
  subject: string
  content: string
  /** This label is intentionally explicit so the UI cannot render an opinion as fact. */
  label: SubjectiveLabel
  confidence: number
  evidence: PersonaEvidence[]
  reason?: string
  createdAt?: string
  updatedAt?: string
}

export type AffectEmotion =
  | 'sympathy'
  | 'tenderness'
  | 'joy'
  | 'gratitude'
  | 'boredom'
  | 'anger'
  | 'irritation'
  | 'jealousy'
  | 'resentment'
  | 'anxiety'
  | string

export interface AffectiveDimension {
  id: AffectEmotion
  label: string
  /** Intensity in the inclusive 0..1 range. */
  value: number
  /** Optional signed contribution, where negative values are unpleasant. */
  valence?: number
}

export interface AffectiveState {
  id?: string
  version?: number
  mood: string
  valence: number
  arousal: number
  intensity: number
  dimensions: AffectiveDimension[]
  reason?: string
  evidence?: PersonaEvidence[]
  updatedAt?: string
}

export interface RelationshipDimension {
  id: string
  label: string
  value: number
}

export interface RelationshipState {
  id: string
  version: number
  summary: string
  dimensions: RelationshipDimension[]
  opinions: SubjectiveOpinion[]
  affect: AffectiveState
  reason?: string
  evidence?: PersonaEvidence[]
  updatedAt?: string
}

export interface PersonaVersion {
  id: string
  version: number
  parentId?: string
  traits: PersonaTrait[]
  diff?: Record<string, unknown>
  promptText?: string
  reason: string
  evidence: PersonaEvidence[]
  authorRunId?: string
  createdAt: string
}

export interface PersonalitySnapshot {
  id: string
  currentVersion: number
  currentVersionId?: string
  traits: PersonaTrait[]
  pinnedTraits: string[]
  opinions: SubjectiveOpinion[]
  affect: AffectiveState
  relationship: RelationshipState
  versions: PersonaVersion[]
  autoEvolution: boolean
  lastReflectionAt?: string
}

/** Alias used by callers that use the domain term rather than the UI label. */
export type PersonaSnapshot = PersonalitySnapshot

export type YuriNotificationType = 'task.completed' | 'background.completed' | 'plugin.event' | 'rule.triggered' | 'agent.message' | 'unknown'

export interface YuriNotification {
  id: string
  type: YuriNotificationType
  title: string
  body: string
  createdAt: string
  allowNative: boolean
  permission?: NotificationPermission
  conversationId?: string
  deepLink?: string
}

export interface PluginInstallRequest {
  path: string
  devMode?: boolean
}

export interface ArchiveSearchRequest {
  query: string
  includeDormant?: boolean
  conversationId?: string
  limit?: number
  offset?: number
}

export interface ArchiveSearchResult {
  id: string
  conversationId?: string
  conversationTitle?: string
  messageId?: string
  role?: MessageRole
  content: string
  snippet?: string
  createdAt?: string
  score?: number
  matchType?: 'lexical' | 'semantic' | 'hybrid'
}

export interface ArchiveSearchResponse {
  results: ArchiveSearchResult[]
  total?: number
  query: string
}

export interface ChatRequest {
  conversationId: string
  text: string
  retryOfMessageId?: string
  voiceClip?: string
}

export interface UsageLimits {
  plan: string
  windowLabel: string
  usedPercent: number
  resetsAt: string
  detail: string
}

export interface ProviderSettings {
  kind: 'openai-compatible' | 'codex-app-server' | 'antigravity'
  baseUrl: string
  model: string
  apiKeyConfigured: boolean
  timeoutSeconds: number
  streamResponses: boolean
}

export interface CodexAccount {
  connected: boolean
  email?: string
  plan?: string
  authenticatedAt?: string
  limits?: UsageLimits
  loginUrl?: string
  verificationUrl?: string
  userCode?: string
}

export interface ProviderSnapshot {
  settings: ProviderSettings
  codex: CodexAccount
}

/**
 * First-run completion is durable only after a provider probe succeeds. The
 * renderer keeps these two flags separate so a saved-but-untested endpoint
 * never silently bypasses onboarding on the next launch.
 */
export interface OnboardingState {
  completed: boolean
  providerTested: boolean
  completedAt?: string
}

export interface OnboardingResult {
  ok: boolean
  message: string
  errorCode?: string
  alternative?: string
  state: OnboardingState
}

export interface ProviderTestResult {
  ok: boolean
  message: string
  errorCode?: string
  alternative?: string
}

export interface EncryptedBackupInput {
  /** Empty in Wails to request the native save dialog. */
  path?: string
  passphrase: string
  includeBlobs?: boolean
}

export interface EncryptedBackupInspectInput {
  /** Empty in Wails to request the native open dialog. */
  path?: string
  passphrase: string
}

export interface EncryptedBackupRestoreInput extends EncryptedBackupInspectInput {
  /** Empty in Wails to request a separate target directory. */
  targetDirectory?: string
}

export interface EncryptedBackupInfo {
  path: string
  createdAt: string
  sizeBytes: number
  blobCount: number
  hasConfig: boolean
  restoredTo?: string
}

export type ChatEvent =
  | { type: 'run.started'; runId: string }
  | { type: 'assistant.delta'; runId: string; messageId: string; delta: string }
  | { type: 'assistant.completed'; runId: string; messageId: string }
  | { type: 'tool.started'; runId: string; toolCall: ToolCall }
  | { type: 'approval.required'; runId: string; approval: ApprovalRequest }
  | { type: 'tool.updated'; runId: string; toolCall: ToolCall }
  | { type: 'run.status'; runId: string; status: RunStatus; label: string }
  | { type: 'run.completed'; runId: string; status: 'complete' | 'cancelled' | 'error'; error?: string }

export interface RunResult {
  runId: string
  status: 'complete' | 'cancelled' | 'error'
}

export interface YuriClient {
  readonly mode: 'wails' | 'mock'
  listConversations(): Promise<Conversation[]>
  createConversation(title: string): Promise<Conversation>
  sendMessage(request: ChatRequest, onEvent: (event: ChatEvent) => void): Promise<RunResult>
  cancelRun(runId: string): Promise<void>
  approve(approvalId: string, decision: 'approve' | 'deny'): Promise<void>
  retryLast(request: ChatRequest, onEvent: (event: ChatEvent) => void): Promise<RunResult>
  getProviderSnapshot(): Promise<ProviderSnapshot>
  saveProviderSettings(settings: ProviderSettings, apiKey?: string): Promise<void>
  testProvider(settings: ProviderSettings): Promise<ProviderTestResult>
  getOnboardingState(): Promise<OnboardingState>
  completeOnboarding(settings: ProviderSettings, apiKey?: string): Promise<OnboardingResult>
  loginCodex(): Promise<CodexAccount>
  refreshCodexLimits(): Promise<UsageLimits | undefined>
  createEncryptedBackup(input: EncryptedBackupInput): Promise<EncryptedBackupInfo | undefined>
  validateEncryptedBackup(input: EncryptedBackupInspectInput): Promise<EncryptedBackupInfo | undefined>
  restoreEncryptedBackup(input: EncryptedBackupRestoreInput): Promise<EncryptedBackupInfo | undefined>
  transcribeAudio(blob: Blob): Promise<string>
  getAllowedDirectories(): Promise<string[]>
  saveAllowedDirectories(directories: string[]): Promise<void>
  listMemories(options?: MemoryListOptions): Promise<MemoryRecord[]>
  searchArchive(request: ArchiveSearchRequest): Promise<ArchiveSearchResponse>
  updateMemory(memoryId: string, update: MemoryUpdate): Promise<MemoryRecord | undefined>
  setMemoryLifecycle(memoryId: string, state: MemoryLifecycleState): Promise<MemoryRecord | undefined>
  deleteMemory(memoryId: string): Promise<void>
  listPlugins(): Promise<PluginRecord[]>
  inspectPluginPackage(path: string, devMode?: boolean): Promise<PluginPackageInspection>
  installPlugin(request: PluginInstallRequest): Promise<PluginRecord | undefined>
  enablePlugin(pluginId: string): Promise<PluginRecord | undefined>
  disablePlugin(pluginId: string): Promise<PluginRecord | undefined>
  uninstallPlugin(pluginId: string): Promise<void>
  startPlugin(pluginId: string): Promise<PluginRecord | undefined>
  stopPlugin(pluginId: string): Promise<PluginRecord | undefined>
  listSchedules(): Promise<Schedule[]>
  createSchedule(input: ScheduleInput): Promise<Schedule | undefined>
  updateSchedule(input: ScheduleInput): Promise<Schedule | undefined>
  setScheduleEnabled(scheduleId: string, enabled: boolean): Promise<Schedule | undefined>
  runScheduleNow(scheduleId: string): Promise<JobRun | undefined>
  cancelJobRun(runId: string): Promise<JobRun | undefined>
  deleteSchedule(scheduleId: string): Promise<void>
  listJobRuns(options?: JobRunListOptions): Promise<JobRun[]>
  getProactivitySettings(): Promise<ProactivitySettings>
  saveProactivitySettings(input: ProactivitySettings): Promise<void>
  listActivity(options?: ActivityListOptions): Promise<ActivityEvent[]>
  getPersonaSnapshot(): Promise<PersonalitySnapshot>
  setPersonaAutoEvolution(enabled: boolean): Promise<PersonalitySnapshot | undefined>
  setPersonaTraitPinned(traitId: string, pinned: boolean): Promise<PersonalitySnapshot | undefined>
  rollbackPersona(versionId: string): Promise<PersonalitySnapshot | undefined>
  resetPersona(): Promise<PersonalitySnapshot | undefined>
  /** Personality-named aliases keep the bridge ergonomic for external callers. */
  getPersonalitySnapshot(): Promise<PersonalitySnapshot>
  setPersonalityAutoEvolution(enabled: boolean): Promise<PersonalitySnapshot | undefined>
  setPersonalityTraitPinned(traitId: string, pinned: boolean): Promise<PersonalitySnapshot | undefined>
  rollbackPersonality(versionId: string): Promise<PersonalitySnapshot | undefined>
  resetPersonality(): Promise<PersonalitySnapshot | undefined>
}
