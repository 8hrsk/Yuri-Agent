import { describe, expect, it } from 'vitest'

import {
  autoSpeakStorageKey,
  parseAutoSpeakPreference,
  readAutoSpeakPreference,
  writeAutoSpeakPreference,
} from './voice'

describe('voice preferences', () => {
  it('accepts only explicit opt-in values', () => {
    expect(parseAutoSpeakPreference(true)).toBe(true)
    expect(parseAutoSpeakPreference('on')).toBe(true)
    expect(parseAutoSpeakPreference('TRUE')).toBe(true)
    expect(parseAutoSpeakPreference('false')).toBe(false)
    expect(parseAutoSpeakPreference(undefined)).toBe(false)
    expect(parseAutoSpeakPreference('hands-free')).toBe(false)
  })

  it('round-trips the renderer-only auto-speak preference', () => {
    const values = new Map<string, string>()
    const storage = {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
    }

    expect(readAutoSpeakPreference(storage)).toBe(false)
    writeAutoSpeakPreference(true, storage)
    expect(values.get(autoSpeakStorageKey)).toBe('true')
    expect(readAutoSpeakPreference(storage)).toBe(true)
    writeAutoSpeakPreference(false, storage)
    expect(readAutoSpeakPreference(storage)).toBe(false)
  })
})
