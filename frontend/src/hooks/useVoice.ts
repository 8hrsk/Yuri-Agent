import { useCallback, useEffect, useRef, useState } from 'react'

import { cancelSpeech, playSpeech } from '../lib/voice'

export type VoiceState = 'idle' | 'recording' | 'ready' | 'error'

type UseVoiceResult = {
  state: VoiceState
  /** True while the browser is waiting for microphone permission/initialization. */
  starting: boolean
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
  const [starting, setStarting] = useState(false)
  const [durationMs, setDurationMs] = useState(0)
  const [blob, setBlob] = useState<Blob>()
  const [error, setError] = useState<string>()
  const recorderRef = useRef<MediaRecorder | undefined>(undefined)
  const streamRef = useRef<MediaStream | undefined>(undefined)
  const startedAtRef = useRef(0)
  const timerRef = useRef<number | undefined>(undefined)
  const chunksRef = useRef<Blob[]>([])
  const startInFlightRef = useRef(false)
  const recordingRef = useRef(false)
  const cancelStartRef = useRef(false)
  const discardOnStopRef = useRef(false)
  const mountedRef = useRef(true)

  const clearTimer = useCallback(() => {
    if (timerRef.current !== undefined) window.clearInterval(timerRef.current)
    timerRef.current = undefined
  }, [])

  const stop = useCallback((discard = false) => {
    if (discard) discardOnStopRef.current = true
    cancelStartRef.current = true
    const recorder = recorderRef.current
    if (recorder && recorder.state !== 'inactive') recorder.stop()
    clearTimer()
    streamRef.current?.getTracks().forEach((track) => track.stop())
    streamRef.current = undefined
  }, [clearTimer])

  const start = useCallback(async () => {
    // Permission prompts are asynchronous. Keep a ref guard so a double click
    // cannot open two microphones before React renders the recording state.
    if (startInFlightRef.current || recordingRef.current) return
    startInFlightRef.current = true
    cancelStartRef.current = false
    discardOnStopRef.current = false
    setError(undefined)
    setBlob(undefined)

    if (typeof navigator === 'undefined' || !navigator.mediaDevices?.getUserMedia || typeof MediaRecorder === 'undefined') {
      startInFlightRef.current = false
      setState('error')
      setError('Браузер или Wails runtime не предоставил доступ к микрофону.')
      return
    }

    setStarting(true)
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true })
      if (!mountedRef.current || cancelStartRef.current) {
        stream.getTracks().forEach((track) => track.stop())
        return
      }
      const recorder = new MediaRecorder(stream)
      streamRef.current = stream
      recorderRef.current = recorder
      recordingRef.current = true
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
        recordingRef.current = false
        const discard = discardOnStopRef.current
        discardOnStopRef.current = false
        if (!mountedRef.current) return
        setBlob(discard ? undefined : chunksRef.current.length > 0 ? new Blob(chunksRef.current, { type: recorder.mimeType || 'audio/webm' }) : undefined)
        chunksRef.current = []
        setState(discard ? 'idle' : 'ready')
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
      recordingRef.current = false
      if (mountedRef.current && !cancelStartRef.current) {
        setState('error')
        setError(cause instanceof DOMException && cause.name === 'NotAllowedError'
          ? 'Доступ к микрофону запрещён. Разрешите его в настройках приложения.'
          : 'Не удалось начать запись с микрофона.')
      }
    } finally {
      startInFlightRef.current = false
      if (mountedRef.current) setStarting(false)
    }
  }, [clearTimer])

  const clear = useCallback(() => {
    // Clearing a captured clip must not let a late MediaRecorder onstop event
    // put the hook back into `ready` after the user already reset it.
    stop(true)
    setDurationMs(0)
    setBlob(undefined)
    setError(undefined)
    setState('idle')
  }, [stop])

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      stop(true)
      clearTimer()
    }
  }, [clearTimer, stop])

  return { state, starting, durationMs, blob, error, start, stop: () => stop(), clear }
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
  const speechTokenRef = useRef(0)

  const stop = useCallback(() => {
    speechTokenRef.current += 1
    if (supported) cancelSpeech(window.speechSynthesis)
    setSpeakingId(undefined)
  }, [supported])

  const speak = useCallback((messageId: string, text: string) => {
    if (!supported || !text.trim()) return
    const token = speechTokenRef.current + 1
    speechTokenRef.current = token
    const clearIfCurrent = () => {
      if (speechTokenRef.current === token) setSpeakingId(undefined)
    }
    setSpeakingId(messageId)
    playSpeech(window.speechSynthesis, (value) => new SpeechSynthesisUtterance(value), text, clearIfCurrent)
  }, [supported])

  useEffect(() => () => {
    speechTokenRef.current += 1
    if (supported) cancelSpeech(window.speechSynthesis)
  }, [supported])

  return { speakingId, supported, speak, stop }
}
