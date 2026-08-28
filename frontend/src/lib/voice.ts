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
