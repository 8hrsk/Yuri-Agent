import type { BackendStatus } from '../lib/backend'

type ConnectionBadgeProps = {
  status: BackendStatus
  label: string
  compact?: boolean
}

export function ConnectionBadge({ status, label, compact = false }: ConnectionBadgeProps) {
  return (
    <span className={`connection-badge connection-badge--${status}${compact ? ' connection-badge--compact' : ''}`}>
      <span className="connection-badge__dot" />
      <span>{label}</span>
    </span>
  )
}
