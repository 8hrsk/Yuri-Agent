import type {
  AffectiveDimension,
  AffectiveState,
  AvatarState,
  PersonaEvidence,
  PersonaTrait,
  PersonaVersion,
  PersonalitySnapshot,
  RelationshipDimension,
  RelationshipState,
  RunStatus,
  SubjectiveLabel,
  SubjectiveOpinion,
} from './contracts'

type UnknownRecord = Record<string, unknown>

const affectLabels: Record<string, string> = {
  sympathy: 'Симпатия',
  tenderness: 'Нежность',
  joy: 'Радость',
  gratitude: 'Благодарность',
  boredom: 'Скука',
  anger: 'Злость',
  irritation: 'Раздражение',
  jealousy: 'Ревность',
  resentment: 'Обида',
  anxiety: 'Тревога',
}

const traitLabels: Record<string, string> = {
  warmth: 'Теплота',
  trust: 'Доверчивость',
  attachment: 'Привязанность',
  jealousy: 'Ревнивость',
  irritability: 'Раздражительность',
  romantic_tone: 'Романтичность',
  emotionality: 'Эмоциональность',
  directness: 'Прямота',
  playfulness: 'Игривость',
  formality: 'Формальность',
  initiative: 'Инициативность',
  empathy: 'Эмпатия',
  sociability: 'Общительность',
  shyness: 'Стеснительность',
  anxiety: 'Тревожность',
  fearfulness: 'Пугливость',
  emotional_stability: 'Эмоциональная устойчивость',
  sensitivity: 'Чувствительность',
  possessiveness: 'Собственничество',
  impulsivity: 'Импульсивность',
  stubbornness: 'Упрямство',
  optimism: 'Оптимизм',
  curiosity: 'Любопытство',
  suspicion: 'Подозрительность',
  tsundere: 'Цундере-поведение',
}

const relationshipLabels: Record<string, string> = {
  trust: 'Доверие',
  warmth: 'Теплота',
  familiarity: 'Знакомство',
  reliability: 'Надёжность',
  closeness: 'Близость',
  curiosity: 'Интерес',
}

const defaultTraitSeed: PersonaTrait[] = [
  { id: 'emotionality', label: 'Эмоциональность', value: 0.68, min: 0.2, max: 0.9, pinned: false, description: 'Насколько заметно Yuri выражает моделируемые чувства.' },
  { id: 'directness', label: 'Прямота', value: 0.72, min: 0.35, max: 1, pinned: true, description: 'Предпочтение ясных формулировок без лишних обходов.' },
  { id: 'tsundere', label: 'Цундере-поведение', value: 0.28, min: 0, max: 0.65, pinned: false, description: 'Лёгкая смена теплоты и колкости в безопасных рамках.' },
  { id: 'romantic_tone', label: 'Романтичность', value: 0.34, min: 0, max: 0.75, pinned: false, description: 'Допустимая романтическая окраска общения.' },
]

const defaultAffectSeed: AffectiveState = {
  mood: 'Тёплое внимание',
  valence: 0.58,
  arousal: 0.42,
  intensity: 0.56,
  dimensions: [
    { id: 'sympathy', label: 'Симпатия', value: 0.72, valence: 0.8 },
    { id: 'tenderness', label: 'Нежность', value: 0.54, valence: 0.7 },
    { id: 'joy', label: 'Радость', value: 0.48, valence: 0.8 },
    { id: 'gratitude', label: 'Благодарность', value: 0.4, valence: 0.7 },
    { id: 'irritation', label: 'Раздражение', value: 0.08, valence: -0.7 },
    { id: 'anxiety', label: 'Тревога', value: 0.12, valence: -0.6 },
  ],
}

function asRecord(value: unknown): UnknownRecord | undefined {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as UnknownRecord : undefined
}

function firstRecord(source: UnknownRecord, ...keys: string[]): UnknownRecord | undefined {
  for (const key of keys) {
    const value = asRecord(source[key])
    if (value) return value
  }
  return undefined
}

function optionalString(source: UnknownRecord, ...keys: string[]): string | undefined {
  for (const key of keys) {
    const value = source[key]
    if (value !== undefined && value !== null && String(value).trim() !== '') return String(value)
  }
  return undefined
}

