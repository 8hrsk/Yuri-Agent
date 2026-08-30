import type { AgentProfile, AgentProfileInput } from './agents'
import type { ArchiveSearchRequest, ArchiveSearchResponse, PluginInstallRequest } from './archive'
import type {
  EncryptedBackupInfo,
  EncryptedBackupInput,
  EncryptedBackupInspectInput,
  EncryptedBackupRestoreInput,
} from './backup'
import type { ChatEvent, RunResult } from './events'
import type { ApprovalDecision, ChatAttachmentContent, ChatHistoryPage, ChatTool, Conversation, ConversationPageOptions } from './chat'
import type { JobRun, JobRunListOptions, Schedule, ScheduleInput } from './scheduler'
import type { MemoryLifecycleState, MemoryListOptions, MemoryRecord, MemoryUpdate } from './memory'
import type {
  ActivityEvent,
  ActivityListOptions,
  PeerDialogue,
  PeerDialogueListOptions,
  ProactivitySettings,
} from './collaboration'
import type { PersonalitySnapshot } from './persona'
import type { PluginEnableRequest, PluginPackageInspection, PluginRecord } from './plugins'
import type {
  ChatRequest,
  CodexAccount,
  CodexLogoutResult,
  CodexModel,
  OnboardingResult,
  OnboardingState,
  ProviderSettings,
  ProviderSnapshot,
  ProviderTestResult,
  UsageLimits,
  WebSearchSettings,
} from './providers'

export interface YuriClient {
  readonly mode: 'wails' | 'mock'
  listConversations(options?: ConversationPageOptions): Promise<Conversation[]>
  /**
   * The page of messages immediately older than `before` (the id of the oldest
   * message the caller already holds). An empty `before` asks for the newest
   * page.
   */
  listMessages(conversationId: string, limit: number, before?: string): Promise<ChatHistoryPage>
  createConversation(title: string): Promise<Conversation>
  /** Explicit owner rename; this marks the title as user-owned on the backend. */
  renameConversation(conversationId: string, title: string): Promise<Conversation | undefined>
  listChatTools(): Promise<ChatTool[]>
  listAgents(): Promise<AgentProfile[]>
  getActiveAgent(): Promise<AgentProfile | undefined>
  createAgent(input: AgentProfileInput): Promise<AgentProfile>
  setActiveAgent(agentId: string): Promise<AgentProfile>
  sendMessage(request: ChatRequest, onEvent: (event: ChatEvent) => void): Promise<RunResult>
  cancelRun(runId: string): Promise<void>
  approve(approvalId: string, decision: ApprovalDecision): Promise<void>
  retryLast(request: ChatRequest, onEvent: (event: ChatEvent) => void): Promise<RunResult>
  getChatAttachment(messageId: string, attachmentId: string): Promise<ChatAttachmentContent | undefined>
  openExternalURL(url: string): Promise<void>
  openLocalPath(path: string): Promise<void>
  getProviderSnapshot(): Promise<ProviderSnapshot>
  saveProviderSettings(settings: ProviderSettings, apiKey?: string): Promise<void>
  getWebSearchSettings(): Promise<WebSearchSettings>
  saveWebSearchSettings(settings: WebSearchSettings): Promise<void>
  testWebSearchSettings(settings: WebSearchSettings): Promise<ProviderTestResult>
  testProvider(settings: ProviderSettings): Promise<ProviderTestResult>
  getOnboardingState(): Promise<OnboardingState>
  completeOnboarding(settings: ProviderSettings, apiKey?: string): Promise<OnboardingResult>
  loginCodex(): Promise<CodexAccount>
  logoutCodex(): Promise<CodexLogoutResult>
  getCodexModels(): Promise<CodexModel[]>
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
  inspectPluginPackage(path: string): Promise<PluginPackageInspection>
  installPlugin(request: PluginInstallRequest): Promise<PluginRecord | undefined>
  /**
   * Plugin dev mode is a persisted owner decision (config.plugin_dev_mode),
   * not a per-call argument. It is what allows an unsigned or unverified
   * package to be installed and started, so the view reads it from the
   * backend instead of keeping a local guess.
   */
  pluginDevMode(): Promise<boolean>
  setPluginDevMode(enabled: boolean): Promise<void>
  /**
   * Enabling requires the owner's consent list. A manifest declaration is a
   * request, so passing an empty `capabilities` enables the plugin with no
   * grants at all rather than with everything it asked for.
   */
  enablePlugin(request: PluginEnableRequest): Promise<PluginRecord | undefined>
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
  listPeerDialogues(options?: PeerDialogueListOptions): Promise<PeerDialogue[]>
  cancelPeerDialogue(dialogueId: string): Promise<void>
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
