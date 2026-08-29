import type { AgentProfile, AgentProfileInput } from './contracts'

type UnknownRecord = Record<string, unknown>

export const defaultAgentTraits: Record<string, number> = {
  warmth: 0.58,
  directness: 0.72,
  emotionality: 0.62,
  playfulness: 0.55,
  jealousy: 0.20,
  irritability: 0.18,
}

export const defaultAgentDraft: AgentProfileInput = {
  name: 'Yuri',
  age: 21,
  gender: 'female',
  preferences: 'Тёплая, самостоятельная и немного цундере.',
  traits: { ...defaultAgentTraits },
}

function text(source: UnknownRecord, ...keys: string[]): string | undefined {
  for (const key of keys) {
    const value = source[key]
    if (typeof value === 'string' && value.trim()) return value.trim()
  }
  return undefined
}

function boundedTrait(value: unknown): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(numeric) ? Math.max(0, Math.min(1, numeric)) : 0
}

export function normalizeAgentTraits(value: unknown): Record<string, number> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  return Object.fromEntries(Object.entries(value as UnknownRecord)
    .filter(([key]) => /^[a-z][a-z0-9_]{0,63}$/.test(key))
    .map(([key, amount]) => [key, boundedTrait(amount)]))
}

export function normalizeAgentProfile(value: unknown): AgentProfile | undefined {
  if (!value || typeof value !== 'object') return undefined
  const source = value as UnknownRecord
  const id = text(source, 'id', 'agentId', 'agent_id')
  const name = text(source, 'name', 'displayName', 'display_name')
  const gender = text(source, 'gender')
  if (!id || !name || !gender) return undefined
  const rawAge = source.age
  const numericAge = typeof rawAge === 'number' ? rawAge : Number(rawAge)
  const age = Number.isFinite(numericAge) && numericAge >= 1 && numericAge <= 200 ? Math.round(numericAge) : undefined
  return {
    id,
    name,
    age,
    gender,
    preferences: text(source, 'preferences', 'shortPreferences', 'short_preferences') ?? '',
    traits: normalizeAgentTraits(source.traits ?? source.initialTraits ?? source.initial_traits),
    active: source.active === true || source.isActive === true || source.is_active === true,
    createdAt: text(source, 'createdAt', 'created_at') ?? new Date(0).toISOString(),
    updatedAt: text(source, 'updatedAt', 'updated_at') ?? text(source, 'createdAt', 'created_at') ?? new Date(0).toISOString(),
  }
}

export function validateAgentDraft(input: AgentProfileInput): string | undefined {
  const nameLength = [...input.name.trim()].length
  if (nameLength === 0) return 'Укажите имя агента.'
  if (nameLength > 64) return 'Имя агента должно быть короче 65 символов.'
  if (input.age !== undefined && (!Number.isInteger(input.age) || input.age < 1 || input.age > 200)) return 'Возраст должен быть целым числом от 1 до 200.'
  if (!input.gender.trim()) return 'Укажите пол или гендер агента.'
  if ([...input.preferences.trim()].length > 2000) return 'Краткие предпочтения должны быть короче 2001 символа.'
  return undefined
}

