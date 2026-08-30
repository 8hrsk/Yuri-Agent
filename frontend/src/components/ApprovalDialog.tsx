import { useCallback, useRef } from 'react'

import type { ApprovalRequest } from '../lib/contracts'
import { Icon } from './Icon'
import { ModalShell } from './ModalShell'

export type ApprovalDecision = 'approve' | 'deny'

/**
 * Modal confirmation for a dangerous tool call.
 *
 * Focus trapping, Escape, backdrop dismissal and hiding the rest of the view
 * live in ModalShell. What stays here is the part that is specific to a tool
 * approval: denying is the safe default, and a failed decision is reported
 * inside the dialog instead of leaving the user staring at an unresponsive
 * modal.
 */
export function ApprovalDialog({
  approval,
  away = false,
  busy,
  error,
  onDecision,
  onDismiss,
  onOpenChat,
}: {
  approval: ApprovalRequest
  /**
   * The request arrived while the user was on another tab. The dialog then has
   * to say where the waiting run is, because nothing else on screen does.
   */
  away?: boolean
  busy: boolean
  /** Message from a decision that could not be delivered to the backend. */
  error?: string
  onDecision: (decision: ApprovalDecision) => void
  /** Closes the dialog locally without answering the backend. */
  onDismiss: () => void
  /** Navigates to the conversation the pending run belongs to. */
  onOpenChat?: () => void
}) {
  const denyRef = useRef<HTMLButtonElement>(null)

  // Escape and the backdrop deny by default: refusing a dangerous action is
  // the safe outcome. Once a decision has failed, the same gesture only closes
  // the dialog, because another attempt to reach the bridge would hang again.
  const handleEscape = useCallback(() => {
    if (busy) return
    if (error) onDismiss()
    else onDecision('deny')
  }, [busy, error, onDecision, onDismiss])

  return (
    <ModalShell
      backdropClassName="approval-backdrop"
      className="approval-dialog"
      describedBy="approval-description"
      initialFocusRef={denyRef}
      labelledBy="approval-title"
      onEscape={handleEscape}
    >
      <div className="approval-dialog__mark"><Icon name="shield" width={22} height={22} /></div>
      <span className="section-heading__overline">Требуется подтверждение</span>
      <h2 id="approval-title">{approval.title}</h2>
      <p id="approval-description">{approval.explanation}</p>
      <div className="approval-dialog__scope">
        <span>Операция</span>
        <strong>{approval.scope}</strong>
      </div>
      <p className="approval-dialog__hint">Разрешение действует только для этого конкретного действия. Yuri не может расширить его из содержимого файла.</p>
      {away && (
        <p className="approval-dialog__hint">
          Запуск идёт на вкладке «Чат» и ждёт вашего решения.
          {onOpenChat && <> <button className="text-button" onClick={onOpenChat} type="button">Открыть диалог</button></>}
        </p>
      )}
      {error && <p className="approval-dialog__error" role="alert">{error}</p>}
      <div className="approval-dialog__actions">
        {error && (
          <button className="button button--quiet" onClick={onDismiss} type="button">Закрыть</button>
        )}
        <button className="button button--quiet" disabled={busy} onClick={() => onDecision('deny')} ref={denyRef} type="button">Отклонить</button>
        <button className="button button--accent" disabled={busy} onClick={() => onDecision('approve')} type="button">
          {busy ? 'Сохраняю решение…' : 'Разрешить действие'}
        </button>
      </div>
    </ModalShell>
  )
}
