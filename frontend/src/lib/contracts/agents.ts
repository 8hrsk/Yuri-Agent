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
}
