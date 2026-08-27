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
  kind: 'openai-compatible' | 'codex-app-server'
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
  testProvider(settings: ProviderSettings): Promise<{ ok: boolean; message: string }>
  loginCodex(): Promise<CodexAccount>
  refreshCodexLimits(): Promise<UsageLimits | undefined>
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
}
