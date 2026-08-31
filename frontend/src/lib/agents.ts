import type {
  AgentCreationMode,
  AgentPersonalizationInput,
  AgentPersonalizationProfile,
  AgentProfile,
  AgentProfileInput,
  RelationshipSeedPreset,
} from './contracts'

type UnknownRecord = Record<string, unknown>

/** Maximum size of the owner-authored fictional identity seed. */
export const AGENT_BACKSTORY_MAX_LENGTH = 12_000

export const defaultAgentTraits: Record<string, number> = {
  warmth: 0.58,
  directness: 0.72,
  emotionality: 0.62,
  playfulness: 0.55,
  jealousy: 0.20,
  irritability: 0.18,
  empathy: 0.72,
  sociability: 0.48,
  shyness: 0.34,
  anxiety: 0.22,
  fearfulness: 0.18,
  emotional_stability: 0.64,
  sensitivity: 0.58,
  possessiveness: 0.16,
  romantic_tone: 0.25,
  initiative: 0.48,
  impulsivity: 0.22,
  stubbornness: 0.38,
  optimism: 0.58,
  curiosity: 0.72,
  suspicion: 0.18,
  trust: 0.45,
  attachment: 0.35,
  formality: 0.20,
  tsundere: 0.52,
}

export const defaultCommunicationStyle = {
  verbosity: 0.55,
  softness: 0.58,
  humor: 0.55,
  figurativeness: 0.35,
  expressiveness: 0.62,
  supportiveness: 0.72,
  formality: 0.20,
  teasing: 0.52,
  emojiFrequency: 0.10,
  flirtation: 0.25,
  conversationalInitiative: 0.48,
}

export const defaultEmotionalDynamics = {
  reactivity: 0.58,
  responseIntensity: 0.62,
  recoverySpeed: 0.64,
  positivePersistence: 0.50,
  negativePersistence: 0.36,
  expression: 0.62,
  masking: 0.25,
  conflictStyle: 'adaptive' as const,
  triggers: {},
  soothingStrategies: [],
}

export const relationshipSeeds: Record<RelationshipSeedPreset, { label: string; summary: string; dimensions: Record<string, number> }> = {
  new_acquaintances: { label: 'Только познакомились', summary: 'Связь только начинает формироваться.', dimensions: { trust: 0.35, attachment: 0.25, respect: 0.50, closeness: 0.20, reliability: 0.40, gratitude: 0.15, irritation: 0, jealousy: 0, resentment: 0 } },
  acquaintances: { label: 'Знакомые', summary: 'Уже знакомы и постепенно узнают друг друга.', dimensions: { trust: 0.48, attachment: 0.30, respect: 0.58, closeness: 0.34, reliability: 0.52, gratitude: 0.22, irritation: 0, jealousy: 0, resentment: 0 } },
  friends: { label: 'Друзья', summary: 'Доверяют друг другу как хорошие друзья.', dimensions: { trust: 0.70, attachment: 0.62, respect: 0.70, closeness: 0.68, reliability: 0.68, gratitude: 0.48, irritation: 0.05, jealousy: 0.08, resentment: 0 } },
  close_friends: { label: 'Близкие друзья', summary: 'Имеют долгую тёплую историю и высокое доверие.', dimensions: { trust: 0.84, attachment: 0.78, respect: 0.76, closeness: 0.84, reliability: 0.82, gratitude: 0.62, irritation: 0.06, jealousy: 0.12, resentment: 0 } },
  professional: { label: 'Партнёры по работе', summary: 'Уважают компетентность и держат профессиональную дистанцию.', dimensions: { trust: 0.58, attachment: 0.24, respect: 0.80, closeness: 0.28, reliability: 0.76, gratitude: 0.30, irritation: 0.04, jealousy: 0, resentment: 0 } },
  romantic_partners: { label: 'Романтические партнёры', summary: 'Начинают с взаимной близости и романтической привязанности.', dimensions: { trust: 0.78, attachment: 0.86, respect: 0.74, closeness: 0.88, reliability: 0.76, gratitude: 0.58, irritation: 0.04, jealousy: 0.22, resentment: 0 } },
  custom: { label: 'Своя история', summary: 'Исходное отношение задано владельцем.', dimensions: { trust: 0.50, attachment: 0.50, respect: 0.50, closeness: 0.50, reliability: 0.50, gratitude: 0.25, irritation: 0, jealousy: 0, resentment: 0 } },
}