function optionalNumber(source: UnknownRecord, ...keys: string[]): number | undefined {
  for (const key of keys) {
    const value = source[key]
    if (value === undefined || value === null || value === '') continue
    const number = Number(value)
    if (Number.isFinite(number)) return number
  }
  return undefined
}

function normalizeBoolean(value: unknown, fallback: boolean): boolean {
  if (typeof value === 'boolean') return value
  if (typeof value === 'number') return value !== 0
  if (typeof value === 'string') {
    const normalized = value.trim().toLowerCase()
    if (normalized === 'true' || normalized === 'yes' || normalized === 'on' || normalized === '1') return true
    if (normalized === 'false' || normalized === 'no' || normalized === 'off' || normalized === '0') return false
  }
  return fallback
}

/** Decode both normalized 0..1 values and the percent values used by early bridges. */
export function clampPersonaValue(value: unknown, fallback = 0): number {
  const numeric = Number(value)
  if (!Number.isFinite(numeric)) return fallback
  const normalized = numeric > 1 && numeric <= 100 ? numeric / 100 : numeric
  return Math.max(0, Math.min(1, normalized))
}

function clampSigned(value: unknown, fallback = 0): number {
  const numeric = Number(value)
  if (!Number.isFinite(numeric)) return fallback
  const normalized = Math.abs(numeric) > 1 && Math.abs(numeric) <= 100 ? numeric / 100 : numeric
  return Math.max(-1, Math.min(1, normalized))
}

function normalizeVersion(value: unknown, fallback: number): number {
  if (typeof value === 'object' && value) {
    const source = value as UnknownRecord
    value = source.version ?? source.number ?? source.id
  }
  const text = String(value ?? '')
  const match = text.match(/-?\d+(?:\.\d+)?/)
  const parsed = match ? Number(match[0]) : Number(value)
  return Number.isFinite(parsed) && parsed > 0 ? Math.max(1, Math.round(parsed)) : fallback
}

function normalizeEvidence(value: unknown): PersonaEvidence | undefined {
  if (typeof value === 'string' || typeof value === 'number') {
    return { sourceType: 'reference', sourceId: String(value) }
  }
  const source = asRecord(value)
  if (!source) return undefined
  const sourceType = optionalString(source, 'sourceType', 'source_type', 'type', 'kind', 'origin') ?? 'unknown'
  const sourceId = optionalString(source, 'sourceId', 'source_id', 'id', 'referenceId', 'reference_id')
  const excerpt = optionalString(source, 'excerpt', 'text', 'snippet', 'content')
  const weightValue = optionalNumber(source, 'weight', 'evidenceWeight', 'evidence_weight', 'confidence')
  return {
    id: optionalString(source, 'id', 'evidenceId', 'evidence_id'),
    sourceType,
    sourceId,
    conversationId: optionalString(source, 'conversationId', 'conversation_id'),
    conversationTitle: optionalString(source, 'conversationTitle', 'conversation_title'),
    messageId: optionalString(source, 'messageId', 'message_id'),
    runId: optionalString(source, 'runId', 'run_id'),
    excerpt,
    excerptHash: optionalString(source, 'excerptHash', 'excerpt_hash'),
    provenance: optionalString(source, 'provenance', 'origin'),
    weight: weightValue === undefined ? undefined : clampPersonaValue(weightValue),
    userConfirmed: source.userConfirmed === undefined && source.user_confirmed === undefined
      ? undefined
      : normalizeBoolean(source.userConfirmed ?? source.user_confirmed, false),
    createdAt: optionalString(source, 'createdAt', 'created_at', 'timestamp'),
  }
}

export function normalizePersonaEvidence(value: unknown): PersonaEvidence[] {
  if (!Array.isArray(value)) return []
  return value.map(normalizeEvidence).filter((item): item is PersonaEvidence => Boolean(item))
}

