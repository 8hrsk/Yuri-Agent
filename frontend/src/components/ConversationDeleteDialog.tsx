import { useRef } from 'react'

import type { Conversation } from '../lib/contracts'
import { Icon } from './Icon'
import { ModalShell } from './ModalShell'

type ConversationDeleteDialogProps = {
  busy: boolean
  conversation: Conversation
  error?: string
  onCancel: () => void
  onConfirm: () => Promise<void>
}

export function ConversationDeleteDialog({ busy, conversation, error, onCancel, onConfirm }: ConversationDeleteDialogProps) {
  const cancelRef = useRef<HTMLButtonElement>(null)

  return (
    <ModalShell
      backdropClassName="approval-backdrop"
      className="approval-dialog conversation-delete-dialog"
      describedBy="conversation-delete-description"
      initialFocusRef={cancelRef}
      labelledBy="conversation-delete-title"
      onEscape={() => { if (!busy) onCancel() }}
    >
      <div className="approval-dialog__mark conversation-delete-dialog__mark"><Icon name="trash" width={20} height={20} /></div>
      <span className="section-heading__overline">CONVERSATION</span>
      <h2 id="conversation-delete-title">Удалить диалог «{conversation.title}»?</h2>
      <p id="conversation-delete-description">Диалог перестанет отображаться в приложении. Его сообщения, вложения и связанные воспоминания останутся в локальном хранилище.</p>
      {error && <p className="approval-dialog__error" role="alert">{error}</p>}
      <div className="approval-dialog__actions">
        <button className="button button--quiet" disabled={busy} onClick={onCancel} ref={cancelRef} type="button">Отмена</button>
        <button className="button button--danger" disabled={busy} onClick={() => void onConfirm()} type="button">
          <Icon name="trash" width={13} height={13} /> {busy ? 'Удаляю…' : 'Удалить диалог'}
        </button>
      </div>
    </ModalShell>
  )
}
