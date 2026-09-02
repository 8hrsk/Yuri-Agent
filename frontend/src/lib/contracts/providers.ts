import type { ChatAttachmentInput } from './chat'

export interface ChatRequest {
  conversationId: string
  text: string
  retryOfMessageId?: string
  voiceClip?: string
  attachments?: ChatAttachmentInput[]
}

export interface UsageLimits {
  plan: string
  windowLabel: string
  usedPercent: number
  resetsAt: string
  detail: string
}

export type ProviderQuotaMode = 'off' | 'free-tier' | 'custom'

export interface ProviderQuotaProfile {
  rpm?: number
  tpm?: number
  rpd?: number
  maxConcurrent?: number
  safetyPercent?: number
  interactiveReservePercent?: number
}

export interface ProviderSettings {
  kind: 'openai-compatible' | 'codex-app-server' | 'antigravity' | 'google-ai-studio'
  providerId?: string
  displayName?: string
  baseUrl: string
  model: string
  apiStyle: 'responses' | 'chat_completions'
  apiKeyConfigured: boolean
  favoriteModels: string[]
  timeoutSeconds: number
  streamResponses: boolean
  /** Optional on legacy renderer payloads; backend treats absence as off. */
  quotaMode?: ProviderQuotaMode
  quotaProfile?: ProviderQuotaProfile
}

export interface ProviderOption {
  id: string
  kind: ProviderSettings['kind']
  displayName: string
  model: string
  enabled: boolean
  hasSecret: boolean
  quotaMode?: ProviderQuotaMode
  quotaProfile?: Partial<ProviderQuotaProfile>
}

export type OpenAIModelSort = '' | 'pricing-low-to-high' | 'pricing-high-to-low' | 'context-high-to-low' | 'throughput-high-to-low' | 'latency-low-to-high' | 'most-popular' | 'newest'

export interface OpenAIModel {
  id: string
  name: string
  description?: string
  contextLength: number
  maxCompletionTokens: number
  promptPrice?: string
  completionPrice?: string
  requestPrice?: string
  free: boolean
  supportsTools: boolean
  supportsToolsKnown: boolean
  /** The model can accept image input in the selected provider contract. */
  supportsVision: boolean
  supportsVisionKnown: boolean
  /** The model can return a provider-supported structured response. */
  supportsStructuredOutput: boolean
  supportsStructuredOutputKnown: boolean
  /** The model can enforce a JSON Schema response format. */
  supportsJSONSchema: boolean
  supportsJSONSchemaKnown: boolean
  inputModalities: string[]
  outputModalities: string[]
  created?: number
  favorite: boolean
}

export interface WebSearchSettings {
  enabled: boolean
  provider: 'searxng'
  endpoint: string
  defaultResultLimit: number
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

export interface CodexModel {
  id: string
  model: string
  displayName: string
  description?: string
  isDefault: boolean
  defaultReasoningEffort?: string
  inputModalities: string[]
}

export interface CodexLogoutResult {
  disconnected: boolean
  onboarding: OnboardingState
}

export interface ProviderSnapshot {
  settings: ProviderSettings
  openAI?: ProviderSettings
  codex: CodexAccount
  googleAIStudio?: ProviderSettings
}

/**
 * First-run completion is durable only after a provider probe succeeds. The
 * renderer keeps these two flags separate so a saved-but-untested endpoint
 * never silently bypasses onboarding on the next launch.
 */
export interface OnboardingState {
  completed: boolean
  providerTested: boolean
  agentConfigured: boolean
  activeAgentId?: string
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

export interface RunUsageStatsInput {
  from?: string
  to?: string
  agentId?: string
}

export interface RunUsageStatsGroup {
  agentId: string
  agentName?: string
  providerId?: string
  model?: string
  runCount: number
  statusCounts: Record<string, number>
  failureKinds: Record<string, number>
  inputTokens: number
  outputTokens: number
  totalTokens: number
}

export interface RunUsageStats {
  from: string
  to: string
  groups: RunUsageStatsGroup[]
}