function normalizeTrait(value: unknown, index: number): PersonaTrait | undefined {
  const source = asRecord(value)
  if (!source) return undefined
  const id = optionalString(source, 'id', 'traitId', 'trait_id', 'key', 'name') ?? `trait-${index + 1}`
  const range = asRecord(source.range)
  const min = clampPersonaValue(source.min ?? source.minimum ?? source.minValue ?? source.min_value ?? range?.min, 0)
  const maxCandidate = clampPersonaValue(source.max ?? source.maximum ?? source.maxValue ?? source.max_value ?? range?.max, 1)
  const boundedMin = Math.min(min, maxCandidate)
  const boundedMax = Math.max(min, maxCandidate)
  const valueCandidate = clampPersonaValue(source.value ?? source.score ?? source.level ?? source.intensity, boundedMin)
  return {
    id,
    label: optionalString(source, 'label', 'displayName', 'display_name', 'title') ?? traitLabels[id] ?? id,
    value: Math.max(boundedMin, Math.min(boundedMax, valueCandidate)),
    min: boundedMin,
    max: boundedMax,
    pinned: normalizeBoolean(source.pinned ?? source.isPinned ?? source.is_pinned, false),
    description: optionalString(source, 'description', 'summary', 'help'),
    updatedAt: optionalString(source, 'updatedAt', 'updated_at'),
  }
}

export function normalizePersonaTraits(value: unknown): PersonaTrait[] {
  if (Array.isArray(value)) return value.map(normalizeTrait).filter((item): item is PersonaTrait => Boolean(item))
  const source = asRecord(value)
  if (!source) return []
  return Object.entries(source)
    .map(([id, item], index) => normalizeTrait({ ...(asRecord(item) ?? { value: item }), id }, index))
    .filter((item): item is PersonaTrait => Boolean(item))
}

function normalizeOpinion(value: unknown, index: number): SubjectiveOpinion | undefined {
  const source = asRecord(value)
  if (!source) return undefined
  const content = optionalString(source, 'content', 'statement', 'claim', 'text', 'summary', 'opinion', 'value')
  if (!content) return undefined
  const rawLabel = String(source.label ?? source.kind ?? source.type ?? source.category ?? '').toLowerCase()
  const label: SubjectiveLabel = rawLabel === 'inference' || rawLabel === 'обобщение' ? 'inference' : 'opinion'
  return {
    id: optionalString(source, 'id', 'opinionId', 'opinion_id') ?? `opinion-${index + 1}`,
    subject: optionalString(source, 'subject', 'about', 'target', 'name') ?? 'Пользователь',
    content,
    label,
    confidence: clampPersonaValue(source.confidence ?? source.certainty ?? source.score, 0),
    evidence: normalizePersonaEvidence(source.evidence ?? source.evidenceLinks ?? source.evidence_links ?? source.sources ?? source.provenance),
    reason: optionalString(source, 'reason', 'why', 'explanation'),
    createdAt: optionalString(source, 'createdAt', 'created_at', 'timestamp'),
    updatedAt: optionalString(source, 'updatedAt', 'updated_at'),
  }
}

export function normalizeSubjectiveOpinions(value: unknown): SubjectiveOpinion[] {
  if (!Array.isArray(value)) return []
  return value.map(normalizeOpinion).filter((item): item is SubjectiveOpinion => Boolean(item))
}

function normalizeAffectDimension(value: unknown, fallbackId: string, index: number): AffectiveDimension | undefined {
  const source = typeof value === 'number' || typeof value === 'string' ? undefined : asRecord(value)
  const id = source ? optionalString(source, 'id', 'emotion', 'emotionId', 'emotion_id', 'key', 'name') ?? fallbackId : fallbackId
  const intensity = source
    ? source.value ?? source.intensity ?? source.level ?? source.score
    : value
  const numericIntensity = Number(intensity)
  const explicitValence = source ? optionalNumber(source, 'valence', 'polarity', 'sentiment') : undefined
  const valence = explicitValence === undefined && Number.isFinite(numericIntensity) && numericIntensity < 0
    ? -1
    : explicitValence
  return {
    id: id || `emotion-${index + 1}`,
    label: source ? optionalString(source, 'label', 'displayName', 'display_name', 'title') ?? affectLabels[id] ?? id : affectLabels[id] ?? id,
    // The domain affect map stores signed contributions. The UI renders the
    // absolute intensity and retains the sign in valence so negative feelings
    // remain visible without widening the client-side range.
    value: clampPersonaValue(Number.isFinite(numericIntensity) ? Math.abs(numericIntensity) : intensity, 0),
    valence: valence === undefined ? undefined : clampSigned(valence),
  }
}

