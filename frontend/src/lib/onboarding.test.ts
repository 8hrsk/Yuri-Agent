import { describe, expect, it } from 'vitest'

import type { ProviderSettings } from './contracts'
import { isOnboardingComplete, onboardingStepIndex, onboardingSteps, validateOnboardingProvider } from './onboarding'

const openAISettings: ProviderSettings = {
  kind: 'openai-compatible',
  baseUrl: 'https://api.openai.com/v1',
  model: 'gpt-4o-mini',
  apiKeyConfigured: false,
  timeoutSeconds: 90,
  streamResponses: true,
}

describe('first-run onboarding rules', () => {
  it('requires both a successful provider probe and persisted completion', () => {
    expect(isOnboardingComplete({ completed: false, providerTested: true })).toBe(false)
    expect(isOnboardingComplete({ completed: true, providerTested: false })).toBe(false)
    expect(isOnboardingComplete({ completed: true, providerTested: true })).toBe(true)
  })

  it('validates the provider fields without requiring a real credential in preview', () => {
    expect(validateOnboardingProvider({ ...openAISettings, baseUrl: '' })).toBe('Укажите Base URL провайдера.')
    expect(validateOnboardingProvider({ ...openAISettings, model: '  ' })).toBe('Укажите модель провайдера.')
    expect(validateOnboardingProvider(openAISettings)).toBeUndefined()
  })

  it('allows Codex only after its existing OAuth account is connected', () => {
    const codex = { ...openAISettings, kind: 'codex-app-server' as const }
    expect(validateOnboardingProvider(codex, { connected: false })).toBe('Завершите OAuth-вход в Codex App Server.')
    expect(validateOnboardingProvider(codex, { connected: true })).toBeUndefined()
  })

  it('keeps Antigravity disabled until an official integration contract exists', () => {
    const antigravity = { ...openAISettings, kind: 'antigravity' as const }
    expect(validateOnboardingProvider(antigravity)).toContain('официальный integration contract')
  })

  it('keeps the stepper order stable', () => {
    expect(onboardingSteps.map((step) => step.id)).toEqual(['welcome', 'provider', 'success'])
    expect(onboardingStepIndex('provider')).toBe(1)
  })
})
