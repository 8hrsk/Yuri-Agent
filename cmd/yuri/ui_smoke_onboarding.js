(() => {
  const flow = 'onboarding'
  const steps = []
  const deadline = Date.now() + 15000

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

  const setInputValue = (input, value) => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
    if (!setter) throw new Error('HTMLInputElement value setter is unavailable')
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
      const welcome = await waitFor('welcome screen', () => findButton('Создать агента'))
      welcome.click()
      const createAgent = await waitFor('agent form', () => document.querySelector('#agent-name') && findButton('Создать агента'))
      createAgent.click()
      const providerInput = await waitForAttempt(() => document.querySelector('#onboarding-base-url'), 1000)
      if (!(providerInput instanceof HTMLInputElement)) continue
      await wait(200)
      const stableInput = document.querySelector('#onboarding-base-url')
      if (stableInput instanceof HTMLInputElement) return stableInput
    }
    throw new Error('Provider screen did not become stable')
  }

  const run = async () => {
    const bridge = await waitFor('Wails desktop bridge', () => window.go?.desktop?.Bridge)
    const originalGetOnboardingState = bridge.GetOnboardingState
    let onboarding = { completed: false, providerTested: false, agentConfigured: false }
    let activeAgent

    bridge.GetOnboardingState = async () => onboarding
    bridge.ListAgents = async () => activeAgent ? [activeAgent] : []
    bridge.GetActiveAgent = async () => activeAgent
    bridge.CreateAgent = async (input) => {
      if (!input?.name) throw new Error('Agent name did not reach the typed bridge')
      activeAgent = {
        id: 'agent-ui-smoke', name: input.name, age: input.age, gender: input.gender,
        preferences: input.preferences, traits: input.traits, active: true,
        createdAt: new Date().toISOString(), updatedAt: new Date().toISOString(),
      }
      onboarding = { ...onboarding, agentConfigured: true, activeAgentId: activeAgent.id }
      return activeAgent
    }
    bridge.CompleteOnboarding = async (payload) => {
      const settings = payload?.settings
      if (settings?.kind !== 'openai-compatible') throw new Error('Unexpected provider kind')
      if (settings?.baseUrl !== 'http://127.0.0.1:34116/v1') throw new Error('Base URL did not reach the typed bridge')
      if (settings?.model !== 'ui-smoke-model') throw new Error('Model did not reach the typed bridge')
      if (payload?.apiKey !== 'ui-smoke-secret-canary') throw new Error('API key did not reach the typed bridge')
      onboarding = {
        completed: true,
        providerTested: true,
        agentConfigured: true,
        activeAgentId: activeAgent.id,
        completedAt: new Date().toISOString(),
      }
      return { ok: true, message: 'UI smoke provider connected.', state: onboarding }
    }

    try {
      await waitFor('welcome screen', () => findButton('Создать агента'))
      steps.push('welcome-visible')
      const baseURL = await enterStableProviderStep()
      const model = document.querySelector('#onboarding-model')
      const apiKey = document.querySelector('#onboarding-api-key')
      if (!(baseURL instanceof HTMLInputElement) || !(model instanceof HTMLInputElement) || !(apiKey instanceof HTMLInputElement)) {
        throw new Error('Provider inputs are unavailable')
      }
      steps.push('provider-form-visible')
      setInputValue(baseURL, 'http://127.0.0.1:34116/v1')
      setInputValue(model, 'ui-smoke-model')
      setInputValue(apiKey, 'ui-smoke-secret-canary')

      // React controlled inputs commit on the next render. Submitting in the
      // same event-loop tick would intermittently exercise the old closure.
      await wait(100)

      const submit = findButton('Сохранить и проверить')
      if (!submit) {
        const labels = Array.from(document.querySelectorAll('button')).map((button) => button.textContent?.trim()).filter(Boolean).join(' | ')
        throw new Error(`Provider submit button is unavailable; visible actions: ${labels}`)
      }
      submit.click()
      steps.push('provider-submit-dispatched')

      const outcome = await waitFor('onboarding outcome', () => {
        const openChat = findButton('Открыть Chat')
        if (openChat) return { openChat }
        const error = document.querySelector('.onboarding-feedback--error span')
        return error?.textContent ? { error: error.textContent } : undefined
      })
      if (outcome.error) throw new Error(`Onboarding rejected the UI payload: ${outcome.error}`)
      const openChat = outcome.openChat
      if (!openChat) throw new Error('Onboarding success action is unavailable')
      steps.push('success-visible')
      openChat.click()
      await waitFor('chat screen', () => document.querySelector('[aria-label="Текущий диалог"]'))
      steps.push('chat-visible')

      await report('passed')
    } finally {
      bridge.GetOnboardingState = originalGetOnboardingState
    }
  }

  void run().catch(async (error) => {
    try {
      await report('failed', error instanceof Error ? error.message : String(error))
    } catch (reportError) {
      console.error('Yuri UI smoke could not report its result', reportError)
    }
  })
})()