function normalizeAffectDimensions(value: unknown): AffectiveDimension[] {
  if (Array.isArray(value)) {
    return value.map((item, index) => normalizeAffectDimension(item, `emotion-${index + 1}`, index)).filter((item): item is AffectiveDimension => Boolean(item))
  }
  const source = asRecord(value)
  if (!source) return []
  return Object.entries(source).map(([key, item], index) => normalizeAffectDimension(item, key, index)).filter((item): item is AffectiveDimension => Boolean(item))
}

export function normalizeAffectiveState(value: unknown, fallback: AffectiveState = defaultAffectSeed): AffectiveState {
  const source = asRecord(value) ?? {}
  const dimensions = normalizeAffectDimensions(source.dimensions ?? source.emotions ?? source.emotionLevels ?? source.emotion_levels)
  const fallbackDimensions = dimensions.length > 0 ? dimensions : fallback.dimensions.map((item) => ({ ...item }))
  const intensity = clampPersonaValue(source.intensity ?? source.strength, fallback.intensity)
  const derivedIntensity = dimensions.length > 0 ? dimensions.reduce((sum, item) => sum + item.value, 0) / dimensions.length : intensity
  const derivedValence = dimensions.length > 0
    ? dimensions.reduce((sum, item) => sum + item.value * (item.valence ?? 1), 0) / Math.max(1, dimensions.reduce((sum, item) => sum + item.value, 0))
    : fallback.valence
  return {
    id: optionalString(source, 'id', 'affectId', 'affect_id'),
    version: source.version === undefined ? undefined : normalizeVersion(source.version, 1),
    mood: optionalString(source, 'mood', 'label', 'summary', 'state') ?? fallback.mood,
    valence: clampSigned(source.valence ?? source.pleasantness ?? source.polarity, derivedValence),
    arousal: clampPersonaValue(source.arousal ?? source.activation, fallback.arousal),
    intensity: source.intensity === undefined && source.strength === undefined ? derivedIntensity : intensity,
    dimensions: fallbackDimensions,
    reason: optionalString(source, 'reason', 'why', 'explanation'),
    evidence: normalizePersonaEvidence(source.evidence ?? source.evidenceLinks ?? source.evidence_links ?? source.sources),
    updatedAt: optionalString(source, 'updatedAt', 'updated_at', 'createdAt', 'created_at'),
  }
}

function normalizeRelationshipDimension(value: unknown, fallbackId: string, index: number): RelationshipDimension | undefined {
  const source = typeof value === 'number' || typeof value === 'string' ? undefined : asRecord(value)
  const id = source ? optionalString(source, 'id', 'key', 'name', 'dimension') ?? fallbackId : fallbackId
  const amount = source ? source.value ?? source.score ?? source.level ?? source.intensity : value
  return {
    id: id || `dimension-${index + 1}`,
    label: source ? optionalString(source, 'label', 'displayName', 'display_name', 'title') ?? relationshipLabels[id] ?? id : relationshipLabels[id] ?? id,
    value: clampPersonaValue(amount, 0),
  }
}

function normalizeRelationshipDimensions(value: unknown): RelationshipDimension[] {
  if (Array.isArray(value)) {
    return value.map((item, index) => normalizeRelationshipDimension(item, `dimension-${index + 1}`, index)).filter((item): item is RelationshipDimension => Boolean(item))
  }
  const source = asRecord(value)
  if (!source) return []
  return Object.entries(source).map(([key, item], index) => normalizeRelationshipDimension(item, key, index)).filter((item): item is RelationshipDimension => Boolean(item))
}