export const defaultAgentPersonalization: AgentPersonalizationInput = {
  identity: { preferredLanguage: 'ru-RU', pronouns: 'она/её', userAddress: '', selfDescription: 'Тёплая, самостоятельная и немного цундере.', role: 'персональная помощница' },
  communicationStyle: { ...defaultCommunicationStyle },
  emotionalDynamics: { ...defaultEmotionalDynamics, triggers: {}, soothingStrategies: [] },
  relationshipSeed: { preset: 'new_acquaintances', summary: relationshipSeeds.new_acquaintances.summary, dimensions: { ...relationshipSeeds.new_acquaintances.dimensions } },
  structuredBackstory: { narrative: '', summary: '', episodes: [] },
  evolutionPolicy: {
    lockedFields: ['identity', 'backstory'],
    traitBounds: Object.fromEntries(Object.keys(defaultAgentTraits).map((trait) => [trait, { min: 0, max: 1 }])),
    reflectionMode: 'enabled',
    reflectionCooldownMinutes: 60,
    reflectionMaxTokens: 2_500,
    reflectionMaxDurationSeconds: 60,
    reflectionMaxEvidence: 8,
  },
}

export const defaultAgentDraft: AgentProfileInput = {
  name: 'Yuri',
  age: 21,
  gender: 'female',
  preferences: 'Тёплая, самостоятельная и немного цундере.',
  backstory: '',
  traits: { ...defaultAgentTraits },
  personalization: clonePersonalization(defaultAgentPersonalization),
  creationMode: 'quick',
  presetId: 'balanced',
}

export type AgentPreset = {
  id: string
  label: string
  description: string
  preferences: string
  traits: Partial<Record<string, number>>
  style: Partial<AgentPersonalizationInput['communicationStyle']>
  dynamics: Partial<AgentPersonalizationInput['emotionalDynamics']>
  relationship: RelationshipSeedPreset
}

export const agentPresets: readonly AgentPreset[] = [
  { id: 'balanced', label: 'Живая и уравновешенная', description: 'Тёплая, любопытная и достаточно прямая.', preferences: 'Тёплая, самостоятельная и немного цундере.', traits: {}, style: {}, dynamics: {}, relationship: 'new_acquaintances' },
  { id: 'gentle', label: 'Заботливая спутница', description: 'Мягкая поддержка, высокая эмпатия и спокойные реакции.', preferences: 'Заботливая, терпеливая и внимательная к чувствам.', traits: { warmth: 0.90, empathy: 0.92, irritability: 0.08, emotional_stability: 0.82, jealousy: 0.10 }, style: { softness: 0.92, supportiveness: 0.94, teasing: 0.18, flirtation: 0.30 }, dynamics: { reactivity: 0.42, recoverySpeed: 0.84, expression: 0.68, conflictStyle: 'direct' }, relationship: 'friends' },
  { id: 'reserved', label: 'Застенчивая аналитик', description: 'Осторожная, наблюдательная и немногословная.', preferences: 'Наблюдательная, застенчивая и любит проверяемые выводы.', traits: { shyness: 0.84, sociability: 0.24, curiosity: 0.88, directness: 0.62, emotionality: 0.34, suspicion: 0.58 }, style: { verbosity: 0.40, softness: 0.68, humor: 0.20, expressiveness: 0.28, emojiFrequency: 0.03 }, dynamics: { masking: 0.72, expression: 0.28, conflictStyle: 'withdraw' }, relationship: 'acquaintances' },
  { id: 'tsundere', label: 'Острая цундере', description: 'Прямая, колкая и эмоциональная, но надёжная.', preferences: 'Прямая, язвительная и заботливая, хотя не любит это признавать.', traits: { directness: 0.90, tsundere: 0.88, irritability: 0.62, warmth: 0.58, stubbornness: 0.72, jealousy: 0.54, romantic_tone: 0.42 }, style: { softness: 0.34, humor: 0.68, teasing: 0.84, expressiveness: 0.76 }, dynamics: { reactivity: 0.76, responseIntensity: 0.74, recoverySpeed: 0.54, conflictStyle: 'cold' }, relationship: 'new_acquaintances' },
]

