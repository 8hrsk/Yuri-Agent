/**
 * The renderer only ever receives bounded, versioned persona state.  These
 * contracts deliberately keep the subjective relationship model separate
 * from factual memories and from the immutable policy layer.
 */
export type AvatarState = 'idle' | 'listening' | 'thinking' | 'speaking' | 'tool_running' | 'error'

/**
 * Trait ids the renderer knows by name, plus any other the backend invents.
 *
 * The open half is written `(string & {})` rather than a bare `string`: a bare
 * `string` in a union of string literals *absorbs* them, so the whole alias
 * collapses to `string` and the listed members buy neither autocomplete nor
 * documentation value (N-19). The intersection with the empty object type is
 * assignable from and to `string` exactly as before — the contract is unchanged
 * — but it is not a literal supertype, so the members survive reduction.
 *
 * The union stays open on purpose. `domain.CommonPersonaTraits` is described in
 * Go as "the stable set used by the default seed", with custom snake_case trait
 * names explicitly allowed, so a closed union would reject valid backend state.
 */
export type PersonaTraitId =
  | 'warmth'
  | 'trust'
  | 'attachment'
  | 'jealousy'
  | 'irritability'
  | 'romantic_tone'
  | 'emotionality'
  | 'directness'
  | 'playfulness'
  | 'formality'
  | 'initiative'
  | 'empathy'
  | 'sociability'
  | 'shyness'
  | 'anxiety'
  | 'fearfulness'
  | 'emotional_stability'
  | 'sensitivity'
  | 'possessiveness'
  | 'impulsivity'
  | 'stubbornness'
  | 'optimism'
  | 'curiosity'
  | 'suspicion'
  | 'tsundere'
  | (string & {})

export interface PersonaTrait {
  id: PersonaTraitId
  label: string
  /** Normalized value in the inclusive 0..1 range. */
  value: number
  /** Reflection/UI bounds; values outside this interval are rejected by the decoder. */
  min: number
  max: number
  pinned: boolean
  description?: string
  updatedAt?: string
}

export interface PersonaEvidence {
  id?: string
  sourceType: string
  sourceId?: string
  conversationId?: string
  conversationTitle?: string
  messageId?: string
  runId?: string
  excerpt?: string
  excerptHash?: string
  provenance?: string
  weight?: number
  userConfirmed?: boolean
  createdAt?: string
}

export type SubjectiveLabel = 'opinion' | 'inference'

export interface SubjectiveOpinion {
  id: string
  subject: string
  content: string
  /** This label is intentionally explicit so the UI cannot render an opinion as fact. */
  label: SubjectiveLabel
  confidence: number
  evidence: PersonaEvidence[]
  reason?: string
  createdAt?: string
  updatedAt?: string
}

/**
 * Emotion names the renderer knows, plus any other the backend reports. Open
 * for the same reason as `PersonaTraitId`, and written the same way so the
 * literals are not absorbed — `internal/domain/affect.go` states outright that
 * "emotion names are extensible".
 */
export type AffectEmotion =
  | 'sympathy'
  | 'tenderness'
  | 'joy'
  | 'gratitude'
  | 'longing'
  | 'boredom'
  | 'anger'
  | 'irritation'
  | 'jealousy'
  | 'resentment'
  | 'anxiety'
  | 'fear'
  | 'embarrassment'
  | (string & {})

export interface AffectiveDimension {
  id: AffectEmotion
  label: string
  /** Intensity in the inclusive 0..1 range. */
  value: number
  /** Optional signed contribution, where negative values are unpleasant. */
  valence?: number
}

export interface AffectiveState {
  id?: string
  version?: number
  mood: string
  valence: number
  arousal: number
  intensity: number
  dimensions: AffectiveDimension[]
  reason?: string
  evidence?: PersonaEvidence[]
  updatedAt?: string
}

export interface RelationshipDimension {
  id: string
  label: string
  value: number
}

export interface RelationshipVersion {
  id: string
  version: number
  parentId?: string
  operation: 'create' | 'update' | 'rollback' | 'reset' | string
  summary: string
  dimensions: Record<string, number>
  diff?: Record<string, number>
  reason: string
  evidence: PersonaEvidence[]
  authorRunId?: string
  createdAt: string
}

export interface RelationshipState {
  id: string
  version: number
  summary: string
  dimensions: RelationshipDimension[]
  opinions: SubjectiveOpinion[]
  affect: AffectiveState
  reason?: string
  evidence?: PersonaEvidence[]
  versions: RelationshipVersion[]
  updatedAt?: string
}

export interface PersonaVersion {
  id: string
  version: number
  parentId?: string
  traits: PersonaTrait[]
  diff?: Record<string, unknown>
  promptText?: string
  reason: string
  evidence: PersonaEvidence[]
  authorRunId?: string
  createdAt: string
}

export interface PersonalitySnapshot {
  id: string
  currentVersion: number
  currentVersionId?: string
  traits: PersonaTrait[]
  pinnedTraits: string[]
  opinions: SubjectiveOpinion[]
  affect: AffectiveState
  relationship: RelationshipState
  versions: PersonaVersion[]
  autoEvolution: boolean
  lastReflectionAt?: string
}

/** Alias used by callers that use the domain term rather than the UI label. */
export type PersonaSnapshot = PersonalitySnapshot
