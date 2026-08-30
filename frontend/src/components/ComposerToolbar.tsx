import { memo, useEffect } from 'react'

import { useVoice } from '../hooks/useVoice'
import { Icon } from './Icon'

function formatDuration(durationMs: number): string {
  const seconds = Math.max(0, Math.floor(durationMs / 1000))
  return `00:${seconds.toString().padStart(2, '0')}`
}

type ComposerToolbarProps = {
  agentName: string
  autoSpeak: boolean
  /** Bumping this asks the recorder to drop the captured clip. */
  clearToken: number
  onBeforeRecord: () => void
  onCancel: () => void
  onCapture: (blob: Blob) => void
  onRecordingChange: (recording: boolean) => void
  onToggleAutoSpeak: () => void
  running: boolean
  sendDisabled: boolean
  speechSupported: boolean
  transcribing: boolean
}

/**
 * Owns `useVoice` so the recording timer stays here.
 *
 * The hook ticks `durationMs` ten times a second while the microphone is open.
 * Held at the top of `ChatView` that re-rendered the whole chat — transcript
 * included — for a single `<span>`; scoped to this toolbar it re-renders a
 * dozen nodes instead.
 */
export const ComposerToolbar = memo(function ComposerToolbar({
  agentName,
  autoSpeak,
  clearToken,
  onBeforeRecord,
  onCancel,
  onCapture,
  onRecordingChange,
  onToggleAutoSpeak,
  running,
  sendDisabled,
  speechSupported,
  transcribing,
}: ComposerToolbarProps) {
  const voice = useVoice()
  const recording = voice.state === 'recording'
  const voiceBlob = voice.blob
  const clearVoice = voice.clear

  useEffect(() => {
    onRecordingChange(recording)
  }, [onRecordingChange, recording])

  useEffect(() => {
    if (voiceBlob) onCapture(voiceBlob)
  }, [onCapture, voiceBlob])

  useEffect(() => {
    if (clearToken > 0) clearVoice()
  }, [clearToken, clearVoice])

  const handleVoice = () => {
    if (voice.starting) return
    if (voice.state === 'recording') voice.stop()
    else if (voice.state === 'ready' || voice.state === 'error') {
      // Barge-in is intentional: pressing the microphone always stops local
      // speech before requesting permission or opening the input stream.
      onBeforeRecord()
      voice.clear()
      void voice.start()
    } else {
      // Do this before getUserMedia so a permission prompt cannot leave Yuri
      // speaking over the user's first words.
      onBeforeRecord()
      void voice.start()
    }
  }

  return (
    <div className="composer__toolbar">
      <div className="composer__note-group">
        <span className="composer__note">⌘/Ctrl + Enter · отправить</span>
        {recording && <span className="voice-timer" aria-live="polite"><i /> {formatDuration(voice.durationMs)}</span>}
        {transcribing && <span className="voice-ready">{agentName} распознаёт голос…</span>}
        {!transcribing && voice.state === 'ready' && <span className="voice-ready">Голосовой фрагмент записан{voice.blob ? ` · ${Math.max(1, Math.round(voice.blob.size / 1024))} KB` : ''}</span>}
        {voice.error && <span className="voice-error" role="alert">{voice.error}</span>}
      </div>
      <div className="composer__actions">
        {speechSupported && (
          <button
            aria-label={autoSpeak ? 'Выключить автоматическую озвучку ответов' : 'Включить автоматическую озвучку ответов'}
            aria-pressed={autoSpeak}
            className={`voice-autospeak${autoSpeak ? ' voice-autospeak--active' : ''}`}
            onClick={onToggleAutoSpeak}
            title="Автоматически озвучивать новые ответы. Микрофон не включается автоматически."
            type="button"
          >
            <Icon name="volume" width={14} height={14} />
            <span>Авто</span>
          </button>
        )}
        <button
          aria-label={recording ? 'Остановить запись' : 'Записать голосовое сообщение'}
          aria-pressed={recording}
          className={`voice-button${recording ? ' voice-button--recording' : ''}`}
          disabled={running || transcribing || voice.starting}
          onClick={handleVoice}
          title={voice.starting ? 'Запрашиваю доступ к микрофону…' : 'Push-to-talk: запись с микрофона. Постоянное прослушивание выключено.'}
          type="button"
        >
          <Icon name="mic" width={16} height={16} />
        </button>
        <button aria-label="Прикрепить файл" className="composer__attach" disabled title="Вложения подключатся в следующем инкременте" type="button">+</button>
        {running ? (
          <button aria-label="Остановить запуск" className="stop-button" onClick={onCancel} type="button"><Icon name="x" width={16} height={16} /></button>
        ) : (
          <button aria-label="Отправить сообщение" className="send-button" disabled={sendDisabled} type="submit"><Icon name="arrow-up" width={17} height={17} /></button>
        )}
      </div>
    </div>
  )
})