export function clonePersonalization(value: AgentPersonalizationInput): AgentPersonalizationInput {
  return {
    identity: { ...value.identity },
    communicationStyle: { ...value.communicationStyle },
    emotionalDynamics: {
      ...value.emotionalDynamics,
      triggers: Object.fromEntries(Object.entries(value.emotionalDynamics.triggers).map(([emotion, triggers]) => [emotion, [...triggers]])),
      soothingStrategies: [...value.emotionalDynamics.soothingStrategies],
    },
    relationshipSeed: { ...value.relationshipSeed, dimensions: { ...value.relationshipSeed.dimensions } },
    structuredBackstory: { ...value.structuredBackstory, episodes: value.structuredBackstory.episodes.map((episode) => ({ ...episode, people: [...episode.people] })) },
    evolutionPolicy: { ...value.evolutionPolicy, lockedFields: [...value.evolutionPolicy.lockedFields], traitBounds: Object.fromEntries(Object.entries(value.evolutionPolicy.traitBounds).map(([trait, bounds]) => [trait, { ...bounds }])) },
  }
}

export function cloneAgentDraft(value: AgentProfileInput): AgentProfileInput {
  return { ...value, traits: { ...value.traits }, personalization: clonePersonalization(value.personalization) }
}

export function newAgentDraft(overrides: Partial<Pick<AgentProfileInput, 'name' | 'preferences' | 'creationMode'>> = {}): AgentProfileInput {
  return cloneAgentDraft({ ...defaultAgentDraft, ...overrides })
}

export function applyAgentPreset(value: AgentProfileInput, presetId: string): AgentProfileInput {
  const preset = agentPresets.find((item) => item.id === presetId) ?? agentPresets[0]
  const relationship = relationshipSeeds[preset.relationship]
  const traits: Record<string, number> = { ...defaultAgentTraits }
  for (const [trait, amount] of Object.entries(preset.traits)) {
    if (typeof amount === 'number') traits[trait] = amount
  }
  return {
    ...value,
    presetId: preset.id,
    preferences: preset.preferences,
    traits,
    personalization: {
      ...clonePersonalization(defaultAgentPersonalization),
      identity: { ...value.personalization.identity, selfDescription: preset.preferences },
      communicationStyle: { ...defaultCommunicationStyle, ...preset.style },
      emotionalDynamics: { ...defaultEmotionalDynamics, ...preset.dynamics, triggers: { ...value.personalization.emotionalDynamics.triggers }, soothingStrategies: [...value.personalization.emotionalDynamics.soothingStrategies] },
      relationshipSeed: { preset: preset.relationship, summary: relationship.summary, dimensions: { ...relationship.dimensions } },
      structuredBackstory: { ...value.personalization.structuredBackstory, narrative: value.backstory },
      evolutionPolicy: { ...value.personalization.evolutionPolicy, traitBounds: Object.fromEntries(Object.keys(traits).map((trait) => [trait, value.personalization.evolutionPolicy.traitBounds[trait] ?? { min: 0, max: 1 }])) },
    },
  }
}

const draftStorageKey = 'yuri.agent-profile-draft.v2'

export function saveAgentDraft(value: AgentProfileInput): void {
  if (typeof window === 'undefined') return
  try { window.localStorage.setItem(draftStorageKey, JSON.stringify(value)) } catch { /* private mode/quota: draft persistence is best effort */ }
}

export function loadAgentDraft(fallback: AgentProfileInput = defaultAgentDraft): AgentProfileInput {
  if (typeof window === 'undefined') return cloneAgentDraft(fallback)
  try {
    const encoded = window.localStorage.getItem(draftStorageKey)
    if (!encoded) return cloneAgentDraft(fallback)
    const source = JSON.parse(encoded) as Partial<AgentProfileInput>
    if (!source || typeof source !== 'object') return cloneAgentDraft(fallback)
    const base = cloneAgentDraft(fallback)
    const creationMode: AgentCreationMode = source.creationMode === 'advanced' ? 'advanced' : 'quick'
    return {
      ...base,
      ...source,
      creationMode,
      traits: { ...base.traits, ...normalizeAgentTraits(source.traits) },
      personalization: normalizePersonalizationInput(source.personalization, base.personalization),
    }
  } catch {
    return cloneAgentDraft(fallback)
  }
}

