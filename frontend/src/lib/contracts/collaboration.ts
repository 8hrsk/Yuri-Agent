import type { PersonaEvidence, SubjectiveOpinion } from './persona'
import type { RunFailureKind } from './chat'

/**
 * A bounded background conversation between two named agents. The backend
 * keeps this separate from user-facing chat transcripts so the UI can show
 * provenance without exposing private context or hidden reasoning.
 */
export type PeerDialogueStatus = 'queued' | 'running' | 'cancelling' | 'completed' | 'failed' | 'cancelled' | 'expired' | 'unknown'
export type PeerDialogueTriggerKind = 'agent_tool' | 'autonomous' | 'unknown'
/** Why a peer exchange stopped. Hard limits are intentionally distinct from semantic completion. */
export type PeerDialogueCompletionReason = 'semantic' | 'implicit' | 'max_turns' | 'max_tokens' | 'max_duration' | 'cancelled' | 'failed' | 'unknown'

export interface PeerDialogueMessage {
  id: string
  sequence: number
  sourceRunId?: string
  senderAgentId: string
  senderName: string
  recipientAgentId: string
  recipientName: string
  content: string
  createdAt: string
  providerId?: string
  model?: string
  inputTokens?: number
  outputTokens?: number
  totalTokens?: number
}

export interface PeerDialogue {
  id: string
  initiatorAgentId: string
  initiatorName: string
  /** Current route; historical messages remain identified by their source run. */
  initiatorProviderId?: string
  initiatorModel?: string
  peerAgentId: string
  peerName: string
  /** Current route; historical messages remain identified by their source run. */
  peerProviderId?: string
  peerModel?: string
  triggerKind: PeerDialogueTriggerKind
  triggerReason: string
  purpose: string
  status: PeerDialogueStatus
  turnCount: number
  minTurns: number
  maxTurns: number
  tokensUsed: number
  maxTokens: number
  maxDurationSeconds: number
  cooldownSeconds: number
  completionReason?: PeerDialogueCompletionReason
  createdAt: string
  finishedAt?: string
  failure?: string
  failureKind?: RunFailureKind
  retryable?: boolean
  retryAfterSeconds?: number
  messages: PeerDialogueMessage[]
}

export interface PeerDialogueListOptions {
  limit?: number
}

/** The active agent's directional, subjective relationship to one peer. */
export interface PeerRelationship {
  observerAgentId: string
  peerAgentId: string
  peerName: string
  relationshipId: string
  version: number
  currentVersionId: string
  summary: string
  dimensions: Record<string, number>
  opinions: SubjectiveOpinion[]
  reason?: string
  evidence: PersonaEvidence[]
  updatedAt: string
}

export type PeerRelationshipOperation = 'create' | 'update' | 'rollback' | 'reset' | 'unknown'

export interface PeerRelationshipVersion {
  id: string
  version: number
  parentId?: string
  operation: PeerRelationshipOperation
  summary: string
  dimensions: Record<string, number>
  opinions: SubjectiveOpinion[]
  reason: string
  evidence: PersonaEvidence[]
  authorRunId?: string
  createdAt: string
}

export interface PeerRelationshipDetail {
  relationship: PeerRelationship
  versions: PeerRelationshipVersion[]
}

export interface PeerRelationshipListOptions {
  limit?: number
}

export interface ProactivitySettings {
  enabled: boolean
  quietHoursEnabled: boolean
  quietHoursStart: string
  quietHoursEnd: string
  timezone: string
  dailyLimit: number
  cooldownMinutes: number
  allowLocalNotifications: boolean
  autonomousPeerDialogues: boolean
  autonomousPeerDailyLimit: number
  autonomousPeerCooldownMinutes: number
}

export type ActivityType = 'job' | 'proactive' | 'system' | 'reflection' | 'memory' | 'unknown'

export type ActivityStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled' | 'skipped' | 'blocked' | 'info' | 'unknown'

export type ActivityLayer = 'owner_seed' | 'mutable_persona' | 'relationship' | 'opinion' | 'affect' | 'memory' | 'policy' | 'task' | 'system' | 'unknown'

export interface ActivityChange {
  key: string
  delta: number
}

export interface ActivityEvent {
  id: string
  type: ActivityType
  status: ActivityStatus
  title: string
  detail?: string
  source?: string
  scheduleId?: string
  runId?: string
  createdAt: string
  durationMs?: number
  reason?: string
  provenance?: string
  layer?: ActivityLayer
  operation?: string
  version?: number
  evidenceCount?: number
  changes?: ActivityChange[]
}

export interface ActivityListOptions {
  limit?: number
  type?: ActivityType | 'all'
  status?: ActivityStatus | 'all'
}
