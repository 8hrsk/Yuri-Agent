import { useLayoutEffect, useRef, type FormEvent, type KeyboardEvent } from 'react'

import type { ChatAttachmentInput } from '../lib/contracts'
import { ComposerToolbar } from './ComposerToolbar'
import { Icon } from './Icon'

type ChatComposerProps = {
  agentName: string
  autoSpeak: boolean
  attachments: ChatAttachmentInput[]
  /** The backend bridge is live; otherwise the local-preview callout is offered. */
  connected: boolean
  clearVoiceToken: number
  draft: string
  onBeforeRecord: () => void
  onCancel: () => void
  onCaptureVoice: (blob: Blob) => void
  onDraftChange: (value: string) => void
  onDraftKeyDown: (event: KeyboardEvent<HTMLTextAreaElement>) => void
  onOpenSettings: () => void
  onRecordingChange: (recording: boolean) => void
  onRemoveAttachment: (attachmentId: string) => void
  onSelectAttachments: (files: FileList) => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
  onToggleAutoSpeak: () => void
  running: boolean
  speechSupported: boolean
  transcribing: boolean
}

export function ChatComposer({
  agentName,
  autoSpeak,
  attachments,
  connected,
  clearVoiceToken,
  draft,
  onBeforeRecord,
  onCancel,
  onCaptureVoice,
  onDraftChange,
  onDraftKeyDown,
  onOpenSettings,
  onRecordingChange,
  onRemoveAttachment,
  onSelectAttachments,
  onSubmit,
  onToggleAutoSpeak,
  running,
  speechSupported,
  transcribing,
}: ChatComposerProps) {
  const inputRef = useRef<HTMLTextAreaElement>(null)

  /*
   * Grow the field with its content instead of offering a resize grip.
   *
   * The grip was drawn in the textarea's bottom-right corner, which is exactly
   * where the send button sits one row below, so the two overlapped. Auto-growth
   * removes the grip and is what people expect from a message box anyway; the
   * CSS caps the height and turns on scrolling past the cap.
   */
  useLayoutEffect(() => {
    const input = inputRef.current
    if (!input) return
    input.style.height = 'auto'
    input.style.height = `${input.scrollHeight}px`
  }, [draft])

  return (
    <div className="composer-wrap composer-wrap--active">
      <form className="composer" onSubmit={onSubmit}>
        <div className="composer__topline">
          <span className="composer__label">Новое сообщение</span>
          <span className="composer__mode"><span /> {connected ? `${agentName} · connected` : `${agentName} · local preview`}</span>
        </div>
        <textarea
          aria-label={`Сообщение ${agentName}`}
          className="composer__input"
          disabled={running}
          onChange={(event) => onDraftChange(event.target.value)}
          onKeyDown={onDraftKeyDown}
          placeholder="Напишите что-нибудь…"
          ref={inputRef}
          rows={2}
          value={draft}
        />
        {attachments.length > 0 && (
          <div aria-label="Прикреплённые файлы" className="composer-attachments">
            {attachments.map((attachment) => (
              <div className={`composer-attachment composer-attachment--${attachment.kind}`} key={attachment.id}>
                {attachment.previewDataUrl
                  ? <img alt="" src={attachment.previewDataUrl} />
                  : <Icon name="file" width={15} height={15} />}
                <span><strong>{attachment.name}</strong><small>{Math.max(1, Math.round(attachment.sizeBytes / 1024))} КБ</small></span>
                <button aria-label={`Убрать ${attachment.name}`} onClick={() => onRemoveAttachment(attachment.id)} type="button"><Icon name="x" width={13} height={13} /></button>
              </div>
            ))}
          </div>
        )}
        <ComposerToolbar
          agentName={agentName}
          autoSpeak={autoSpeak}
          clearToken={clearVoiceToken}
          onBeforeRecord={onBeforeRecord}
          onCancel={onCancel}
          onCapture={onCaptureVoice}
          onRecordingChange={onRecordingChange}
          onSelectAttachments={onSelectAttachments}
          onToggleAutoSpeak={onToggleAutoSpeak}
          running={running}
          sendDisabled={(draft.trim() === '' && attachments.length === 0) || transcribing}
          speechSupported={speechSupported}
          transcribing={transcribing}
        />
      </form>
      {!connected && (
        <button className="connection-callout" onClick={onOpenSettings} type="button">
          <span className="connection-callout__icon"><Icon name="settings" width={16} height={16} /></span>
          <span><strong>Локальный preview режим</strong><small>Подключите OpenAI-compatible endpoint или Codex App Server в Settings</small></span>
          <Icon name="chevron-right" width={15} height={15} />
        </button>
      )}
    </div>
  )
}