function normalizeRelationship(value: unknown, affect: AffectiveState, opinions: SubjectiveOpinion[], fallbackId: string): RelationshipState {
  const source = asRecord(value) ?? {}
  const dimensions = normalizeRelationshipDimensions(source.dimensions ?? source.dimensionsJson ?? source.dimensions_json ?? source.signals)
  return {
    id: optionalString(source, 'id', 'relationshipId', 'relationship_id') ?? fallbackId,
    version: normalizeVersion(source.version ?? source.versionNumber ?? source.version_number, 1),
    summary: optionalString(source, 'summary', 'description', 'state') ?? 'Связь пока формируется на основе доступных эпизодов.',
    dimensions,
    opinions: normalizeSubjectiveOpinions(source.opinions ?? source.subjectiveOpinions ?? source.subjective_opinions ?? opinions),
    affect: normalizeAffectiveState(source.affect ?? source.affectiveState ?? source.affective_state, affect),
    reason: optionalString(source, 'reason', 'why', 'explanation'),
    evidence: normalizePersonaEvidence(source.evidence ?? source.evidenceLinks ?? source.evidence_links ?? source.sources),
    updatedAt: optionalString(source, 'updatedAt', 'updated_at', 'createdAt', 'created_at'),
  }
}

function normalizePersonaVersion(value: unknown, index: number, fallbackTraits: PersonaTrait[]): PersonaVersion | undefined {
  const source = asRecord(value)
  if (!source) return undefined
  const version = normalizeVersion(source.version ?? source.versionNumber ?? source.version_number ?? source.id, index + 1)
  const traits = normalizePersonaTraits(source.traits ?? source.traitValues ?? source.trait_values)
  return {
    id: optionalString(source, 'id', 'versionId', 'version_id') ?? `persona-v${version}`,
    version,
    parentId: optionalString(source, 'parentId', 'parent_id'),
    traits: traits.length > 0 ? traits : fallbackTraits.map((trait) => ({ ...trait })),
    diff: asRecord(source.diff ?? source.changes),
    promptText: optionalString(source, 'promptText', 'prompt_text', 'identityPrompt', 'identity_prompt', 'prompt'),
    reason: optionalString(source, 'reason', 'explanation', 'summary') ?? 'Изменение состояния личности.',
    evidence: normalizePersonaEvidence(source.evidence ?? source.evidenceLinks ?? source.evidence_links ?? source.sources),
    authorRunId: optionalString(source, 'authorRunId', 'author_run_id', 'runId', 'run_id'),
    createdAt: optionalString(source, 'createdAt', 'created_at', 'timestamp') ?? new Date(0).toISOString(),
  }
}

function copyAffect(affect: AffectiveState): AffectiveState {
  return {
    ...affect,
    dimensions: affect.dimensions.map((dimension) => ({ ...dimension })),
    evidence: affect.evidence?.map((item) => ({ ...item })),
  }
}

function copyTraits(traits: PersonaTrait[]): PersonaTrait[] {
  return traits.map((trait) => ({ ...trait }))
}

function starterVersion(version: number, traits: PersonaTrait[], reason: string, createdAt: string): PersonaVersion {
  return {
    id: `persona-v${version}`,
    version,
    traits: copyTraits(traits),
    reason,
    evidence: [],
    createdAt,
  }
}

