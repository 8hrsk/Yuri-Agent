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
