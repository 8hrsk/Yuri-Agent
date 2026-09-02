import type {
  EncryptedBackupInfo,
  OnboardingResult,
  OnboardingState,
  ProactivitySettings,
  ProviderSettings,
  UsageLimits,
  WebSearchSettings,
} from '../contracts'
import { normalizeBoolean, optionalNumber, optionalString } from './primitives'
import type { UnknownRecord } from './primitives'

const defaultSettings: ProviderSettings = {
  kind: 'openai-compatible',
  baseUrl: 'https://api.openai.com/v1',
  model: 'gpt-4o-mini',
  apiStyle: 'responses',
  apiKeyConfigured: false,
  favoriteModels: [],
  timeoutSeconds: 90,
  streamResponses: true,
  quotaMode: 'off',
  quotaProfile: { rpm: 0, tpm: 0, rpd: 0, maxConcurrent: 0, safetyPercent: 80, interactiveReservePercent: 25 },
}

const openRouterSettings: ProviderSettings = {
  ...defaultSettings,
  providerId: 'openrouter',
  displayName: 'OpenRouter',
  baseUrl: 'https://openrouter.ai/api/v1',
  model: '',
  apiStyle: 'chat_completions',
}

const googleAIStudioSettings: ProviderSettings = {
  ...defaultSettings,
  providerId: 'google-ai-studio',
  displayName: 'Google AI Studio',
  kind: 'google-ai-studio',
  baseUrl: 'https://generativelanguage.googleapis.com/v1beta/openai/',
  model: '',
  apiStyle: 'chat_completions',
  quotaMode: 'free-tier',
  quotaProfile: { rpm: 0, tpm: 0, rpd: 0, maxConcurrent: 1, safetyPercent: 80, interactiveReservePercent: 25 },
}

const defaultOnboardingState: OnboardingState = {
  completed: false,
  providerTested: false,
  agentConfigured: false,
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
  autonomousPeerDialogues: false,
  autonomousPeerDailyLimit: 2,
  autonomousPeerCooldownMinutes: 120,
}

const defaultWebSearchSettings: WebSearchSettings = {
  enabled: false,
  provider: 'searxng',
  endpoint: '',
  defaultResultLimit: 5,
}

function normalizeWebSearchSettings(value: unknown): WebSearchSettings {
  if (!value || typeof value !== 'object') return { ...defaultWebSearchSettings }
  const source = value as UnknownRecord
  const limit = optionalNumber(source, 'defaultResultLimit', 'default_result_limit')
  return {
    enabled: normalizeBoolean(source.enabled, false),
    provider: 'searxng',
    endpoint: optionalString(source, 'endpoint', 'baseUrl', 'base_url') ?? '',
    defaultResultLimit: Math.max(3, Math.min(10, Math.round(limit ?? 5))),
  }
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
  const result: OnboardingState = {
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
    agentConfigured: normalizeBoolean(source.agentConfigured ?? source.agent_configured, false),
  }
  const activeAgentId = optionalString(source, 'activeAgentId', 'active_agent_id')
  const completedAt = optionalString(source, 'completedAt', 'completed_at')
  if (activeAgentId) result.activeAgentId = activeAgentId
  if (completedAt) result.completedAt = completedAt
  return result
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
    providerId: settings.providerId,
    baseUrl: settings.baseUrl,
    model: settings.model,
    apiStyle: settings.apiStyle,
    timeoutSeconds: settings.timeoutSeconds,
    streamResponses: settings.streamResponses,
    apiKeyConfigured: settings.apiKeyConfigured,
    quotaMode: settings.quotaMode,
    quotaProfile: settings.quotaProfile,
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
    autonomous_peer_dialogues: input.autonomousPeerDialogues,
    autonomous_peer_daily_limit: input.autonomousPeerDailyLimit,
    autonomous_peer_cooldown_minutes: input.autonomousPeerCooldownMinutes,
  }
}

function normalizeProactivitySettings(value: unknown): ProactivitySettings {
  if (!value || typeof value !== 'object') return { ...defaultProactivitySettings }
  const rawValue = value as UnknownRecord
  const source = rawValue.settings && typeof rawValue.settings === 'object' ? rawValue.settings as UnknownRecord : rawValue
  const timezone = optionalString(source, 'timezone', 'timeZone', 'time_zone') ?? defaultProactivitySettings.timezone
  const dailyLimit = optionalNumber(source, 'dailyLimit', 'daily_limit', 'maxPerDay', 'max_per_day')
  const cooldownMinutes = optionalNumber(source, 'cooldownMinutes', 'cooldown_minutes', 'cooldown')
  const autonomousPeerDailyLimit = optionalNumber(source, 'autonomousPeerDailyLimit', 'autonomous_peer_daily_limit')
  const autonomousPeerCooldownMinutes = optionalNumber(source, 'autonomousPeerCooldownMinutes', 'autonomous_peer_cooldown_minutes')
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
    autonomousPeerDialogues: source.autonomousPeerDialogues === undefined && source.autonomous_peer_dialogues === undefined
      ? defaultProactivitySettings.autonomousPeerDialogues
      : Boolean(source.autonomousPeerDialogues ?? source.autonomous_peer_dialogues),
    autonomousPeerDailyLimit: Math.max(1, Math.round(autonomousPeerDailyLimit ?? defaultProactivitySettings.autonomousPeerDailyLimit)),
    autonomousPeerCooldownMinutes: Math.max(5, Math.round(autonomousPeerCooldownMinutes ?? defaultProactivitySettings.autonomousPeerCooldownMinutes)),
  }
}

export {
  defaultLimits,
  defaultOnboardingState,
  defaultProactivitySettings,
  defaultWebSearchSettings,
  defaultSettings,
  googleAIStudioSettings,
  openRouterSettings,
  normalizeEncryptedBackup,
  normalizeOnboardingResult,
  normalizeOnboardingState,
  normalizeProactivitySettings,
  normalizeWebSearchSettings,
  onboardingSettingsWire,
  proactivityWire,
}
