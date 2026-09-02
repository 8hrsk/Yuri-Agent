import { useEffect, useRef, useState } from 'react'

import type { AgentProfile } from '../lib/contracts'
import { Icon } from './Icon'
import { ModalShell } from './ModalShell'

type AgentDeleteDialogProps = {
  agent: AgentProfile
  busy: boolean
  error?: string
  onCancel: () => void
  onConfirm: () => Promise<void>
}

export function AgentDeleteDialog({ agent, busy, error, onCancel, onConfirm }: AgentDeleteDialogProps) {
  const [step, setStep] = useState<'warning' | 'verify'>('warning')
  const [enteredName, setEnteredName] = useState('')
  const cancelRef = useRef<HTMLButtonElement>(null)
  const nameRef = useRef<HTMLInputElement>(null)
  const matches = enteredName === agent.name

  useEffect(() => {
    if (step === 'verify') nameRef.current?.focus()
  }, [step])

  return (
    <ModalShell
      backdropClassName="approval-backdrop"
      className="approval-dialog agent-delete-dialog"
      describedBy="agent-delete-description"
      initialFocusRef={cancelRef}
      labelledBy="agent-delete-title"
      onEscape={() => { if (!busy) onCancel() }}
    >
      <div className="approval-dialog__mark agent-delete-dialog__mark"><Icon name="trash" width={20} height={20} /></div>
      <span className="section-heading__overline">DANGER ZONE</span>
      <h2 id="agent-delete-title">Удалить агента «{agent.name}»?</h2>
      {step === 'warning' ? (
        <>
          <p id="agent-delete-description">Агент больше не сможет отвечать, запускать задачи или общаться с другими агентами. Его история, воспоминания и отношения сохранятся как исторические данные.</p>
          <div className="approval-dialog__actions">
            <button className="button button--quiet" onClick={onCancel} ref={cancelRef} type="button">Отмена</button>
            <button className="button button--danger" onClick={() => setStep('verify')} type="button">Продолжить</button>
          </div>
        </>
      ) : (
        <>
          <p id="agent-delete-description">Для подтверждения введите имя агента точно как указано ниже.</p>
          <label className="agent-delete-dialog__field">
            <span>Введите <strong>{agent.name}</strong></span>
            <input
              aria-label={`Введите имя агента ${agent.name}`}
              autoComplete="off"
              disabled={busy}
              onChange={(event) => setEnteredName(event.target.value)}
              ref={nameRef}
              spellCheck={false}
              value={enteredName}
            />
          </label>
          {error && <p className="approval-dialog__error" role="alert">{error}</p>}
          <div className="approval-dialog__actions">
            <button className="button button--quiet" disabled={busy} onClick={onCancel} ref={cancelRef} type="button">Отмена</button>
            <button className="button button--danger" disabled={busy || !matches} onClick={() => void onConfirm()} type="button">
              <Icon name="trash" width={13} height={13} /> {busy ? 'Удаляю…' : 'Удалить агента'}
            </button>
          </div>
        </>
      )}
    </ModalShell>
  )
}
