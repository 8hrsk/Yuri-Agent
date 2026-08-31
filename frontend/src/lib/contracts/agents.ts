export type AgentCreationMode = 'quick' | 'advanced'

export type RelationshipSeedPreset =
  | 'new_acquaintances'
  | 'acquaintances'
  | 'friends'
  | 'close_friends'
  | 'professional'
  | 'romantic_partners'
  | 'custom'

export type ConflictStyle = 'adaptive' | 'withdraw' | 'direct' | 'cold' | 'humor'

export interface AgentIdentityPersonalization {
  preferredLanguage: string
  pronouns: string
  userAddress: string
  selfDescription: string
  role: string
}

export interface AgentCommunicationStyle {
  verbosity: number
  softness: number
  humor: number
  figurativeness: number
  expressiveness: number
  supportiveness: number
  formality: number
  teasing: number
  emojiFrequency: number
  flirtation: number
  conversationalInitiative: number
}

export interface AgentEmotionalDynamics {
  reactivity: number
  responseIntensity: number
  recoverySpeed: number
  positivePersistence: number
  negativePersistence: number
  expression: number
  masking: number
  conflictStyle: ConflictStyle
  triggers: Record<string, string[]>
  soothingStrategies: string[]
}

export interface AgentRelationshipSeed {
  preset: RelationshipSeedPreset
  dimensions: Record<string, number>
  summary: string
}

export interface AgentBackstoryEpisode {
  id: string
  title: string
  content: string
  kind: string
  people: string[]
  place: string
  emotionalValence: number
  sequence: number
}

export interface AgentStructuredBackstory {
  narrative: string
  summary: string
  episodes: AgentBackstoryEpisode[]
}

export interface NumericRange {
  min: number
  max: number
}

export interface AgentEvolutionPolicy {
  lockedFields: string[]
  traitBounds: Record<string, NumericRange>
  reflectionMode: 'enabled' | 'disabled'
  reflectionCooldownMinutes: number
  reflectionMaxTokens: number
  reflectionMaxDurationSeconds: number
  reflectionMaxEvidence: number
}

export interface AgentPersonalizationInput {
  identity: AgentIdentityPersonalization
  communicationStyle: AgentCommunicationStyle
  emotionalDynamics: AgentEmotionalDynamics
  relationshipSeed: AgentRelationshipSeed
  structuredBackstory: AgentStructuredBackstory
  evolutionPolicy: AgentEvolutionPolicy
}

export interface AgentPersonalizationProfile extends AgentPersonalizationInput {
  agentId: string
  schemaVersion: number
  version: number
  revisionId: string
  operation: string
  reason: string
  createdAt: string
  updatedAt: string
  temperament: Record<string, number>
}

export interface AgentPersonalizationUpdate {
  expectedVersion: number
  traits: Record<string, number>
  personalization: AgentPersonalizationInput
  reason: string
}

export interface AgentProfile {
  id: string
  name: string
  age?: number
  gender: string
  preferences: string
  /** Owner-authored fictional autobiographical identity seed. */
  backstory: string
  traits: Record<string, number>
  active: boolean
  createdAt: string
  updatedAt: string
}

export interface AgentProfileInput {
  name: string
  age?: number
  gender: string
  preferences: string
  /** Owner-authored fictional autobiographical identity seed. */
  backstory: string
  traits: Record<string, number>
  personalization: AgentPersonalizationInput
  /** UI-only draft metadata. Unknown fields are ignored by the Go bridge. */
  creationMode: AgentCreationMode
  presetId: string
}

export interface PortableAgentProfile {
  path: string
  exportedAt: string
  sizeBytes: number
  checksum: string
  profile: AgentProfileInput
}

export type PersonalityPreviewScenario =
  | 'introduction'
  | 'disagreement'
  | 'self_correction'
  | 'praise'
  | 'peer_praise'
  | 'fear'
  | 'reconciliation'

export interface PersonalityPreviewInfluence {
  layer: string
  key: string
  value: number
  direction: 'low' | 'balanced' | 'high'
}

export interface PersonalityPreview {
  scenario: PersonalityPreviewScenario
  scenarioTitle: string
  prompt: string
  response: string
  model: string
  compilerCharacters: number
  influences: PersonalityPreviewInfluence[]
}
