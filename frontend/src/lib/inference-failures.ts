import type { RunFailureKind } from './contracts'

export type InferenceRecoveryAction = 'retry' | 'settings' | 'personality' | 'new_chat'

export const inferenceRecoveryActionLabels: Record<InferenceRecoveryAction, string> = {
  retry: 'Повторить запрос',
  settings: 'Открыть Settings',
  personality: 'Выбрать модель агента',
  new_chat: 'Новый диалог',
}

export function inferenceFailureGuidance(kind?: RunFailureKind, retryAfterSeconds?: number, retryable?: boolean): string | undefined {
  switch (kind) {
    case 'authentication': return 'Откройте Settings и заново подключите этот provider.'
    case 'rate_limit': return retryAfterSeconds && retryAfterSeconds > 0
      ? `Маршрут не переключён. Повторите запрос через ${retryAfterSeconds} сек.`
      : 'Маршрут не переключён. Подождите и повторите запрос.'
    case 'quota_exhausted': return 'Проверьте лимит или баланс provider либо явно выберите другую модель.'
    case 'context_limit': return 'Сократите вложения или продолжите задачу в новом диалоге.'
    case 'model_unavailable': return 'Выберите доступную модель для этого агента в Personality.'
    case 'timeout': return 'Маршрут не переключён. Можно безопасно повторить запрос.'
    case 'transient': return 'Маршрут не переключён. Повторите запрос позже.'
    case 'invalid_request': return 'Проверьте выбранную модель и настройки OpenAI-compatible endpoint.'
    case 'budget_exceeded': return 'Сократите задачу или разбейте её на несколько запусков.'
    case 'unknown': return retryable ? 'Маршрут не переключён. Повторите запрос позже.' : 'Проверьте provider и модель в Settings.'
    default: return undefined
  }
}

export function inferenceFailureRecoveryActions(kind?: RunFailureKind, retryable?: boolean): InferenceRecoveryAction[] {
  switch (kind) {
    case 'authentication': return ['settings']
    case 'rate_limit':
    case 'timeout':
    case 'transient': return ['retry']
    case 'quota_exhausted': return ['settings', 'personality']
    case 'context_limit': return ['new_chat']
    case 'model_unavailable': return ['personality']
    case 'invalid_request': return ['settings', 'personality']
    case 'budget_exceeded': return ['new_chat']
    case 'unknown': return retryable ? ['retry'] : ['settings']
    default: return []
  }
}
