/**
 * A bounded background conversation between two named agents. The backend
 * keeps this separate from user-facing chat transcripts so the UI can show
 * provenance without exposing private context or hidden reasoning.
 */
export type PeerDialogueStatus = 'queued' | 'running' | 'cancelling' | 'completed' | 'failed' | 'cancelled' | 'expired' | 'unknown'

export interface PeerDialogueMessage {
  id: string
  sequence: number
  senderAgentId: string
  senderName: string
  recipientAgentId: string
  recipientName: string
  content: string
  createdAt: string
}

export interface PeerDialogue {
  id: string
  initiatorAgentId: string
  initiatorName: string
  peerAgentId: string
  peerName: string
  purpose: string
  status: PeerDialogueStatus
  turnCount: number
  maxTurns: number
  tokensUsed: number
  maxTokens: number
  createdAt: string
  finishedAt?: string
  failure?: string
  messages: PeerDialogueMessage[]
}

export interface PeerDialogueListOptions {
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
}

export type ActivityType = 'job' | 'proactive' | 'system' | 'reflection' | 'memory' | 'unknown'

export type ActivityStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled' | 'skipped' | 'blocked' | 'info' | 'unknown'

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
}

export interface ActivityListOptions {
  limit?: number
  type?: ActivityType | 'all'
  status?: ActivityStatus | 'all'
}