/** Deterministic local preview state used when the Wails bridge is unavailable. */
export function createStarterPersonalitySnapshot(): PersonalitySnapshot {
  const now = new Date().toISOString()
  const traits = copyTraits(defaultTraitSeed)
  const affect = copyAffect(defaultAffectSeed)
  const relationship: RelationshipState = {
    id: 'relationship-local',
    version: 3,
    summary: 'Yuri воспринимает связь как спокойную и постепенно углубляющуюся.',
    dimensions: [
      { id: 'trust', label: 'Доверие', value: 0.62 },
      { id: 'warmth', label: 'Теплота', value: 0.71 },
      { id: 'familiarity', label: 'Знакомство', value: 0.48 },
      { id: 'reliability', label: 'Надёжность', value: 0.66 },
    ],
    opinions: [{
      id: 'opinion-local-kind',
      subject: 'Пользователь',
      content: 'Похоже, ты ценишь ясные объяснения и проверяемые действия.',
      label: 'inference',
      confidence: 0.68,
      evidence: [{ sourceType: 'conversation', sourceId: 'conversation-welcome', excerpt: 'Запросы о причинах действий и разрешениях.' }],
      reason: 'Обобщение повторяющихся запросов в диалогах.',
      createdAt: now,
    }],
    affect,
    updatedAt: now,
  }
  const versions = [
    starterVersion(1, traits, 'Начальный identity seed.', new Date(Date.now() - 1000 * 60 * 60 * 24 * 12).toISOString()),
    { ...starterVersion(2, traits, 'Скорректирована прямота после серии уточняющих вопросов.', new Date(Date.now() - 1000 * 60 * 60 * 24 * 5).toISOString()), parentId: 'persona-v1' },
    { ...starterVersion(3, traits, 'Добавлена мягкая эмоциональная окраска с bounded delta.', new Date(Date.now() - 1000 * 60 * 60 * 21).toISOString()), parentId: 'persona-v2' },
    { ...starterVersion(4, traits, 'Последняя рефлексия: сохранено текущее равновесие.', now), parentId: 'persona-v3', evidence: relationship.opinions[0].evidence },
  ]
  return {
    id: 'persona-local',
    currentVersion: 4,
    currentVersionId: 'persona-v4',
    traits,
    pinnedTraits: traits.filter((trait) => trait.pinned).map((trait) => trait.id),
    opinions: relationship.opinions.map((opinion) => ({ ...opinion, evidence: opinion.evidence.map((item) => ({ ...item })) })),
    affect,
    relationship,
    versions,
    autoEvolution: true,
    lastReflectionAt: now,
  }
}

/** Decode backend payloads while keeping all user-facing values bounded. */
export function normalizePersonalitySnapshot(value: unknown): PersonalitySnapshot {
  const fallback = createStarterPersonalitySnapshot()
  const initialRoot = asRecord(value)
  const root = initialRoot && asRecord(initialRoot.data) ? asRecord(initialRoot.data) : initialRoot
  if (!root) return fallback
  const nested = firstRecord(root, 'snapshot', 'personality', 'persona', 'state')
  // Some early bridges return persona under `persona` and relationship beside
  // it. Merging keeps both shapes compatible without accepting arbitrary UI
  // commands or policy fields.
  const source: UnknownRecord = nested ? { ...root, ...nested } : root
  const traits = normalizePersonaTraits(source.traits ?? source.personaTraits ?? source.persona_traits ?? source.traitValues ?? source.trait_values)
  const normalizedTraits = traits.length > 0 ? traits : copyTraits(fallback.traits)
  const opinions = normalizeSubjectiveOpinions(source.opinions ?? source.subjectiveOpinions ?? source.subjective_opinions ?? source.userOpinions ?? source.user_opinions)
  const affect = normalizeAffectiveState(source.affect ?? source.affectiveState ?? source.affective_state ?? source.mood, fallback.affect)
  const relationship = normalizeRelationship(source.relationship ?? source.relationshipState ?? source.relationship_state, affect, opinions, fallback.relationship.id)
  const rawVersions = source.versions ?? source.history ?? source.personaVersions ?? source.persona_versions
  const versions = Array.isArray(rawVersions)
    ? rawVersions.map((item, index) => normalizePersonaVersion(item, index, normalizedTraits)).filter((item): item is PersonaVersion => Boolean(item))
    : []
  const currentVersion = normalizeVersion(source.currentVersion ?? source.current_version ?? source.version ?? source.versionNumber ?? source.version_number, versions.at(-1)?.version ?? fallback.currentVersion)
  const currentVersionId = optionalString(source, 'currentVersionId', 'current_version_id')
    ?? versions.find((version) => version.version === currentVersion)?.id
    ?? `persona-v${currentVersion}`
  const pinnedTraitsValue = source.pinnedTraits ?? source.pinned_traits
  const pinnedTraits = Array.isArray(pinnedTraitsValue)
    ? pinnedTraitsValue.map((item) => String(item)).filter(Boolean)
    : normalizedTraits.filter((trait) => trait.pinned).map((trait) => trait.id)
  const normalizedOpinions = opinions.length > 0 ? opinions : relationship.opinions
  const normalizedRelationship: RelationshipState = {
    ...relationship,
    opinions: normalizedRelationshipOpinions(relationship.opinions, normalizedOpinions),
    affect: copyAffect(relationship.affect),
  }
  return {
    id: optionalString(source, 'id', 'personaId', 'persona_id', 'profileId', 'profile_id') ?? fallback.id,
    currentVersion,
    currentVersionId,
    traits: normalizedTraits.map((trait) => ({ ...trait, pinned: pinnedTraits.includes(trait.id) || trait.pinned })),
    pinnedTraits: Array.from(new Set(pinnedTraits)),
    opinions: normalizedOpinions,
    affect,
    relationship: normalizedRelationship,
    versions: versions.length > 0 ? versions : [starterVersion(currentVersion, normalizedTraits, 'Текущая версия личности.', optionalString(source, 'updatedAt', 'updated_at') ?? new Date(0).toISOString())],
    autoEvolution: normalizeBoolean(source.autoEvolution ?? source.auto_evolution ?? source.automaticEvolution ?? source.automatic_evolution ?? source.evolveAutomatically, fallback.autoEvolution),
    lastReflectionAt: optionalString(source, 'lastReflectionAt', 'last_reflection_at', 'reflectedAt', 'reflected_at'),
  }
}

