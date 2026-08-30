import type { FormEvent, KeyboardEvent } from 'react'

import { ComposerToolbar } from './ComposerToolbar'
import { Icon } from './Icon'

type ChatComposerProps = {
  agentName: string
  autoSpeak: boolean
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
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
  onToggleAutoSpeak: () => void
  running: boolean
  speechSupported: boolean
  transcribing: boolean
}

export function ChatComposer({
  agentName,
  autoSpeak,
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
  onSubmit,
  onToggleAutoSpeak,
  running,
  speechSupported,
  transcribing,
}: ChatComposerProps) {
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
          rows={2}
          value={draft}
        />
        <ComposerToolbar
          agentName={agentName}
          autoSpeak={autoSpeak}
          clearToken={clearVoiceToken}
          onBeforeRecord={onBeforeRecord}
          onCancel={onCancel}
          onCapture={onCaptureVoice}
          onRecordingChange={onRecordingChange}
          onToggleAutoSpeak={onToggleAutoSpeak}
          running={running}
          sendDisabled={draft.trim() === '' || transcribing}
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
