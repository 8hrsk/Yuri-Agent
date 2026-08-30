import type { MessageRole } from './chat'

/**
 * Installs a local package. Mirrors desktop.PluginPathRequest, which carries
 * `path` and nothing else: whether an unsigned or unverified package may be
 * installed is decided by the owner-controlled, persisted dev-mode switch, so
 * the renderer has no per-request field to waive signature verification with.
 */
export interface PluginInstallRequest {
  path: string
}

export interface ArchiveSearchRequest {
  query: string
  includeDormant?: boolean
  conversationId?: string
  limit?: number
  offset?: number
}

export interface ArchiveSearchResult {
  id: string
  conversationId?: string
  conversationTitle?: string
  messageId?: string
  role?: MessageRole
  content: string
  snippet?: string
  createdAt?: string
  score?: number
  matchType?: 'lexical' | 'semantic' | 'hybrid'
}

export interface ArchiveSearchResponse {
  results: ArchiveSearchResult[]
  total?: number
  query: string
}
