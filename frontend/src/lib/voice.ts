/**
 * Voice preferences deliberately live in the renderer's local storage for the
 * MVP. They control speech synthesis only; they never grant microphone access
 * or start recording in the background.
 */
export const autoSpeakStorageKey = 'yuri.voice.autoSpeak'

export function parseAutoSpeakPreference(value: unknown): boolean {
  if (typeof value === 'boolean') return value
  if (typeof value !== 'string') return false
  return ['1', 'true', 'yes', 'on'].includes(value.trim().toLowerCase())
}

type PreferenceStorage = Pick<Storage, 'getItem' | 'setItem'>

export function readAutoSpeakPreference(storage?: Pick<Storage, 'getItem'>): boolean {
  try {
    const target = storage ?? (typeof window === 'undefined' ? undefined : window.localStorage)
    return parseAutoSpeakPreference(target?.getItem(autoSpeakStorageKey))
  } catch {
    // Private browsing and hardened WebViews may deny localStorage. The safe
    // fallback is opt-out, and the feature remains manually controllable.
    return false
  }
}

export function loadAutoSpeakPreference(): boolean {
  return readAutoSpeakPreference()
}

export function writeAutoSpeakPreference(enabled: boolean, storage?: PreferenceStorage): void {
  try {
    const target = storage ?? (typeof window === 'undefined' ? undefined : window.localStorage)
    target?.setItem(autoSpeakStorageKey, String(enabled))
  } catch {
    // Preference persistence is best-effort and must not affect chat/voice.
  }
}

export function saveAutoSpeakPreference(enabled: boolean): void {
  writeAutoSpeakPreference(enabled)
}

type SpeechSynthesisPort = Pick<SpeechSynthesis, 'cancel' | 'speak'>

/**
 * Starts one bounded renderer-owned utterance. Keeping this browser boundary
 * pure lets the offline smoke verify the same cancel-before-speak behavior
 * used by the React hook without requesting microphone or audio permissions.
 */
export function playSpeech(
  synthesis: SpeechSynthesisPort,
  createUtterance: (text: string) => SpeechSynthesisUtterance,
  text: string,
  onSettled: () => void,
): boolean {
  const value = text.trim()
  if (!value) return false
  synthesis.cancel()
  const utterance = createUtterance(value)
  utterance.lang = 'ru-RU'
  utterance.onend = onSettled
  utterance.onerror = onSettled
  try {
    synthesis.speak(utterance)
    return true
  } catch {
    onSettled()
    return false
  }
}

export function cancelSpeech(synthesis: Pick<SpeechSynthesis, 'cancel'>): void {
  synthesis.cancel()
}
