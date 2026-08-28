import { afterEach, describe, expect, it } from 'vitest'

import { createYuriClient, resetYuriClientForTests } from './client'
import type { ChatEvent } from './contracts'
import { cancelSpeech, playSpeech } from './voice'

describe('offline voice → chat → TTS smoke', () => {
  const previousWindow = (globalThis as { window?: unknown }).window

  afterEach(() => {
    if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
    else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
    resetYuriClientForTests()
  })

  it('passes a fake recording through Wails STT, streaming chat, TTS, and barge-in', async () => {
    const calls: string[] = []
    const bridge = {
      ListConversations: () => [],
      TranscribeAudio: (input: { audioBase64: string; contentType: string }) => {
        calls.push('stt')
        expect(input.audioBase64).toBe('ZmFrZS13ZWJtLWF1ZGlv')
        expect(input.contentType).toBe('audio/webm')
        return { text: 'Расскажи коротко о статусе проекта', language: 'ru' }
      },
      SendMessage: (input: { text: string }) => {
        calls.push('chat')
        expect(input.text).toBe('Расскажи коротко о статусе проекта')
        return {
          runId: 'run-voice-smoke',
          status: 'complete',
          events: [
            { type: 'run.started', runId: 'run-voice-smoke' },
            { type: 'assistant.delta', runId: 'run-voice-smoke', messageId: 'message-voice-smoke', delta: 'Проект ' },
            { type: 'assistant.delta', runId: 'run-voice-smoke', messageId: 'message-voice-smoke', delta: 'готов.' },
            { type: 'assistant.completed', runId: 'run-voice-smoke', messageId: 'message-voice-smoke' },
            { type: 'run.completed', runId: 'run-voice-smoke', status: 'complete' },
          ],
        }
      },
    }
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    const client = createYuriClient()
    const transcript = await client.transcribeAudio(new Blob(['fake-webm-audio'], { type: 'audio/webm' }))
    let answer = ''
    const events: ChatEvent[] = []
    const run = await client.sendMessage({ conversationId: 'conversation-voice-smoke', text: transcript }, (event) => {
      events.push(event)
      if (event.type === 'assistant.delta') answer += event.delta
    })

    let cancelCount = 0
    let spoken: SpeechSynthesisUtterance | undefined
    const synthesis = {
      cancel: () => { cancelCount += 1 },
      speak: (utterance: SpeechSynthesisUtterance) => { spoken = utterance },
    }
    const utterance = { text: '', lang: '', onend: null, onerror: null } as unknown as SpeechSynthesisUtterance
    expect(playSpeech(synthesis, (text) => {
      Object.defineProperty(utterance, 'text', { configurable: true, value: text })
      return utterance
    }, answer, () => undefined)).toBe(true)
    cancelSpeech(synthesis)

    expect(run).toEqual({ runId: 'run-voice-smoke', status: 'complete' })
    expect(events.map((event) => event.type)).toEqual([
      'run.started', 'assistant.delta', 'assistant.delta', 'assistant.completed', 'run.completed',
    ])
    expect(spoken?.text).toBe('Проект готов.')
    expect(spoken?.lang).toBe('ru-RU')
    expect(cancelCount).toBe(2)
    expect(calls).toEqual(['stt', 'chat'])
  })
})
