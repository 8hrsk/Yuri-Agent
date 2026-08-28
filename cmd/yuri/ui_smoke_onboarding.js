(() => {
  const flow = 'onboarding'
  const steps = []
  const deadline = Date.now() + 15000

  const waitFor = async (description, predicate) => {
    while (Date.now() < deadline) {
      const value = predicate()
      if (value) return value
      await new Promise((resolve) => window.setTimeout(resolve, 50))
    }
    throw new Error(`Timed out waiting for ${description}`)
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

  const run = async () => {
    const bridge = await waitFor('Wails desktop bridge', () => window.go?.desktop?.Bridge)
    const originalGetOnboardingState = bridge.GetOnboardingState
    let onboarding = { completed: false, providerTested: false }

    bridge.GetOnboardingState = async () => onboarding
    bridge.CompleteOnboarding = async (payload) => {
      const settings = payload?.settings
      if (settings?.kind !== 'openai-compatible') throw new Error('Unexpected provider kind')
      if (settings?.baseUrl !== 'http://127.0.0.1:34116/v1') throw new Error('Base URL did not reach the typed bridge')
      if (settings?.model !== 'ui-smoke-model') throw new Error('Model did not reach the typed bridge')
      if (payload?.apiKey !== 'ui-smoke-secret-canary') throw new Error('API key did not reach the typed bridge')
      onboarding = {
        completed: true,
        providerTested: true,
        completedAt: new Date().toISOString(),
      }
      return { ok: true, message: 'UI smoke provider connected.', state: onboarding }
    }

    try {
      const welcome = await waitFor('welcome screen', () => findButton('Настроить провайдера'))
      steps.push('welcome-visible')
      welcome.click()

      const baseURL = await waitFor('provider form', () => document.querySelector('#onboarding-base-url'))
      const model = document.querySelector('#onboarding-model')
      const apiKey = document.querySelector('#onboarding-api-key')
      if (!(baseURL instanceof HTMLInputElement) || !(model instanceof HTMLInputElement) || !(apiKey instanceof HTMLInputElement)) {
        throw new Error('Provider inputs are unavailable')
      }
      steps.push('provider-form-visible')
      setInputValue(baseURL, 'http://127.0.0.1:34116/v1')
      setInputValue(model, 'ui-smoke-model')
      setInputValue(apiKey, 'ui-smoke-secret-canary')

      const submit = findButton('Сохранить и проверить')
      if (!submit) throw new Error('Provider submit button is unavailable')
      submit.click()
      steps.push('provider-submit-dispatched')

      const openChat = await waitFor('onboarding success screen', () => findButton('Открыть Chat'))
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
