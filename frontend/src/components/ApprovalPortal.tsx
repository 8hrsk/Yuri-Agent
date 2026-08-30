import { createPortal } from 'react-dom'

import type { ApprovalRequest } from '../lib/contracts'
import { ApprovalDialog, type ApprovalDecision } from './ApprovalDialog'

type ApprovalPortalProps = {
  approval?: ApprovalRequest
  away: boolean
  busy: boolean
  error?: string
  onDecision: (decision: ApprovalDecision) => void
  onDismiss: () => void
  onOpenChat: () => void
}

/**
 * Portalled to the document body on purpose. A pending approval blocks a
 * dangerous operation and the run behind it, so it has to reach the user
 * on whatever tab they are on — including when this whole subtree is
 * hidden, which is exactly the case the modal used to be lost in.
 */
export function ApprovalPortal({ approval, away, busy, error, onDecision, onDismiss, onOpenChat }: ApprovalPortalProps) {
  if (!approval) return null
  return createPortal(
    <ApprovalDialog
      approval={approval}
      away={away}
      busy={busy}
      error={error}
      key={approval.id}
      onDecision={onDecision}
      onDismiss={onDismiss}
      onOpenChat={onOpenChat}
    />,
    document.body,
  )
}
