/**
 * A memory belongs to Yuri's single local profile, not to an individual
 * conversation. Lifecycle is deliberately explicit: dormant records are
 * excluded from normal recall, while deleted records are soft tombstones.
 */
export type MemoryKind = 'core' | 'user_model' | 'episodic' | 'semantic' | 'relationship' | 'procedural'

export type MemoryContentKind = 'fact' | 'opinion' | 'emotion' | 'inference'

export type MemoryLifecycleState = 'active' | 'dormant' | 'deleted'

export type MemoryScope = 'agent_private' | 'owner_shared' | 'installation_shared'

export interface MemorySource {
  sourceType: string
  sourceId?: string
  conversationId?: string
  conversationTitle?: string
  messageId?: string
  excerpt?: string
  excerptHash?: string
  evidenceWeight?: number
  createdAt?: string
}

export interface MemoryRecord {
  id: string
  agentId?: string
  agentName?: string
  scope: MemoryScope
  kind: MemoryKind
  contentKind: MemoryContentKind
  content: string
  confidence: number
  salience: number
  valence?: number
  sensitivity?: string
  lifecycleState: MemoryLifecycleState
  pinned: boolean
  accessCount: number
  lastRecalledAt?: string
  decayPolicy?: string
  embeddingVersion?: string
  createdAt: string
  updatedAt: string
  sources: MemorySource[]
}

export interface MemoryListOptions {
  lifecycleState?: MemoryLifecycleState | 'all'
  kind?: MemoryKind | 'all'
  scope?: MemoryScope | 'all'
  query?: string
  limit?: number
  offset?: number
}

export interface MemoryUpdate {
  content?: string
  kind?: MemoryKind
  contentKind?: MemoryContentKind
  confidence?: number
  salience?: number
  valence?: number
  pinned?: boolean
}
