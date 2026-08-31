import { useEffect, useState, type FormEvent } from 'react'

import type { AffectiveState, AvatarState, RunStatus } from '../lib/contracts'
import { dominantAffectMood } from '../lib/personality'
import { Icon } from './Icon'
import { YuriAvatar } from './YuriAvatar'

type ChatHeaderProps = {
  affect?: AffectiveState
  agentName: string
  avatarState: AvatarState
  runLabel: string
  runStatus: RunStatus
  title: string
  onRename: (title: string) => Promise<void>
}

export function ChatHeader({ affect, agentName, avatarState, onRename, runLabel, runStatus, title }: ChatHeaderProps) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(title)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string>()
  const affectMood = dominantAffectMood(affect)
  const activeEmotions = affect?.dimensions
    .filter((dimension) => dimension.value >= 0.15)
    .sort((left, right) => right.value - left.value)
    .slice(0, 2) ?? []

  useEffect(() => {
    if (!editing) setDraft(title)
  }, [editing, title])

  const beginEditing = () => {
    setDraft(title)
    setSaveError(undefined)
    setEditing(true)
  }

  const cancelEditing = () => {
    setDraft(title)
    setSaveError(undefined)
    setEditing(false)
  }

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const nextTitle = draft.trim()
    if (!nextTitle || saving) return
    setSaving(true)
    setSaveError(undefined)
    try {
      await onRename(nextTitle)
      setEditing(false)
    } catch (cause) {
      setSaveError(cause instanceof Error ? cause.message : 'Не удалось переименовать диалог.')
    } finally {
      setSaving(false)
    }
  }

  return (
    <header className="chat-main__header">
      <div className="chat-main__header-persona">
        <YuriAvatar affect={affect} label={`${agentName} · ${runLabel}${affect?.mood ? ` · ${affect.mood}` : ''}`} size="sm" state={avatarState} />
        <div>
          <span className="section-heading__overline">Conversation · local</span>
          {editing ? (
            <form className="chat-title-editor" onSubmit={(event) => void submit(event)}>
              <label className="sr-only" htmlFor="conversation-title">Название диалога</label>
              <input
                aria-label="Название диалога"
                autoFocus
                className="chat-title-editor__input"
                id="conversation-title"
                maxLength={80}
                onChange={(event) => setDraft(event.target.value)}
                value={draft}
              />
              <button aria-label="Сохранить название диалога" className="icon-button icon-button--small" disabled={saving || !draft.trim()} type="submit">
                <Icon name="check" width={14} height={14} />
              </button>
              <button aria-label="Отменить переименование диалога" className="icon-button icon-button--small" disabled={saving} onClick={cancelEditing} type="button">
                <Icon name="x" width={14} height={14} />
              </button>
              {saveError && <span className="chat-title-editor__error" role="alert">{saveError}</span>}
            </form>
          ) : (
            <div className="chat-title-row">
              <h2>{title}</h2>
              <button aria-label="Переименовать диалог" className="icon-button icon-button--small chat-title-row__edit" onClick={beginEditing} type="button">
                <Icon name="edit" width={13} height={13} />
              </button>
            </div>
          )}
        </div>
      </div>
      <div className="chat-main__header-meta">
        {affect && <span aria-label={`Текущее эмоциональное состояние: ${affect.mood}`} className={`affect-state affect-state--${affectMood}`} title={affect.reason}><i /><span>{affect.mood}</span>{activeEmotions.length > 0 && <small>{activeEmotions.map((emotion) => `${emotion.label} ${Math.round(emotion.value * 100)}%`).join(' · ')}</small>}</span>}
        <span className={`run-state run-state--${runStatus}`} role="status"><i /> {runLabel}</span>
        <span className="chat-main__privacy"><Icon name="lock" width={13} height={13} /> private</span>
      </div>
    </header>
  )
}