export function clearAgentDraft(): void {
  if (typeof window === 'undefined') return
  try { window.localStorage.removeItem(draftStorageKey) } catch { /* best effort */ }
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

function limitRunes(value: string, maxLength: number): string {
  return [...value].slice(0, maxLength).join('')
}

function record(value: unknown): UnknownRecord {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as UnknownRecord : {}
}

function numberValue(source: UnknownRecord, fallback: number, ...keys: string[]): number {
  for (const key of keys) {
    if (!(key in source)) continue
    return boundedTrait(source[key])
  }
  return fallback
}

function integerValue(source: UnknownRecord, fallback: number, ...keys: string[]): number {
  for (const key of keys) {
    if (!(key in source)) continue
    const value = Number(source[key])
    return Number.isFinite(value) ? Math.round(value) : fallback
  }
  return fallback
}

function stringValue(source: UnknownRecord, fallback: string, ...keys: string[]): string {
  for (const key of keys) {
    if (typeof source[key] === 'string') return source[key] as string
  }
  return fallback
}

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string').map((item) => item.trim()).filter(Boolean) : []
}

function normalizePersonalizationInput(value: unknown, fallback: AgentPersonalizationInput = defaultAgentPersonalization): AgentPersonalizationInput {
  const source = record(value)
  const identity = record(source.identity)
  const style = record(source.communicationStyle ?? source.communication_style)
  const dynamics = record(source.emotionalDynamics ?? source.emotional_dynamics)
  const relationship = record(source.relationshipSeed ?? source.relationship_seed)
  const backstory = record(source.structuredBackstory ?? source.structured_backstory ?? source.backstory)
  const policy = record(source.evolutionPolicy ?? source.evolution_policy)
  const triggerSource = record(dynamics.triggers)
  const triggers = Object.fromEntries(Object.entries(triggerSource).map(([emotion, items]) => [emotion, stringList(items)]).filter(([, items]) => items.length > 0))
  const episodeSource = Array.isArray(backstory.episodes) ? backstory.episodes : []
  const episodes = episodeSource.map((value, index) => {
    const episode = record(value)
    return {
      id: stringValue(episode, `episode-${index + 1}`, 'id'),
      title: stringValue(episode, '', 'title'),
      content: stringValue(episode, '', 'content'),
      kind: stringValue(episode, '', 'kind'),
      people: stringList(episode.people),
      place: stringValue(episode, '', 'place'),
      emotionalValence: Math.max(-1, Math.min(1, Number(episode.emotionalValence ?? episode.emotional_valence) || 0)),
      sequence: Math.max(0, Math.round(Number(episode.sequence) || index + 1)),
    }
  })
  const boundSource = record(policy.traitBounds ?? policy.trait_bounds)
  const traitBounds = Object.fromEntries(Object.entries(boundSource).map(([trait, value]) => {
    const bounds = record(value)
    const min = numberValue(bounds, 0, 'min')
    const max = Math.max(min, numberValue(bounds, 1, 'max'))
    return [trait, { min, max }]
  }))
  const relationshipPreset = stringValue(relationship, fallback.relationshipSeed.preset, 'preset') as RelationshipSeedPreset
  const conflictStyle = stringValue(dynamics, fallback.emotionalDynamics.conflictStyle, 'conflictStyle', 'conflict_style') as AgentPersonalizationInput['emotionalDynamics']['conflictStyle']
  return {
    identity: {
      preferredLanguage: stringValue(identity, fallback.identity.preferredLanguage, 'preferredLanguage', 'preferred_language'),
      pronouns: stringValue(identity, fallback.identity.pronouns, 'pronouns'),
      userAddress: stringValue(identity, fallback.identity.userAddress, 'userAddress', 'user_address'),
      selfDescription: stringValue(identity, fallback.identity.selfDescription, 'selfDescription', 'self_description'),
      role: stringValue(identity, fallback.identity.role, 'role'),
    },
    communicationStyle: {
      verbosity: numberValue(style, fallback.communicationStyle.verbosity, 'verbosity'),
      softness: numberValue(style, fallback.communicationStyle.softness, 'softness'),
      humor: numberValue(style, fallback.communicationStyle.humor, 'humor'),
      figurativeness: numberValue(style, fallback.communicationStyle.figurativeness, 'figurativeness'),
      expressiveness: numberValue(style, fallback.communicationStyle.expressiveness, 'expressiveness'),
      supportiveness: numberValue(style, fallback.communicationStyle.supportiveness, 'supportiveness'),
      formality: numberValue(style, fallback.communicationStyle.formality, 'formality'),
      teasing: numberValue(style, fallback.communicationStyle.teasing, 'teasing'),
      emojiFrequency: numberValue(style, fallback.communicationStyle.emojiFrequency, 'emojiFrequency', 'emoji_frequency'),
      flirtation: numberValue(style, fallback.communicationStyle.flirtation, 'flirtation'),
      conversationalInitiative: numberValue(style, fallback.communicationStyle.conversationalInitiative, 'conversationalInitiative', 'conversational_initiative'),
    },
    emotionalDynamics: {
      reactivity: numberValue(dynamics, fallback.emotionalDynamics.reactivity, 'reactivity'),
      responseIntensity: numberValue(dynamics, fallback.emotionalDynamics.responseIntensity, 'responseIntensity', 'response_intensity'),
      recoverySpeed: numberValue(dynamics, fallback.emotionalDynamics.recoverySpeed, 'recoverySpeed', 'recovery_speed'),
      positivePersistence: numberValue(dynamics, fallback.emotionalDynamics.positivePersistence, 'positivePersistence', 'positive_persistence'),
      negativePersistence: numberValue(dynamics, fallback.emotionalDynamics.negativePersistence, 'negativePersistence', 'negative_persistence'),
      expression: numberValue(dynamics, fallback.emotionalDynamics.expression, 'expression'),
      masking: numberValue(dynamics, fallback.emotionalDynamics.masking, 'masking'),
      conflictStyle,
      triggers: Object.keys(triggers).length > 0 ? triggers : Object.fromEntries(Object.entries(fallback.emotionalDynamics.triggers).map(([emotion, items]) => [emotion, [...items]])),
      soothingStrategies: stringList(dynamics.soothingStrategies ?? dynamics.soothing_strategies).length > 0 ? stringList(dynamics.soothingStrategies ?? dynamics.soothing_strategies) : [...fallback.emotionalDynamics.soothingStrategies],
    },
    relationshipSeed: {
      preset: relationshipPreset in relationshipSeeds ? relationshipPreset : fallback.relationshipSeed.preset,
      dimensions: { ...fallback.relationshipSeed.dimensions, ...normalizeAgentTraits(relationship.dimensions) },
      summary: stringValue(relationship, fallback.relationshipSeed.summary, 'summary'),
    },
    structuredBackstory: {
      narrative: limitRunes(stringValue(backstory, fallback.structuredBackstory.narrative, 'narrative'), AGENT_BACKSTORY_MAX_LENGTH),
      summary: limitRunes(stringValue(backstory, fallback.structuredBackstory.summary, 'summary'), 2_000),
      episodes,
    },
    evolutionPolicy: {
      lockedFields: stringList(policy.lockedFields ?? policy.locked_fields).length > 0 ? stringList(policy.lockedFields ?? policy.locked_fields) : [...fallback.evolutionPolicy.lockedFields],
      traitBounds: Object.keys(traitBounds).length > 0 ? traitBounds : Object.fromEntries(Object.entries(fallback.evolutionPolicy.traitBounds).map(([trait, bounds]) => [trait, { ...bounds }])),
      reflectionMode: (stringValue(policy, fallback.evolutionPolicy.reflectionMode, 'reflectionMode', 'reflection_mode') === 'disabled' ? 'disabled' : 'enabled'),
      reflectionCooldownMinutes: Math.max(1, Math.min(7 * 24 * 60, integerValue(policy, fallback.evolutionPolicy.reflectionCooldownMinutes, 'reflectionCooldownMinutes', 'reflection_cooldown_minutes'))),
      reflectionMaxTokens: Math.max(256, Math.min(10_000, integerValue(policy, fallback.evolutionPolicy.reflectionMaxTokens, 'reflectionMaxTokens', 'reflection_max_tokens'))),
      reflectionMaxDurationSeconds: Math.max(5, Math.min(120, integerValue(policy, fallback.evolutionPolicy.reflectionMaxDurationSeconds, 'reflectionMaxDurationSeconds', 'reflection_max_duration_seconds'))),
      reflectionMaxEvidence: Math.max(1, Math.min(32, integerValue(policy, fallback.evolutionPolicy.reflectionMaxEvidence, 'reflectionMaxEvidence', 'reflection_max_evidence'))),
    },
  }
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
    backstory: limitRunes(text(source, 'backstory', 'identityBackstory', 'identity_backstory') ?? '', AGENT_BACKSTORY_MAX_LENGTH),
    traits: normalizeAgentTraits(source.traits ?? source.initialTraits ?? source.initial_traits),
    active: source.active === true || source.isActive === true || source.is_active === true,
    createdAt: text(source, 'createdAt', 'created_at') ?? new Date(0).toISOString(),
    updatedAt: text(source, 'updatedAt', 'updated_at') ?? text(source, 'createdAt', 'created_at') ?? new Date(0).toISOString(),
  }
}

