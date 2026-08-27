import { useCallback, useEffect, useRef, useState } from 'react'

export type VoiceState = 'idle' | 'recording' | 'ready' | 'error'

type UseVoiceResult = {
  state: VoiceState
  durationMs: number
  blob?: Blob
  error?: string
  start: () => Promise<void>
  stop: () => void
  clear: () => void
}

const now = () => (typeof performance === 'undefined' ? Date.now() : performance.now())

export function useVoice(): UseVoiceResult {
  const [state, setState] = useState<VoiceState>('idle')
  const [durationMs, setDurationMs] = useState(0)
  const [blob, setBlob] = useState<Blob>()
  const [error, setError] = useState<string>()
  const recorderRef = useRef<MediaRecorder>()
  const streamRef = useRef<MediaStream>()
  const startedAtRef = useRef(0)
  const timerRef = useRef<number>()
  const chunksRef = useRef<Blob[]>([])

  const clearTimer = useCallback(() => {
    if (timerRef.current !== undefined) window.clearInterval(timerRef.current)
    timerRef.current = undefined
  }, [])

  const stop = useCallback(() => {
    const recorder = recorderRef.current
    if (recorder && recorder.state !== 'inactive') recorder.stop()
    clearTimer()
    streamRef.current?.getTracks().forEach((track) => track.stop())
    streamRef.current = undefined
  }, [clearTimer])

  const start = useCallback(async () => {
    if (state === 'recording') return
    setError(undefined)
    setBlob(undefined)

    if (!navigator.mediaDevices?.getUserMedia || typeof MediaRecorder === 'undefined') {
      setState('error')
      setError('Браузер или Wails runtime не предоставил доступ к микрофону.')
      return
    }

    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true })
      const recorder = new MediaRecorder(stream)
      streamRef.current = stream
      recorderRef.current = recorder
      chunksRef.current = []
      startedAtRef.current = now()
      setDurationMs(0)
      setState('recording')
      timerRef.current = window.setInterval(() => setDurationMs(now() - startedAtRef.current), 100)
      recorder.onstop = () => {
        clearTimer()
        stream.getTracks().forEach((track) => track.stop())
        streamRef.current = undefined
        recorderRef.current = undefined
        setBlob(chunksRef.current.length > 0 ? new Blob(chunksRef.current, { type: recorder.mimeType || 'audio/webm' }) : undefined)
        chunksRef.current = []
        setState('ready')
      }
      recorder.ondataavailable = (event) => {
        if (event.data.size > 0) chunksRef.current.push(event.data)
      }
      recorder.start()
    } catch (cause) {
      clearTimer()
      streamRef.current?.getTracks().forEach((track) => track.stop())
      streamRef.current = undefined
      recorderRef.current = undefined
      setState('error')
      setError(cause instanceof DOMException && cause.name === 'NotAllowedError'
        ? 'Доступ к микрофону запрещён. Разрешите его в настройках приложения.'
        : 'Не удалось начать запись с микрофона.')
    }
  }, [clearTimer, state])

  const clear = useCallback(() => {
    stop()
    setDurationMs(0)
    setBlob(undefined)
    setError(undefined)
    setState('idle')
  }, [stop])

  useEffect(() => () => {
    stop()
    clearTimer()
  }, [clearTimer, stop])

  return { state, durationMs, blob, error, start, stop, clear }
}

type UseTTSResult = {
  speakingId?: string
  supported: boolean
  speak: (messageId: string, text: string) => void
  stop: () => void
}

export function useTTS(): UseTTSResult {
  const supported = typeof window !== 'undefined' && 'speechSynthesis' in window
  const [speakingId, setSpeakingId] = useState<string>()

  const stop = useCallback(() => {
    if (supported) window.speechSynthesis.cancel()
    setSpeakingId(undefined)
  }, [supported])

  const speak = useCallback((messageId: string, text: string) => {
    if (!supported) return
    window.speechSynthesis.cancel()
    const utterance = new SpeechSynthesisUtterance(text)
    utterance.lang = 'ru-RU'
    utterance.onend = () => setSpeakingId(undefined)
    utterance.onerror = () => setSpeakingId(undefined)
    setSpeakingId(messageId)
    window.speechSynthesis.speak(utterance)
  }, [supported])

  useEffect(() => () => {
    if (supported) window.speechSynthesis.cancel()
  }, [supported])

  return { speakingId, supported, speak, stop }
}