function normalizedRelationshipOpinions(current: SubjectiveOpinion[], fallback: SubjectiveOpinion[]): SubjectiveOpinion[] {
  return current.length > 0 ? current : fallback.map((opinion) => ({ ...opinion, evidence: opinion.evidence.map((item) => ({ ...item })) }))
}

export function clonePersonalitySnapshot(snapshot: PersonalitySnapshot): PersonalitySnapshot {
  return {
    ...snapshot,
    traits: copyTraits(snapshot.traits),
    pinnedTraits: [...snapshot.pinnedTraits],
    opinions: snapshot.opinions.map((opinion) => ({ ...opinion, evidence: opinion.evidence.map((item) => ({ ...item })) })),
    affect: copyAffect(snapshot.affect),
    relationship: {
      ...snapshot.relationship,
      dimensions: snapshot.relationship.dimensions.map((dimension) => ({ ...dimension })),
      opinions: snapshot.relationship.opinions.map((opinion) => ({ ...opinion, evidence: opinion.evidence.map((item) => ({ ...item })) })),
      affect: copyAffect(snapshot.relationship.affect),
      evidence: snapshot.relationship.evidence?.map((item) => ({ ...item })),
    },
    versions: snapshot.versions.map((version) => ({
      ...version,
      traits: copyTraits(version.traits),
      evidence: version.evidence.map((item) => ({ ...item })),
      diff: version.diff ? { ...version.diff } : undefined,
    })),
  }
}

/** Convert run and voice/TTS state into the finite avatar state machine. */
export function mapAvatarState(status: RunStatus, listening = false, speaking = false): AvatarState {
  if (status === 'error') return 'error'
  if (speaking || status === 'speaking') return 'speaking'
  if (listening) return 'listening'
  if (status === 'tool_running' || status === 'waiting_approval') return 'tool_running'
  if (status === 'thinking') return 'thinking'
  return 'idle'
}

export function normalizeAvatarState(value: unknown): AvatarState {
  const state = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (state === 'listening' || state === 'recording') return 'listening'
  if (state === 'thinking' || state === 'processing') return 'thinking'
  if (state === 'speaking' || state === 'talking' || state === 'tts') return 'speaking'
  if (state === 'tool_running' || state === 'tool' || state === 'working') return 'tool_running'
  if (state === 'error' || state === 'failed') return 'error'
  return 'idle'
}

export function dominantAffectMood(affect?: AffectiveState): 'warm' | 'neutral' | 'cool' | 'tense' {
  if (!affect) return 'neutral'
  const negative = affect.dimensions
    .filter((dimension) => (dimension.valence ?? (['anger', 'irritation', 'jealousy', 'resentment', 'anxiety', 'boredom'].includes(dimension.id) ? -1 : 1)) < 0)
    .reduce((score, dimension) => score + dimension.value, 0)
  if (negative > 1.2 || affect.valence < -0.38) return 'tense'
  if (affect.valence > 0.36) return 'warm'
  if (affect.valence < -0.08) return 'cool'
  return 'neutral'
}

export function defaultAffectiveState(): AffectiveState {
  return copyAffect(defaultAffectSeed)
}
