(() => {
  const flow = 'voice'
  const steps = []
  const deadline = Date.now() + 20000

  const wait = (milliseconds) => new Promise((resolve) => window.setTimeout(resolve, milliseconds))
  const waitFor = async (description, predicate) => {
    while (Date.now() < deadline) {
      const value = predicate()
      if (value) return value
      await wait(50)
    }
    throw new Error(`Timed out waiting for ${description}`)
  }
  const waitForAttempt = async (predicate, milliseconds) => {
    const attemptDeadline = Date.now() + milliseconds
    while (Date.now() < attemptDeadline) {
      const value = predicate()
      if (value) return value
      await wait(50)
    }
    return undefined
  }
  const findButton = (label) => Array.from(document.querySelectorAll('button'))
    .find((button) => button.textContent?.includes(label))
  const findButtonByAria = (label) => document.querySelector(`button[aria-label="${label}"]`)
  const setInputValue = (input, value) => {
    const prototype = input instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
    const setter = Object.getOwnPropertyDescriptor(prototype, 'value')?.set
    if (!setter) throw new Error('Form value setter is unavailable')
    setter.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
    input.dispatchEvent(new Event('change', { bubbles: true }))
  }
  const report = async (state, error = '') => {
    const reporter = window.go?.main?.UISmokeReporter
    if (!reporter?.Report) throw new Error('UI smoke reporter binding is unavailable')
    await reporter.Report({ flow, state, steps, error })
  }

  const enterStableProviderStep = async () => {
    for (let attempt = 0; attempt < 5; attempt += 1) {
      const welcome = await waitFor('welcome screen', () => findButton('Настроить провайдера'))
      welcome.click()
      const providerInput = await waitForAttempt(() => document.querySelector('#onboarding-base-url'), 1000)
      if (!(providerInput instanceof HTMLInputElement)) continue
      await wait(200)
      const stableInput = document.querySelector('#onboarding-base-url')
      if (stableInput instanceof HTMLInputElement) return stableInput
    }
    throw new Error('Provider screen did not become stable')
  }

  const installVoiceBoundaries = (bridge) => {
    let mediaRequestCount = 0
    let speechCancelCount = 0
    let activeUtterance
    let spokenText = ''
    let spokenLanguage = ''

    const fakeTrack = { stop: () => undefined }
    const mediaDevices = {}
    Object.defineProperty(mediaDevices, 'getUserMedia', {
      configurable: true,
      value: async (constraints) => {
        if (!constraints?.audio) throw new Error('Audio constraint was not requested')
        mediaRequestCount += 1
        return { getTracks: () => [fakeTrack] }
      },
    })
    Object.defineProperty(navigator, 'mediaDevices', { configurable: true, value: mediaDevices })

    class FakeMediaRecorder {
      constructor(stream) {
        this.stream = stream
        this.state = 'inactive'
        this.mimeType = 'audio/webm'
        this.ondataavailable = null
        this.onstop = null
      }
      start() { this.state = 'recording' }
      stop() {
        if (this.state === 'inactive') return
        this.state = 'inactive'
        this.ondataavailable?.({ data: new Blob(['fake-webm-audio'], { type: this.mimeType }) })
        this.onstop?.()
      }
    }
    Object.defineProperty(window, 'MediaRecorder', { configurable: true, value: FakeMediaRecorder })

    class FakeSpeechSynthesisUtterance {
      constructor(text) {
        this.text = text
        this.lang = ''
        this.onend = null
        this.onerror = null
      }
    }
    const synthesis = window.speechSynthesis ?? {}
    Object.defineProperty(synthesis, 'cancel', {
      configurable: true,
      value: () => {
        speechCancelCount += 1
        const settled = activeUtterance
        activeUtterance = undefined
        settled?.onend?.()
      },
    })
    Object.defineProperty(synthesis, 'speak', {
      configurable: true,
      value: (utterance) => {
        activeUtterance = utterance
        spokenText = utterance.text
        spokenLanguage = utterance.lang
      },
    })
    Object.defineProperty(window, 'speechSynthesis', { configurable: true, value: synthesis })
    Object.defineProperty(window, 'SpeechSynthesisUtterance', { configurable: true, value: FakeSpeechSynthesisUtterance })

    bridge.ListConversations = async () => [{
      id: 'conversation-voice-ui-smoke',
      title: 'Voice UI smoke',
      preview: '',
      updatedAt: new Date().toISOString(),
      messages: [],
    }]
    bridge.TranscribeAudio = async (input) => {
      if (input?.audioBase64 !== 'ZmFrZS13ZWJtLWF1ZGlv') throw new Error('Unexpected audio payload')
      if (input?.contentType !== 'audio/webm') throw new Error('Unexpected audio content type')
      await wait(180)
      return { text: 'Расскажи коротко о статусе проекта', language: 'ru' }
    }
    bridge.SendMessage = async (input) => {
      if (input?.conversationId !== 'conversation-voice-ui-smoke') throw new Error('Unexpected conversation id')
      if (input?.text !== 'Расскажи коротко о статусе проекта') throw new Error('Unexpected transcript')
      await wait(180)
      return {
        runId: 'run-voice-ui-smoke',
        status: 'complete',
        events: [
          { type: 'run.started', runId: 'run-voice-ui-smoke' },
          { type: 'assistant.delta', runId: 'run-voice-ui-smoke', messageId: 'message-voice-ui-smoke', delta: 'Проект ' },
          { type: 'assistant.delta', runId: 'run-voice-ui-smoke', messageId: 'message-voice-ui-smoke', delta: 'готов.' },
          { type: 'assistant.completed', runId: 'run-voice-ui-smoke', messageId: 'message-voice-ui-smoke' },
          { type: 'run.completed', runId: 'run-voice-ui-smoke', status: 'complete' },
        ],
      }
    }

    return {
      mediaRequestCount: () => mediaRequestCount,
      speechCancelCount: () => speechCancelCount,
      spokenText: () => spokenText,
      spokenLanguage: () => spokenLanguage,
    }
  }

  const enterChat = async (bridge) => {
    let onboarding = { completed: false, providerTested: false }
    bridge.GetOnboardingState = async () => onboarding
    bridge.CompleteOnboarding = async () => {
      onboarding = { completed: true, providerTested: true, completedAt: new Date().toISOString() }
      return { ok: true, message: 'Voice UI smoke provider connected.', state: onboarding }
    }

    const baseURL = await enterStableProviderStep()
    const model = document.querySelector('#onboarding-model')
    if (!(baseURL instanceof HTMLInputElement) || !(model instanceof HTMLInputElement)) throw new Error('Provider form is unavailable')
    setInputValue(baseURL, 'http://127.0.0.1:34116/v1')
    setInputValue(model, 'voice-ui-smoke-model')
    const submit = findButton('Сохранить и проверить')
    if (!submit) throw new Error('Provider submit button is unavailable')
    submit.click()
    const openChat = await waitFor('onboarding success screen', () => findButton('Открыть Chat'))
    const boundaries = installVoiceBoundaries(bridge)
    openChat.click()
    await waitFor('chat screen', () => document.querySelector('[aria-label="Текущий диалог"]'))
    steps.push('chat-visible')
    return boundaries
  }

  const run = async () => {
    const bridge = await waitFor('Wails desktop bridge', () => window.go?.desktop?.Bridge)
    const boundaries = await enterChat(bridge)
    await waitFor('speech controls', () => findButtonByAria('Озвучить ответ') || findButtonByAria('Включить автоматическую озвучку ответов'))
    steps.push('voice-boundaries-ready')

    const microphone = await waitFor('microphone button', () => findButtonByAria('Записать голосовое сообщение'))
    microphone.click()
    await waitFor('recording state', () => findButtonByAria('Остановить запись'))
    steps.push('recording-visible')

    findButtonByAria('Остановить запись')?.click()
    await waitFor('transcribing state', () => document.body.textContent?.includes('Yuri распознаёт голос'))
    steps.push('transcribing-visible')
    const composer = await waitFor('recognized transcript', () => {
      const input = document.querySelector('[aria-label="Сообщение Yuri"]')
      return input instanceof HTMLTextAreaElement && input.value === 'Расскажи коротко о статусе проекта' ? input : undefined
    })
    steps.push('transcript-visible')

    const send = findButtonByAria('Отправить сообщение')
    if (!send || send.disabled) throw new Error('Recognized transcript cannot be sent')
    send.click()
    await waitFor('thinking state', () => document.body.textContent?.includes('Yuri думает'))
    steps.push('agent-thinking-visible')
    await waitFor('assistant response', () => Array.from(document.querySelectorAll('.message__content')).find((node) => node.textContent?.includes('Проект готов.')))
    steps.push('assistant-response-visible')

    const listen = await waitFor('listen action', () => findButtonByAria('Озвучить ответ'))
    listen.click()
    await waitFor('speaking state', () => findButtonByAria('Остановить озвучивание'))
    if (boundaries.spokenText() !== 'Проект готов.' || boundaries.spokenLanguage() !== 'ru-RU') {
      throw new Error('TTS did not receive the completed Russian response')
    }
    steps.push('tts-speaking-visible')

    findButtonByAria('Записать голосовое сообщение')?.click()
    await waitFor('barge-in recording state', () => findButtonByAria('Остановить запись'))
    if (boundaries.speechCancelCount() < 2 || boundaries.mediaRequestCount() < 2) {
      throw new Error(`Barge-in counters are incomplete: speech=${boundaries.speechCancelCount()}, media=${boundaries.mediaRequestCount()}`)
    }
    steps.push('barge-in-visible')
    await report('passed')
  }

  void run().catch(async (error) => {
    try {
      await report('failed', error instanceof Error ? error.message : String(error))
    } catch (reportError) {
      console.error('Yuri voice UI smoke could not report its result', reportError)
    }
  })
})()