export function normalizeAgentPersonalizationProfile(value: unknown): AgentPersonalizationProfile | undefined {
  if (!value || typeof value !== 'object') return undefined
  const source = value as UnknownRecord
  const agentId = text(source, 'agentId', 'agent_id')
  const revisionId = text(source, 'revisionId', 'revision_id')
  if (!agentId || !revisionId) return undefined
  const normalized = normalizePersonalizationInput({
    identity: source.identity,
    communicationStyle: source.communicationStyle ?? source.communication_style,
    emotionalDynamics: source.emotionalDynamics ?? source.emotional_dynamics,
    relationshipSeed: source.relationshipSeed ?? source.relationship_seed,
    structuredBackstory: source.structuredBackstory ?? source.structured_backstory ?? source.backstory,
    evolutionPolicy: source.evolutionPolicy ?? source.evolution_policy,
  })
  const temperamentSource = record(source.temperament)
  const custom = normalizeAgentTraits(temperamentSource.custom)
  const temperament = { ...normalizeAgentTraits(Object.fromEntries(Object.entries(temperamentSource).filter(([key]) => key !== 'custom'))), ...custom }
  return {
    ...normalized,
    agentId,
    schemaVersion: Math.max(0, Math.round(Number(source.schemaVersion ?? source.schema_version) || 0)),
    version: Math.max(0, Math.round(Number(source.version) || 0)),
    revisionId,
    operation: text(source, 'operation') ?? '',
    reason: text(source, 'reason') ?? '',
    createdAt: text(source, 'createdAt', 'created_at') ?? new Date(0).toISOString(),
    updatedAt: text(source, 'updatedAt', 'updated_at') ?? new Date(0).toISOString(),
    temperament,
  }
}

