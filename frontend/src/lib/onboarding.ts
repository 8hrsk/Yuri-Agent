import type { CodexAccount, OnboardingState, ProviderSettings } from './contracts'

export const onboardingSteps = [
  { id: 'welcome', label: 'Начало' },
  { id: 'provider', label: 'Провайдер' },
  { id: 'success', label: 'Готово' },
] as const

export type OnboardingStep = (typeof onboardingSteps)[number]['id']

export function isOnboardingComplete(state: OnboardingState): boolean {
  return state.completed && state.providerTested
}

export function onboardingStepIndex(step: OnboardingStep): number {
  return onboardingSteps.findIndex((item) => item.id === step)
}

export function validateOnboardingProvider(settings: ProviderSettings, codex?: CodexAccount): string | undefined {
  if (settings.kind === 'antigravity') {
    return 'Antigravity OAuth пока недоступен: официальный integration contract для стороннего приложения отсутствует.'
  }
  if (settings.kind === 'codex-app-server') {
    return codex?.connected ? undefined : 'Завершите OAuth-вход в Codex App Server.'
  }
  if (!settings.baseUrl.trim()) return 'Укажите Base URL провайдера.'
  if (!settings.model.trim()) return 'Укажите модель провайдера.'
  return undefined
}