export function validateAgentDraft(input: AgentProfileInput): string | undefined {
  const nameLength = [...input.name.trim()].length
  if (nameLength === 0) return 'Укажите имя агента.'
  if (nameLength > 64) return 'Имя агента должно быть короче 65 символов.'
  if (input.age !== undefined && (!Number.isInteger(input.age) || input.age < 1 || input.age > 200)) return 'Возраст должен быть целым числом от 1 до 200.'
  if (!input.gender.trim()) return 'Укажите пол или гендер агента.'
  if ([...input.preferences.trim()].length > 2000) return 'Краткие предпочтения должны быть короче 2001 символа.'
  if ([...(input.backstory ?? '').trim()].length > AGENT_BACKSTORY_MAX_LENGTH) return `Предыстория должна быть короче ${AGENT_BACKSTORY_MAX_LENGTH + 1} символа.`
  if ([...input.personalization.identity.preferredLanguage.trim()].length > 64) return 'Язык должен быть короче 65 символов.'
  if ([...input.personalization.identity.pronouns.trim()].length > 64) return 'Местоимения должны быть короче 65 символов.'
  if ([...input.personalization.identity.userAddress.trim()].length > 128) return 'Обращение к пользователю должно быть короче 129 символов.'
  if ([...input.personalization.identity.selfDescription.trim()].length > 2000) return 'Описание образа должно быть короче 2001 символа.'
  if ([...input.personalization.structuredBackstory.summary.trim()].length > 2000) return 'Краткое резюме backstory должно быть короче 2001 символа.'
  if (input.personalization.structuredBackstory.episodes.length > 64) return 'В backstory может быть не больше 64 эпизодов.'
  if (input.personalization.structuredBackstory.episodes.some((episode) => !episode.id.trim() || !episode.content.trim())) return 'Каждому эпизоду нужны ID и содержание.'
  return undefined
}
