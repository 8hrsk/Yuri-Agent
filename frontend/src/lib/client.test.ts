import { beforeEach, describe, expect, it } from 'vitest'

import {
  canUseNativeNotification,
  createYuriClient,
  requestBrowserNotificationPermission,
  resetYuriClientForTests,
  subscribeMemoryUpdates,
  subscribeNotifications,
  type MemoryUpdateEvent,
} from './client'
import { defaultAgentDraft } from './agents'

type RuntimeListener = (value: unknown) => void

/**
 * Wails v2 keeps a *list* of listeners per event name: `EventsOn` appends one
 * and hands back an unregister for that listener alone, while `EventsOff(name)`
 * tears down the whole list at once.
 *
 * The previous fake stored a single callback per name (`listeners.set(name, cb)`
 * / `listeners.delete(name)`), which made `EventsOff` look like a per-listener
 * cleanup and hid H-7 — one run's teardown silencing every other run — from the
 * whole suite. Modelling the real shape is what lets these tests see it.
 */
function createWailsEventBus(options: { unregisterable?: boolean } = {}) {
  const listeners = new Map<string, RuntimeListener[]>()
  return {
    listenerCount: (name: string) => listeners.get(name)?.length ?? 0,
    emit: (name: string, value: unknown) => {
      for (const listener of [...(listeners.get(name) ?? [])]) listener(value)
    },
    runtime: {
      EventsOn: (name: string, callback: RuntimeListener) => {
        const current = listeners.get(name) ?? []
        current.push(callback)
        listeners.set(name, current)
        // Not every runtime build hands an unregister back; `unregisterable:
        // false` models that one, where the only defence left is the caller's
        // own `active` guard.
        if (options.unregisterable === false) return undefined
        return () => {
          const registered = listeners.get(name)
          if (!registered) return
          const index = registered.indexOf(callback)
          if (index >= 0) registered.splice(index, 1)
          if (registered.length === 0) listeners.delete(name)
        }
      },
      EventsOff: (name: string) => { listeners.delete(name) },
    },
  }
}

/** Installs a fake `window` for the duration of `run` and always restores it. */
async function withWindow(value: unknown, run: () => Promise<void>): Promise<void> {
  const previousWindow = (globalThis as { window?: unknown }).window
  Object.defineProperty(globalThis, 'window', { configurable: true, value })
  resetYuriClientForTests()
  try {
    await run()
  } finally {
    if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
    else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
    resetYuriClientForTests()
  }
}

describe('Yuri client contract', () => {
  beforeEach(() => {
    resetYuriClientForTests()
  })

  it('provides a usable local preview when Wails bindings are absent', async () => {
    const client = createYuriClient()
    const conversations = await client.listConversations()

    expect(client.mode).toBe('mock')
    expect(conversations[0]?.id).toBe('conversation-welcome')

    const events: string[] = []
    const result = await client.sendMessage(
      { conversationId: 'conversation-welcome', text: 'Привет, Yuri' },
      (event) => events.push(event.type),
    )

    expect(result.status).toBe('complete')
    expect(events).toContain('run.started')
    expect(events).toContain('assistant.delta')
    expect(events).toContain('assistant.completed')
    expect(events.at(-1)).toBe('run.completed')
  })

  it('forwards an explicit conversation rename and normalizes the returned source', async () => {
    const calls: unknown[] = []
    await withWindow({
      go: {
        main: {
          Bridge: {
            ListConversations: () => [],
            RenameConversation: (input: unknown) => {
              calls.push(input)
              return {
                id: 'conversation-rename',
                title: 'План релиза',
                title_source: 'user',
                preview: '',
                updated_at: '2026-08-30T10:00:00.000Z',
                messages: [],
              }
            },
          },
        },
      },
    }, async () => {
      const conversation = await createYuriClient().renameConversation('conversation-rename', 'План релиза')
      expect(calls).toEqual([{ id: 'conversation-rename', conversationId: 'conversation-rename', title: 'План релиза' }])
      expect(conversation).toMatchObject({ id: 'conversation-rename', title: 'План релиза', titleSource: 'user' })
    })
  })

  it('forwards an active-agent model route and normalizes configured providers', async () => {
    const calls: unknown[] = []
    await withWindow({
      go: {
        main: {
          Bridge: {
            ListConversations: () => [],
            ListProviders: () => [{ id: 'openrouter', kind: 'openai-compatible', displayName: 'OpenRouter', model: 'openrouter/free', enabled: false, hasSecret: true }],
            UpdateActiveAgentModelRoute: (input: unknown) => {
              calls.push(input)
              return {
                id: 'agent-emily', name: 'Emily', gender: 'female', providerId: 'openrouter', model: 'openrouter/free',
                active: true, createdAt: '2026-09-01T00:00:00Z', updatedAt: '2026-09-01T00:01:00Z',
              }
            },
          },
        },
      },
    }, async () => {
      const client = createYuriClient()
      await expect(client.listProviders()).resolves.toEqual([
        { id: 'openrouter', kind: 'openai-compatible', displayName: 'OpenRouter', model: 'openrouter/free', enabled: false, hasSecret: true },
      ])
      await expect(client.updateActiveAgentModelRoute('openrouter', 'openrouter/free')).resolves.toMatchObject({
        id: 'agent-emily', providerId: 'openrouter', model: 'openrouter/free',
      })
      expect(calls).toEqual([{ providerId: 'openrouter', model: 'openrouter/free' }])
    })
  })

  it('routes rendered web and local links through dedicated Wails bridge methods', async () => {
    const calls: Array<{ name: string; value: string }> = []
    await withWindow({
      go: {
        main: {
          Bridge: {
            ListConversations: () => [],
            OpenExternalURL: (url: string) => calls.push({ name: 'OpenExternalURL', value: url }),
            OpenLocalPath: (path: string) => calls.push({ name: 'OpenLocalPath', value: path }),
          },
        },
      },
    }, async () => {
      const client = createYuriClient()
      await client.openExternalURL('https://example.test/docs')
      await client.openLocalPath('/Users/owner/My Project/main.go')

      expect(calls).toEqual([
        { name: 'OpenExternalURL', value: 'https://example.test/docs' },
        { name: 'OpenLocalPath', value: '/Users/owner/My Project/main.go' },
      ])
    })
  })

  it('keeps the mock first-run gate closed until provider probe succeeds', async () => {
    const client = createYuriClient()
    expect(await client.getOnboardingState()).toEqual({ completed: false, providerTested: false, agentConfigured: false })

    await client.createAgent(defaultAgentDraft)

    const settings = (await client.getProviderSnapshot()).settings
    const result = await client.completeOnboarding(settings)

    expect(result).toMatchObject({
      ok: true,
      state: { completed: true, providerTested: true, agentConfigured: true },
    })
    expect(await client.getOnboardingState()).toMatchObject({ completed: true, providerTested: true })
  })

  it('reports Antigravity as unsupported without completing onboarding', async () => {
    const client = createYuriClient()
    const current = (await client.getProviderSnapshot()).settings
    const result = await client.completeOnboarding({ ...current, kind: 'antigravity' }, 'must-not-be-stored')

    expect(result).toMatchObject({
      ok: false,
      errorCode: 'unsupported_auth_mode',
      alternative: 'openai-compatible-api-key',
      state: { completed: false, providerTested: false },
    })
    expect(await client.getOnboardingState()).toEqual({ completed: false, providerTested: false, agentConfigured: false })
  })

  it('logs the mock Codex account out and closes the provider gate', async () => {
    const client = createYuriClient()
    await client.loginCodex()
    const settings = { ...(await client.getProviderSnapshot()).settings, kind: 'codex-app-server' as const }
    await client.testProvider(settings)

    await expect(client.logoutCodex()).resolves.toEqual({
      disconnected: true,
      onboarding: { completed: false, providerTested: false, agentConfigured: false },
    })
    expect((await client.getProviderSnapshot()).codex.connected).toBe(false)
    expect(await client.getOnboardingState()).toEqual({ completed: false, providerTested: false, agentConfigured: false })
  })

  it('forwards Codex logout and requires an explicit backend confirmation', async () => {
    const calls: string[] = []
    const bridge = {
      ListConversations: () => [],
      CodexLogout: () => {
        calls.push('CodexLogout')
        return { disconnected: true, onboarding: { completed: false, provider_tested: false } }
      },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      await expect(createYuriClient().logoutCodex()).resolves.toEqual({
        disconnected: true,
        onboarding: { completed: false, providerTested: false, agentConfigured: false },
      })
      expect(calls).toEqual(['CodexLogout'])
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('forwards typed first-run state and atomic completion through Wails', async () => {
    const calls: Array<{ name: string; args: unknown[] }> = []
    let onboardingComplete = false
    const bridge = {
      GetOnboardingState: () => {
        calls.push({ name: 'GetOnboardingState', args: [] })
        return { completed: onboardingComplete, provider_tested: onboardingComplete, agent_configured: true, active_agent_id: 'agent-yuri' }
      },
      CompleteOnboarding: (input: unknown) => {
        calls.push({ name: 'CompleteOnboarding', args: [input] })
        onboardingComplete = true
        return {
          ok: true,
          message: 'Endpoint отвечает.',
          state: { completed: true, provider_tested: true, agent_configured: true, active_agent_id: 'agent-yuri', completed_at: '2026-08-28T12:00:00Z' },
        }
      },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      const client = createYuriClient()
      expect(client.mode).toBe('wails')
      await expect(client.getOnboardingState()).resolves.toEqual({ completed: false, providerTested: false, agentConfigured: true, activeAgentId: 'agent-yuri' })
      const result = await client.completeOnboarding({
        kind: 'openai-compatible',
        baseUrl: 'https://api.example.test/v1',
        model: 'test-model',
        apiStyle: 'responses',
        apiKeyConfigured: false,
        favoriteModels: [],
        timeoutSeconds: 90,
        streamResponses: true,
      }, 'preview-key')

      expect(result).toMatchObject({ ok: true, state: { completed: true, providerTested: true, agentConfigured: true } })
      expect(calls).toEqual([
        { name: 'GetOnboardingState', args: [] },
        {
          name: 'CompleteOnboarding',
          args: [{
            settings: {
              kind: 'openai-compatible',
              baseUrl: 'https://api.example.test/v1',
              model: 'test-model',
              apiStyle: 'responses',
              apiKeyConfigured: false,
              timeoutSeconds: 90,
              streamResponses: true,
            },
            apiKey: 'preview-key',
          }],
        },
        { name: 'GetOnboardingState', args: [] },
      ])
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('does not start Codex while reading a clean first-run provider snapshot', async () => {
    let codexCalls = 0
    const bridge = {
      ListProviders: () => [],
      CodexAccount: () => { codexCalls += 1; return { account: null } },
      CodexRateLimits: () => { codexCalls += 1; return undefined },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      const snapshot = await createYuriClient().getProviderSnapshot()
      expect(snapshot.settings.kind).toBe('openai-compatible')
      expect(snapshot.codex.connected).toBe(false)
      expect(codexCalls).toBe(0)
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('saves an OpenRouter credential before activation and projects a secret-free model catalog', async () => {
    const calls: Array<{ name: string; input: unknown }> = []
    const bridge = {
      ListConversations: () => [],
      SaveOpenAIProviderCredential: (input: unknown) => { calls.push({ name: 'credential', input }); return { id: 'openrouter' } },
      ListOpenAIModels: (input: unknown) => {
        calls.push({ name: 'models', input })
        return [{
          id: 'openrouter/free', name: 'Free Router', context_length: 200000,
          max_completion_tokens: 16384, prompt_price: '0', completion_price: '0',
          free: true, supports_tools: true, supports_vision: false, supports_structured_output: true, supports_json_schema: true,
          input_modalities: ['text'], output_modalities: ['text'], favorite: false,
        }]
      },
      SetProviderModelFavorite: (input: unknown) => { calls.push({ name: 'favorite', input }); return { id: 'openrouter' } },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      const client = createYuriClient()
      const settings = {
        kind: 'openai-compatible' as const,
        providerId: 'openrouter', displayName: 'OpenRouter', baseUrl: 'https://openrouter.ai/api/v1', model: '',
        apiStyle: 'chat_completions' as const, apiKeyConfigured: false, favoriteModels: [], timeoutSeconds: 90, streamResponses: true,
      }
      await expect(client.connectOpenAIProvider(settings, 'sk-or-secret')).resolves.toEqual([expect.objectContaining({ id: 'openrouter/free', contextLength: 200000, free: true, supportsTools: true, supportsVision: false, supportsStructuredOutput: true, supportsJSONSchema: true })])
      await client.getOpenAIModels('openrouter', 'throughput-high-to-low')
      await client.setOpenAIModelFavorite('openrouter', 'openrouter/free', true)

      expect(calls).toEqual([
        { name: 'credential', input: { id: 'openrouter', displayName: 'OpenRouter', baseUrl: 'https://openrouter.ai/api/v1', apiStyle: 'chat_completions', apiKey: 'sk-or-secret' } },
        { name: 'models', input: { providerId: 'openrouter', sort: '' } },
        { name: 'models', input: { providerId: 'openrouter', sort: 'throughput-high-to-low' } },
        { name: 'favorite', input: { providerId: 'openrouter', model: 'openrouter/free', favorite: true } },
      ])
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('normalizes and forwards named-agent roster methods through Wails', async () => {
    const calls: Array<{ name: string; args: unknown[] }> = []
    const wireAgent = {
      id: 'agent-yuri', name: 'Юри', age: 21, gender: 'female', preferences: 'Коротко', backstory: 'Выросла среди старых карт.',
      traits: { warmth: 0.6 }, active: true, created_at: '2026-08-29T00:00:00Z', updated_at: '2026-08-29T00:00:00Z',
    }
    const wirePersonalization = {
      agentId: 'agent-yuri', schemaVersion: 2, version: 1, revisionId: 'revision-1', operation: 'create',
      identity: defaultAgentDraft.personalization.identity,
      communicationStyle: defaultAgentDraft.personalization.communicationStyle,
      temperament: { warmth: 0.6 },
      emotionalDynamics: defaultAgentDraft.personalization.emotionalDynamics,
      relationshipSeed: defaultAgentDraft.personalization.relationshipSeed,
      backstory: defaultAgentDraft.personalization.structuredBackstory,
      evolutionPolicy: defaultAgentDraft.personalization.evolutionPolicy,
      createdAt: '2026-08-29T00:00:00Z', updatedAt: '2026-08-29T00:00:00Z',
    }
    const personalizationUpdate = {
      expectedVersion: 1,
      traits: { ...defaultAgentDraft.traits, warmth: 0.72 },
      personalization: defaultAgentDraft.personalization,
      reason: 'Owner adjusted the reset baseline',
    }
    const portableProfile = { path: '/tmp/yuri-agent-profile.json', exportedAt: '2026-08-31T12:00:00Z', sizeBytes: 2048, checksum: 'sha256:abc', profile: defaultAgentDraft }
    const bridge = {
      ListConversations: () => [],
      ListAgents: () => { calls.push({ name: 'ListAgents', args: [] }); return [wireAgent] },
      GetActiveAgent: () => { calls.push({ name: 'GetActiveAgent', args: [] }); return wireAgent },
      GetActiveAgentPersonalization: () => { calls.push({ name: 'GetActiveAgentPersonalization', args: [] }); return wirePersonalization },
      UpdateActiveAgentPersonalization: (input: unknown) => { calls.push({ name: 'UpdateActiveAgentPersonalization', args: [input] }); return { ...wirePersonalization, version: 2, revisionId: 'revision-2' } },
      CreateAgent: (input: unknown) => { calls.push({ name: 'CreateAgent', args: [input] }); return wireAgent },
      ExportActiveAgentProfile: (input: unknown) => { calls.push({ name: 'ExportActiveAgentProfile', args: [input] }); return portableProfile },
      OpenPortableAgentProfile: (input: unknown) => { calls.push({ name: 'OpenPortableAgentProfile', args: [input] }); return portableProfile },
      SetActiveAgent: (input: unknown) => { calls.push({ name: 'SetActiveAgent', args: [input] }); return wireAgent },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()
    try {
      const client = createYuriClient()
      await expect(client.listAgents()).resolves.toMatchObject([{ id: 'agent-yuri', name: 'Юри', backstory: 'Выросла среди старых карт.', traits: { warmth: 0.6 }, active: true }])
      await expect(client.getActiveAgent()).resolves.toMatchObject({ id: 'agent-yuri', backstory: 'Выросла среди старых карт.' })
      await expect(client.getActiveAgentPersonalization()).resolves.toMatchObject({ agentId: 'agent-yuri', schemaVersion: 2, revisionId: 'revision-1' })
      await expect(client.updateActiveAgentPersonalization(personalizationUpdate)).resolves.toMatchObject({ agentId: 'agent-yuri', version: 2, revisionId: 'revision-2' })
      await expect(client.createAgent(defaultAgentDraft)).resolves.toMatchObject({ id: 'agent-yuri', backstory: 'Выросла среди старых карт.' })
      await expect(client.exportActiveAgentProfile()).resolves.toMatchObject({ path: '/tmp/yuri-agent-profile.json', profile: { name: 'Yuri', creationMode: 'advanced', presetId: 'custom' } })
      await expect(client.openPortableAgentProfile()).resolves.toMatchObject({ checksum: 'sha256:abc', profile: { name: 'Yuri', creationMode: 'advanced' } })
      await expect(client.setActiveAgent('agent-yuri')).resolves.toMatchObject({ id: 'agent-yuri', active: true })
      expect(calls).toEqual([
        { name: 'ListAgents', args: [] },
        { name: 'GetActiveAgent', args: [] },
        { name: 'GetActiveAgentPersonalization', args: [] },
        { name: 'UpdateActiveAgentPersonalization', args: [personalizationUpdate] },
        { name: 'CreateAgent', args: [defaultAgentDraft] },
        { name: 'ExportActiveAgentProfile', args: [{}] },
        { name: 'OpenPortableAgentProfile', args: [{}] },
        { name: 'SetActiveAgent', args: [{ id: 'agent-yuri' }] },
      ])
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('forwards an isolated personality preview with the selected scenario', async () => {
    const calls: unknown[] = []
    await withWindow({
      go: { main: { Bridge: {
        ListConversations: () => [],
        PreviewAgentPersonality: (input: unknown) => {
          calls.push(input)
          return {
            scenario: 'fear', scenarioTitle: 'Тревожная ситуация', prompt: 'Мне тревожно.',
            response: 'Я рядом.', model: 'test-model', compilerCharacters: 900, influences: [],
          }
        },
      } } },
    }, async () => {
      await expect(createYuriClient().previewAgentPersonality(defaultAgentDraft, 'fear')).resolves.toMatchObject({
        scenario: 'fear', response: 'Я рядом.', model: 'test-model',
      })
      expect(calls).toEqual([{ profile: defaultAgentDraft, scenario: 'fear' }])
    })
  })

  it('lists Codex account models through the Wails bridge', async () => {
    const bridge = {
      ListConversations: () => [],
      CodexModels: () => [{
        id: 'model-1', model: 'gpt-current', display_name: 'GPT Current',
        description: 'Current account model', is_default: true,
        default_reasoning_effort: 'medium', input_modalities: ['text', 'image'],
      }],
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      await expect(createYuriClient().getCodexModels()).resolves.toEqual([{
        id: 'model-1', model: 'gpt-current', displayName: 'GPT Current',
        description: 'Current account model', isDefault: true,
        defaultReasoningEffort: 'medium', inputModalities: ['text', 'image'],
      }])
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('normalizes persisted execution traces and available chat tools from Wails', async () => {
    const bridge = {
      ListConversations: () => [{
        id: 'conversation-trace',
        title: 'История tools',
        updatedAt: '2026-08-29T10:00:05Z',
        messages: [],
        traces: [{
          id: 'run-history',
          kind: 'interactive',
          status: 'completed',
          createdAt: '2026-08-29T10:00:00Z',
          startedAt: '2026-08-29T10:00:01Z',
          finishedAt: '2026-08-29T10:00:05Z',
          toolCalls: [{ id: 'call-history', name: 'filesystem.read', risk: 'low', status: 'completed', args: { path: '/allowed/readme.txt' }, result: 'ok' }],
        }],
      }],
      ListChatTools: () => [{ name: 'filesystem.read', label: 'Чтение файлов', risk: 'low', capabilities: ['filesystem.read'] }],
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      const client = createYuriClient()
      await expect(client.listChatTools()).resolves.toEqual([{
        id: 'filesystem.read', name: 'filesystem.read', label: 'Чтение файлов', risk: 'low', available: true,
        requiresApproval: false, description: undefined, capabilities: ['filesystem.read'],
      }])
      const conversation = (await client.listConversations())[0]
      expect(conversation?.traces?.[0]?.steps.map((step) => step.kind)).toEqual(['thinking', 'tool', 'completion'])
      expect(conversation?.traces?.[0]?.steps.find((step) => step.kind === 'tool')).toMatchObject({ toolCall: { name: 'filesystem.read', result: 'ok' } })
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('loads and saves the SearXNG search adapter settings through Wails', async () => {
    const saved: unknown[] = []
    await withWindow({
      go: { main: { Bridge: {
        ListConversations: () => [],
        GetWebSearchSettings: () => ({ enabled: true, provider: 'searxng', endpoint: 'https://search.example.com', default_result_limit: 7 }),
        SaveWebSearchSettings: (input: unknown) => { saved.push(input) },
        TestWebSearchSettings: () => ({ ok: true, message: 'SearXNG отвечает.' }),
      } } },
    }, async () => {
      const client = createYuriClient()
      const settings = await client.getWebSearchSettings()
      expect(settings).toEqual({ enabled: true, provider: 'searxng', endpoint: 'https://search.example.com', defaultResultLimit: 7 })
      await client.saveWebSearchSettings({ ...settings, defaultResultLimit: 4 })
      await expect(client.testWebSearchSettings(settings)).resolves.toEqual({ ok: true, message: 'SearXNG отвечает.' })
      expect(saved).toEqual([{ enabled: true, provider: 'searxng', endpoint: 'https://search.example.com', defaultResultLimit: 4 }])
    })
  })

  it('preserves repeated live streaming deltas while suppressing returned replay events', async () => {
    const bus = createWailsEventBus()
    const streamed = [
      { type: 'run.started', runId: 'run-1' },
      { type: 'assistant.delta', runId: 'run-1', messageId: 'message-1', delta: 'Я' },
      { type: 'assistant.delta', runId: 'run-1', messageId: 'message-1', delta: ' ' },
      { type: 'assistant.delta', runId: 'run-1', messageId: 'message-1', delta: ' ' },
      { type: 'assistant.delta', runId: 'run-1', messageId: 'message-1', delta: 'здесь' },
      { type: 'assistant.completed', runId: 'run-1', messageId: 'message-1' },
      { type: 'run.completed', runId: 'run-1', status: 'complete' },
    ]
    const bridge = {
      ListConversations: () => [],
      SendMessage: () => {
        streamed.forEach((event) => bus.emit('yuri:chat', event))
        return { runId: 'run-1', status: 'complete', events: streamed }
      },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: {
        go: { main: { Bridge: bridge } },
        runtime: bus.runtime,
      },
    })
    resetYuriClientForTests()

    try {
      const deltas: string[] = []
      await createYuriClient().sendMessage({ conversationId: 'conversation-1', text: 'Привет' }, (event) => {
        if (event.type === 'assistant.delta') deltas.push(event.delta)
      })
      expect(deltas).toEqual(['Я', ' ', ' ', 'здесь'])
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('keeps two concurrent runs on separate streams and lets neither silence the other (H-7)', async () => {
    const bus = createWailsEventBus()
    const finish = new Map<string, (result: unknown) => void>()
    const bridge = {
      ListConversations: () => [],
      SendMessage: (request: { text: string }) => new Promise((resolve) => finish.set(request.text, resolve)),
    }

    await withWindow({ go: { main: { Bridge: bridge } }, runtime: bus.runtime }, async () => {
      const client = createYuriClient()
      const first: string[] = []
      const second: string[] = []

      // Both runs live in the same conversation: the user sends, leaves the tab,
      // comes back and sends again while the first run is still going.
      const firstRun = client.sendMessage({ conversationId: 'conversation-1', text: 'первый' }, (event) => {
        if (event.type === 'assistant.delta') first.push(event.delta)
      })
      const secondRun = client.sendMessage({ conversationId: 'conversation-1', text: 'второй' }, (event) => {
        if (event.type === 'assistant.delta') second.push(event.delta)
      })

      // Go mints the ids and announces each run before any text arrives, which
      // is what lets each subscription claim the run it owns.
      bus.emit('yuri:chat', { type: 'run.started', runId: 'run-a', conversationId: 'conversation-1' })
      bus.emit('yuri:chat', { type: 'run.started', runId: 'run-b', conversationId: 'conversation-1' })
      bus.emit('yuri:chat', { type: 'assistant.delta', runId: 'run-a', conversationId: 'conversation-1', messageId: 'message-a', delta: 'A1' })
      bus.emit('yuri:chat', { type: 'assistant.delta', runId: 'run-b', conversationId: 'conversation-1', messageId: 'message-b', delta: 'B1' })

      // Neither run has picked up the other's text.
      expect(first).toEqual(['A1'])
      expect(second).toEqual(['B1'])

      // The first run finishes and tears its subscription down.
      bus.emit('yuri:chat', { type: 'run.completed', runId: 'run-a', conversationId: 'conversation-1', status: 'complete' })
      finish.get('первый')?.({ runId: 'run-a', status: 'complete', events: [] })
      await expect(firstRun).resolves.toMatchObject({ runId: 'run-a', status: 'complete' })

      // …and the second run, still active, keeps streaming. Under the old
      // `EventsOff('yuri:chat')` cleanup this delta reached nobody.
      bus.emit('yuri:chat', { type: 'assistant.delta', runId: 'run-b', conversationId: 'conversation-1', messageId: 'message-b', delta: 'B2' })
      expect(second).toEqual(['B1', 'B2'])

      bus.emit('yuri:chat', { type: 'run.completed', runId: 'run-b', conversationId: 'conversation-1', status: 'complete' })
      finish.get('второй')?.({ runId: 'run-b', status: 'complete', events: [] })
      await expect(secondRun).resolves.toMatchObject({ runId: 'run-b', status: 'complete' })

      expect(first).toEqual(['A1'])
      expect(second).toEqual(['B1', 'B2'])
    })
  })

  it('releases only the finished run from the chat bus and drops the bus when the last run leaves', async () => {
    const bus = createWailsEventBus()
    const finish = new Map<string, (result: unknown) => void>()
    const bridge = {
      ListConversations: () => [],
      SendMessage: (request: { conversationId: string }) => new Promise((resolve) => finish.set(request.conversationId, resolve)),
    }

    await withWindow({ go: { main: { Bridge: bridge } }, runtime: bus.runtime }, async () => {
      const client = createYuriClient()
      const second: string[] = []

      const firstRun = client.sendMessage({ conversationId: 'conversation-a', text: 'первый' }, () => undefined)
      const secondRun = client.sendMessage({ conversationId: 'conversation-b', text: 'второй' }, (event) => second.push(event.type))

      // One shared bus listener multiplexes both runs.
      expect(bus.listenerCount('yuri:chat')).toBe(1)

      bus.emit('yuri:chat', { type: 'run.started', runId: 'run-a', conversationId: 'conversation-a' })
      bus.emit('yuri:chat', { type: 'run.started', runId: 'run-b', conversationId: 'conversation-b' })

      finish.get('conversation-a')?.({ runId: 'run-a', status: 'complete', events: [] })
      await firstRun

      // The bus survives the first run's cleanup, and the second run still hears it.
      expect(bus.listenerCount('yuri:chat')).toBe(1)
      bus.emit('yuri:chat', { type: 'assistant.completed', runId: 'run-b', conversationId: 'conversation-b', messageId: 'message-b' })
      expect(second).toEqual(['run.started', 'assistant.completed'])

      finish.get('conversation-b')?.({ runId: 'run-b', status: 'complete', events: [] })
      await secondRun

      // Nothing is left registered once the last run releases.
      expect(bus.listenerCount('yuri:chat')).toBe(0)

      // A straggler for a retired run is not adopted by anyone.
      bus.emit('yuri:chat', { type: 'assistant.completed', runId: 'run-b', conversationId: 'conversation-b', messageId: 'message-b' })
      expect(second).toEqual(['run.started', 'assistant.completed'])
    })
  })

  it('renders each lifecycle event once when the return value replays what the live stream already delivered', async () => {
    const bus = createWailsEventBus()
    // Mirrors ChatRunResult.Events: lifecycle events only, all of them already
    // dispatched on the bus (chatEmitter.record skips assistant.delta).
    const lifecycle = [
      { type: 'run.started', runId: 'run-1', conversationId: 'conversation-1', createdAt: '2026-08-29T10:00:00.000Z' },
      { type: 'assistant.completed', runId: 'run-1', conversationId: 'conversation-1', messageId: 'message-1', createdAt: '2026-08-29T10:00:02.000Z' },
      { type: 'run.completed', runId: 'run-1', conversationId: 'conversation-1', status: 'complete', createdAt: '2026-08-29T10:00:02.100Z' },
    ]
    const bridge = {
      ListConversations: () => [],
      SendMessage: () => {
        bus.emit('yuri:chat', lifecycle[0])
        bus.emit('yuri:chat', { type: 'assistant.delta', runId: 'run-1', conversationId: 'conversation-1', messageId: 'message-1', delta: 'Готово' })
        bus.emit('yuri:chat', lifecycle[1])
        bus.emit('yuri:chat', lifecycle[2])
        return { runId: 'run-1', status: 'complete', events: lifecycle }
      },
    }

    await withWindow({ go: { main: { Bridge: bridge } }, runtime: bus.runtime }, async () => {
      const seen: string[] = []
      await createYuriClient().sendMessage({ conversationId: 'conversation-1', text: 'Привет' }, (event) => seen.push(event.type))
      expect(seen).toEqual(['run.started', 'assistant.delta', 'assistant.completed', 'run.completed'])
    })
  })

  it('replays the returned events in full when no live stream exists', async () => {
    const bridge = {
      ListConversations: () => [],
      SendMessage: () => ({
        runId: 'run-1',
        status: 'complete',
        events: [
          { type: 'run.started', runId: 'run-1', conversationId: 'conversation-1' },
          { type: 'assistant.delta', runId: 'run-1', conversationId: 'conversation-1', messageId: 'message-1', delta: 'Готово' },
          { type: 'assistant.completed', runId: 'run-1', conversationId: 'conversation-1', messageId: 'message-1' },
          { type: 'run.completed', runId: 'run-1', conversationId: 'conversation-1', status: 'complete' },
        ],
      }),
    }

    // No `runtime`, so there is nothing to subscribe to and the replay is the
    // only delivery path.
    await withWindow({ go: { main: { Bridge: bridge } } }, async () => {
      const seen: string[] = []
      await createYuriClient().sendMessage({ conversationId: 'conversation-1', text: 'Привет' }, (event) => seen.push(event.type))
      expect(seen).toEqual(['run.started', 'assistant.delta', 'assistant.completed', 'run.completed'])
    })
  })

  it('forwards the memory update payload instead of discarding it', async () => {
    const bus = createWailsEventBus()
    await withWindow({ go: { main: { Bridge: { ListConversations: () => [] } } }, runtime: bus.runtime }, async () => {
      const updates: MemoryUpdateEvent[] = []
      const unsubscribe = subscribeMemoryUpdates((update) => updates.push(update))

      bus.emit('yuri:memory', { type: 'memory.updated', writes: 3 })
      bus.emit('yuri:memory', { data: { type: 'memory.updated', writes: '2' } })
      expect(updates).toEqual([
        { type: 'memory.updated', writes: 3 },
        { type: 'memory.updated', writes: 2 },
      ])

      unsubscribe()
      expect(bus.listenerCount('yuri:memory')).toBe(0)
      bus.emit('yuri:memory', { type: 'memory.updated', writes: 9 })
      expect(updates).toHaveLength(2)
    })
  })

  it('unsubscribes one listener without silencing the others on the same event (H-7)', async () => {
    const bus = createWailsEventBus()
    await withWindow({ go: { main: { Bridge: { ListConversations: () => [] } } }, runtime: bus.runtime }, async () => {
      const first: number[] = []
      const second: number[] = []
      const releaseFirst = subscribeMemoryUpdates((update) => first.push(update.writes))
      subscribeMemoryUpdates((update) => second.push(update.writes))
      expect(bus.listenerCount('yuri:memory')).toBe(2)

      releaseFirst()

      // The old cleanup was `EventsOff('yuri:memory')`, which drops the whole
      // registration: the surviving subscriber went deaf along with the one
      // that actually asked to leave.
      expect(bus.listenerCount('yuri:memory')).toBe(1)
      bus.emit('yuri:memory', { type: 'memory.updated', writes: 5 })
      expect(first).toEqual([])
      expect(second).toEqual([5])
    })
  })

  it('leaves a released listener inert when the runtime returns no unregister (H-7)', async () => {
    const bus = createWailsEventBus({ unregisterable: false })
    await withWindow({ go: { main: { Bridge: { ListConversations: () => [] } } }, runtime: bus.runtime }, async () => {
      const first: number[] = []
      const second: number[] = []
      const releaseFirst = subscribeMemoryUpdates((update) => first.push(update.writes))
      subscribeMemoryUpdates((update) => second.push(update.writes))

      releaseFirst()

      // Nothing can be unregistered here, so the released listener stays on the
      // bus — the `active` guard is what stops it delivering. Reaching for
      // `EventsOff` instead would have taken the survivor down too.
      bus.emit('yuri:memory', { type: 'memory.updated', writes: 5 })
      expect(first).toEqual([])
      expect(second).toEqual([5])
    })
  })

  it('holds a side effect at approval until the user resolves it', async () => {
    const client = createYuriClient()
    const events: string[] = []
    const resultPromise = client.sendMessage(
      { conversationId: 'conversation-welcome', text: 'Запиши заметку в Documents' },
      (event) => {
        events.push(event.type)
        if (event.type === 'approval.required') void client.approve(event.approval.id, 'deny')
      },
    )

    const result = await resultPromise

    expect(result.status).toBe('error')
    expect(events).toEqual(expect.arrayContaining(['tool.started', 'approval.required', 'tool.updated', 'run.completed']))
  })

  it('keeps the offline memory and archive preview data-free', async () => {
    const client = createYuriClient()

    expect(await client.listMemories({ lifecycleState: 'active' })).toEqual([])
    expect(await client.searchArchive({ query: 'проект', includeDormant: true })).toEqual({
      results: [],
      total: 0,
      query: 'проект',
    })
  })

  it('normalizes shared-memory ownership and forwards explicit scope changes', async () => {
    const calls: unknown[] = []
    await withWindow({
      go: { main: { Bridge: {
        ListConversations: () => [],
        ListMemories: (input: unknown) => {
          calls.push(['list', input])
          return [{
            id: 'memory-1', agentId: 'agent-yuri', agentName: 'Yuri', scope: 'owner_shared',
            kind: 'episodic', nature: 'fiction', content: 'Владелец любит сенчу', confidence: .9,
            salience: .8, lifecycle: 'active', createdAt: '2026-08-30T00:00:00Z', updatedAt: '2026-08-30T00:00:00Z',
            fiction: { provenance: 'owner_seed', recallState: 'remembered', epistemicStatus: 'fictional', ownerAuthored: true, episodeId: 'tea' },
            history: [{ version: 1, operation: 'create', reason: 'owner seed', createdAt: '2026-08-30T00:00:00Z' }],
          }]
        },
        SetMemoryScope: (input: unknown) => {
          calls.push(['scope', input])
          return { id: 'memory-1', scope: 'agent_private', kind: 'semantic', nature: 'fact', content: 'Владелец любит сенчу', lifecycle: 'active' }
        },
        UpdateBackstoryMemory: (input: unknown) => {
          calls.push(['backstory-update', input])
          return { id: 'memory-1', scope: 'agent_private', kind: 'episodic', nature: 'fiction', content: 'Новая версия', lifecycle: 'active' }
        },
        DisableBackstoryMemory: (input: unknown) => {
          calls.push(['backstory-disable', input])
          return { id: 'memory-1', scope: 'agent_private', kind: 'episodic', nature: 'fiction', content: 'Новая версия', lifecycle: 'deleted' }
        },
        RehydrateBackstoryMemory: (input: unknown) => {
          calls.push(['backstory-rehydrate', input])
          return { id: 'memory-1', scope: 'agent_private', kind: 'episodic', nature: 'fiction', content: 'Новая версия', lifecycle: 'active' }
        },
      } } },
    }, async () => {
      const client = createYuriClient()
      await expect(client.listMemories({ scope: 'owner_shared' })).resolves.toMatchObject([{
        id: 'memory-1', agentId: 'agent-yuri', agentName: 'Yuri', scope: 'owner_shared', contentKind: 'fiction',
        fiction: { provenance: 'owner_seed', recallState: 'remembered', episodeId: 'tea' },
        history: [{ version: 1, operation: 'create' }],
      }])
      await expect(client.setMemoryScope('memory-1', 'agent_private')).resolves.toMatchObject({ scope: 'agent_private' })
      await expect(client.updateBackstoryMemory('memory-1', 'Новая версия')).resolves.toMatchObject({ content: 'Новая версия' })
      await expect(client.disableBackstoryMemory('memory-1')).resolves.toMatchObject({ lifecycleState: 'deleted' })
      await expect(client.rehydrateBackstoryMemory('memory-1')).resolves.toMatchObject({ lifecycleState: 'active' })
      expect(calls).toEqual([
        ['list', expect.objectContaining({ scope: 'owner_shared' })],
        ['scope', { id: 'memory-1', memoryId: 'memory-1', scope: 'agent_private' }],
        ['backstory-update', { id: 'memory-1', memoryId: 'memory-1', content: 'Новая версия' }],
        ['backstory-disable', { id: 'memory-1', memoryId: 'memory-1' }],
        ['backstory-rehydrate', { id: 'memory-1', memoryId: 'memory-1' }],
      ])
    })
  })

  it('exposes the plugin lifecycle contract in the offline preview', async () => {
    const client = createYuriClient()

    expect(await client.listPlugins()).toEqual([])
    await expect(client.inspectPluginPackage('/tmp/reference-plugin.zip')).resolves.toMatchObject({
      path: '/tmp/reference-plugin.zip',
      valid: false,
      compatible: false,
    })
    await expect(client.enablePlugin({ pluginId: 'reference.demo', capabilities: [] })).resolves.toBeUndefined()
    await expect(client.startPlugin('reference.demo')).resolves.toBeUndefined()
    await expect(client.stopPlugin('reference.demo')).resolves.toBeUndefined()
    await expect(client.disablePlugin('reference.demo')).resolves.toBeUndefined()
    await expect(client.uninstallPlugin('reference.demo')).resolves.toBeUndefined()
    // Nothing offline is installable, and the switch starts off rather than
    // reporting a value the owner never chose.
    await expect(client.pluginDevMode()).resolves.toBe(false)
  })

  it('normalizes plugin metadata and forwards lifecycle requests to Wails', async () => {
    const calls: Array<{ name: string; args: unknown[] }> = []
    const plugin = {
      id: 'reference.demo',
      name: 'Reference plugin',
      version: '0.1.0',
      publisher: 'Yuri',
      enabled: false,
      running: false,
      status: 'stopped',
      signature_status: 'signed',
      protocol_version: '1',
      // PluginPermissionDTO splits the scope: `scope` is the kind
      // (domain.ScopeKind) and `values` the values it is narrowed to.
      permissions: [
        { capability: 'network.http', scope: 'network', values: ['example.test'], granted: true },
        { capability: 'filesystem.read', scope: 'filesystem', values: ['/tmp/reference'], granted: false },
      ],
      tools: [{ id: 'demo.echo', name: 'Echo', risk: 'low' }],
    }
    const bridge = {
      ListConversations: () => [],
      ListPlugins: () => [plugin],
      InspectPluginPackage: (request: unknown) => {
        calls.push({ name: 'InspectPluginPackage', args: [request] })
        return {
          path: '/tmp/reference-plugin.zip', valid: true, compatible: true, signatureStatus: 'signed',
          manifest: plugin, warnings: [], errors: [], installable: true, requires_dev_mode: false,
        }
      },
      EnablePlugin: (request: unknown) => {
        calls.push({ name: 'EnablePlugin', args: [request] })
        return { ...plugin, enabled: true, status: 'enabled' }
      },
      UninstallPlugin: (request: unknown) => {
        calls.push({ name: 'UninstallPlugin', args: [request] })
      },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      const client = createYuriClient()
      expect(client.mode).toBe('wails')
      await expect(client.listPlugins()).resolves.toMatchObject([{
        id: 'reference.demo',
        signatureStatus: 'signed',
        permissions: [
          { capability: 'network.http', scope: 'network', scopeValues: ['example.test'], granted: true },
          { capability: 'filesystem.read', scope: 'filesystem', scopeValues: ['/tmp/reference'], granted: false },
        ],
        tools: [{ id: 'demo.echo', name: 'Echo' }],
      }])
      await expect(client.inspectPluginPackage('/tmp/reference-plugin.zip')).resolves.toMatchObject({
        valid: true,
        compatible: true,
        manifest: { id: 'reference.demo' },
        // The backend's verdict, already resolved against plugin dev mode.
        installable: true,
        requiresDevMode: false,
      })
      // A subset: only the capability the owner approved travels, narrowed and
      // time-bounded. Nothing else in the manifest is granted.
      await expect(client.enablePlugin({
        pluginId: 'reference.demo',
        capabilities: [{ capability: 'network.http', scopeKind: 'network', scopeValues: ['api.example.test'], expiresInHours: 24 }],
      })).resolves.toMatchObject({ enabled: true, status: 'enabled' })
      await client.uninstallPlugin('reference.demo')
      expect(calls).toEqual([
        // PluginPathRequest carries the path and nothing else.
        { name: 'InspectPluginPackage', args: [{ path: '/tmp/reference-plugin.zip' }] },
        {
          name: 'EnablePlugin',
          args: [{
            id: 'reference.demo',
            pluginId: 'reference.demo',
            capabilities: [{ capability: 'network.http', scopeKind: 'network', scopeValues: ['api.example.test'], expiresInHours: 24 }],
          }],
        },
        { name: 'UninstallPlugin', args: [{ id: 'reference.demo', pluginId: 'reference.demo' }] },
      ])
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('reads and writes plugin dev mode as persisted backend state', async () => {
    const calls: Array<{ name: string; args: unknown[] }> = []
    let devMode = true
    const bridge = {
      ListConversations: () => [],
      // Bridge.PluginDevMode() bool — the persisted config.plugin_dev_mode.
      PluginDevMode: () => {
        calls.push({ name: 'PluginDevMode', args: [] })
        return devMode
      },
      // Bridge.SetPluginDevMode(enabled bool) error — a scalar argument, not
      // a request object.
      SetPluginDevMode: (enabled: unknown) => {
        calls.push({ name: 'SetPluginDevMode', args: [enabled] })
        devMode = Boolean(enabled)
      },
      InspectPluginPackage: (request: unknown) => {
        calls.push({ name: 'InspectPluginPackage', args: [request] })
        return {
          path: '/tmp/unsigned-plugin', valid: true, compatible: true, signature_status: 'unsigned',
          warnings: [], errors: [], installable: devMode, requires_dev_mode: true,
        }
      },
      InstallPlugin: (request: unknown) => {
        calls.push({ name: 'InstallPlugin', args: [request] })
        return undefined
      },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      const client = createYuriClient()
      await expect(client.pluginDevMode()).resolves.toBe(true)
      await client.setPluginDevMode(false)
      await expect(client.pluginDevMode()).resolves.toBe(false)

      // With the switch off the backend refuses the unsigned package, and the
      // signature status is reported as it is rather than relabelled 'dev'.
      await expect(client.inspectPluginPackage('/tmp/unsigned-plugin')).resolves.toMatchObject({
        signatureStatus: 'unsigned',
        installable: false,
        requiresDevMode: true,
      })

      await client.setPluginDevMode(true)
      await expect(client.inspectPluginPackage('/tmp/unsigned-plugin')).resolves.toMatchObject({
        signatureStatus: 'unsigned',
        installable: true,
        requiresDevMode: true,
      })
      await client.installPlugin({ path: '/tmp/unsigned-plugin' })

      expect(calls).toEqual([
        { name: 'PluginDevMode', args: [] },
        { name: 'SetPluginDevMode', args: [false] },
        { name: 'PluginDevMode', args: [] },
        // PluginPathRequest is `{Path string}`: no devMode, no allowUnsigned.
        { name: 'InspectPluginPackage', args: [{ path: '/tmp/unsigned-plugin' }] },
        { name: 'SetPluginDevMode', args: [true] },
        { name: 'InspectPluginPackage', args: [{ path: '/tmp/unsigned-plugin' }] },
        { name: 'InstallPlugin', args: [{ path: '/tmp/unsigned-plugin' }] },
      ])
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('treats an inspection with no verdict as not installable', async () => {
    const bridge = {
      ListConversations: () => [],
      // A payload the current bridge cannot produce. If it ever appears, the
      // missing verdict must fail closed: absent is not permission.
      InspectPluginPackage: () => ({ path: '/tmp/legacy', valid: true, compatible: true, signature_status: 'signed', warnings: [], errors: [] }),
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      await expect(createYuriClient().inspectPluginPackage('/tmp/legacy')).resolves.toMatchObject({
        installable: false,
        requiresDevMode: false,
      })
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('refuses to send an unrestricted consent that was never confirmed', async () => {
    const calls: unknown[] = []
    const bridge = {
      ListConversations: () => [],
      EnablePlugin: (request: unknown) => {
        calls.push(request)
        return undefined
      },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      const client = createYuriClient()

      // Scope kind `unrestricted` without AllowUnrestricted: rejected by
      // pluginConsentGrants, and never worth attempting.
      await expect(client.enablePlugin({
        pluginId: 'reference.demo',
        capabilities: [{ capability: 'filesystem.read', scopeKind: 'unrestricted' }],
      })).rejects.toThrow(/подтверждения/)

      // N-8: a bare "*" value is unbounded too, even though its kind looks
      // narrow. It passes through the same gate.
      await expect(client.enablePlugin({
        pluginId: 'reference.demo',
        capabilities: [{ capability: 'network.http', scopeKind: 'network', scopeValues: ['*'] }],
      })).rejects.toThrow(/подтверждения/)

      // Neither attempt reached the bridge.
      expect(calls).toEqual([])

      await client.enablePlugin({
        pluginId: 'reference.demo',
        capabilities: [{ capability: 'network.http', scopeKind: 'network', scopeValues: ['*'], allowUnrestricted: true }],
      })
      expect(calls).toEqual([{
        id: 'reference.demo',
        pluginId: 'reference.demo',
        capabilities: [{ capability: 'network.http', scopeKind: 'network', scopeValues: ['*'], allowUnrestricted: true }],
      }])
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('surfaces a backend rejection of a consented scope instead of swallowing it', async () => {
    const bridge = {
      ListConversations: () => [],
      EnablePlugin: () => {
        throw new Error('consented scope for "filesystem.read" is broader than the manifest declaration')
      },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      const client = createYuriClient()
      await expect(client.enablePlugin({
        pluginId: 'reference.demo',
        capabilities: [{ capability: 'filesystem.read', scopeKind: 'filesystem', scopeValues: ['/'] }],
      })).rejects.toThrow('broader than the manifest declaration')
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('forwards encrypted backup operations without persisting the passphrase in client state', async () => {
    const calls: Array<{ name: string; input: unknown }> = []
    const bridge = {
      ListConversations: () => [],
      CreateEncryptedBackup: (input: unknown) => {
        calls.push({ name: 'create', input })
        return { path: '/tmp/yuri.yuribackup', created_at: '2026-08-28T06:00:00Z', size_bytes: 8192, blob_count: 3, has_config: true }
      },
      ValidateEncryptedBackup: (input: unknown) => {
        calls.push({ name: 'validate', input })
        return { path: '/tmp/yuri.yuribackup', createdAt: '2026-08-28T06:00:00Z', sizeBytes: 8192, blobCount: 3, hasConfig: true }
      },
      RestoreEncryptedBackup: (input: unknown) => {
        calls.push({ name: 'restore', input })
        return { path: '/tmp/yuri.yuribackup', createdAt: '2026-08-28T06:00:00Z', sizeBytes: 8192, blobCount: 3, hasConfig: true, restoredTo: '/tmp/restored' }
      },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      const client = createYuriClient()
      const request = { passphrase: 'correct horse battery staple' }
      await expect(client.createEncryptedBackup({ ...request, includeBlobs: true })).resolves.toMatchObject({ sizeBytes: 8192, blobCount: 3, hasConfig: true })
      await expect(client.validateEncryptedBackup(request)).resolves.toMatchObject({ path: '/tmp/yuri.yuribackup' })
      await expect(client.restoreEncryptedBackup(request)).resolves.toMatchObject({ restoredTo: '/tmp/restored' })
      expect(calls).toEqual([
        { name: 'create', input: { passphrase: request.passphrase, includeBlobs: true } },
        { name: 'validate', input: request },
        { name: 'restore', input: request },
      ])
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('exposes durable scheduler and proactivity controls in the offline preview', async () => {
    const client = createYuriClient()
    const schedules = await client.listSchedules()

    expect(schedules[0]).toMatchObject({
      id: 'schedule-daily-briefing',
      type: 'cron',
      expression: '0 9 * * 1-5',
      timezone: 'Europe/Moscow',
      misfirePolicy: 'run_once',
      enabled: true,
    })
    expect((await client.listJobRuns({ scheduleId: schedules[0]?.id })).length).toBeGreaterThan(0)
    expect((await client.listActivity({ type: 'proactive' }))[0]?.type).toBe('proactive')

    const created = await client.createSchedule({
      title: 'Проверка задач',
      prompt: 'Проверь, что scheduler отвечает.',
      type: 'interval',
      intervalSeconds: 3600,
      timezone: 'UTC',
      misfirePolicy: 'skip',
      deliveryChannel: 'in_app',
    })
    expect(created).toMatchObject({ title: 'Проверка задач', type: 'interval', enabled: true })
    expect(await client.setScheduleEnabled(created!.id, false)).toMatchObject({ enabled: false, status: 'paused' })
    await client.deleteSchedule(created!.id)
    expect((await client.listSchedules()).find((item) => item.id === created!.id)).toBeUndefined()

    const settings = await client.getProactivitySettings()
    await client.saveProactivitySettings({
      ...settings,
      enabled: false,
      dailyLimit: 0,
      autonomousPeerDialogues: true,
      autonomousPeerDailyLimit: 3,
      autonomousPeerCooldownMinutes: 90,
    })
    expect(await client.getProactivitySettings()).toMatchObject({
      enabled: false,
      dailyLimit: 0,
      autonomousPeerDialogues: true,
      autonomousPeerDailyLimit: 3,
      autonomousPeerCooldownMinutes: 90,
    })
  })

  it('normalizes scheduler payloads and forwards the Stage 4 Wails API', async () => {
    const calls: Array<{ name: string; args: unknown[] }> = []
    const schedule = {
      schedule_id: 'schedule-1',
      name: 'Сводка',
      instruction: 'Собери сводку.',
      schedule_type: 'cron',
      cron_expression: '0 9 * * 1-5',
      timezone: 'Europe/Moscow',
      misfire_policy: 'run_once',
      enabled: false,
      state: 'paused',
      next_run_at: '2026-08-29T06:00:00.000Z',
      delivery_channel: 'notification',
    }
    const bridge = {
      ListConversations: () => [],
      ListSchedules: () => [schedule],
      CreateSchedule: (input: unknown) => {
        calls.push({ name: 'CreateSchedule', args: [input] })
        return { ...schedule, enabled: true, state: 'active' }
      },
      SetScheduleEnabled: (input: unknown) => {
        calls.push({ name: 'SetScheduleEnabled', args: [input] })
        return { ...schedule, enabled: true, state: 'active' }
      },
      RunScheduleNow: (input: unknown) => {
        calls.push({ name: 'RunScheduleNow', args: [input] })
        return { id: 'run-1', schedule_id: 'schedule-1', state: 'completed', attempt: 1, triggered_by: 'manual' }
      },
      CancelJobRun: (input: unknown) => {
        calls.push({ name: 'CancelJobRun', args: [input] })
        return { id: 'run-1', schedule_id: 'schedule-1', state: 'cancelled', attempt: 1, trigger: 'manual' }
      },
      DeleteSchedule: (input: unknown) => {
        calls.push({ name: 'DeleteSchedule', args: [input] })
      },
      ListJobRuns: (input: unknown) => {
        calls.push({ name: 'ListJobRuns', args: [input] })
        return [{ id: 'run-1', schedule_id: 'schedule-1', state: 'completed', attempt: 1 }]
      },
      GetProactivitySettings: () => ({
        global_enabled: false,
        quiet_hours_enabled: true,
        quiet_hours_start: '22:30',
        quiet_hours_end: '07:30',
        timezone: 'UTC',
        daily_limit: 4,
        cooldown_minutes: 15,
        allow_local_notifications: false,
        autonomous_peer_dialogues: true,
        autonomous_peer_daily_limit: 3,
        autonomous_peer_cooldown_minutes: 90,
      }),
      SaveProactivitySettings: (input: unknown) => {
        calls.push({ name: 'SaveProactivitySettings', args: [input] })
      },
      ListActivity: (input: unknown) => {
        calls.push({ name: 'ListActivity', args: [input] })
        return [{ event_id: 'activity-1', category: 'proactive', state: 'skipped', action: 'Отложено', created_at: '2026-08-28T10:00:00.000Z' }]
      },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      const client = createYuriClient()
      expect(client.mode).toBe('wails')
      await expect(client.listSchedules()).resolves.toMatchObject([{ id: 'schedule-1', title: 'Сводка', type: 'cron', status: 'paused', enabled: false }])
      await expect(client.createSchedule({
        title: 'Новая',
        prompt: 'Сделай.',
        type: 'once',
        runAt: '2026-08-29T09:00:00.000Z',
        timezone: 'UTC',
        misfirePolicy: 'skip',
      })).resolves.toMatchObject({ id: 'schedule-1', enabled: true })
      await expect(client.setScheduleEnabled('schedule-1', true)).resolves.toMatchObject({ enabled: true })
      await expect(client.runScheduleNow('schedule-1')).resolves.toMatchObject({ scheduleId: 'schedule-1', status: 'completed', triggeredBy: 'manual' })
      await expect(client.cancelJobRun('run-1')).resolves.toMatchObject({ id: 'run-1', status: 'cancelled' })
      await expect(client.listJobRuns({ scheduleId: 'schedule-1', limit: 5 })).resolves.toMatchObject([{ scheduleId: 'schedule-1', status: 'completed' }])
      await expect(client.getProactivitySettings()).resolves.toMatchObject({
        enabled: false,
        quietHoursStart: '22:30',
        dailyLimit: 4,
        autonomousPeerDialogues: true,
        autonomousPeerDailyLimit: 3,
        autonomousPeerCooldownMinutes: 90,
      })
      await client.saveProactivitySettings({ ...(await client.getProactivitySettings()), enabled: true })
      await expect(client.listActivity({ limit: 10 })).resolves.toMatchObject([{ id: 'activity-1', type: 'proactive', status: 'skipped' }])
      await client.deleteSchedule('schedule-1')
      expect(calls.map((call) => call.name)).toEqual([
        'CreateSchedule',
        'SetScheduleEnabled',
        'RunScheduleNow',
        'CancelJobRun',
        'ListJobRuns',
        'SaveProactivitySettings',
        'ListActivity',
        'DeleteSchedule',
      ])
      expect(calls[0]?.args[0]).toMatchObject({ title: 'Новая', type: 'once', scheduleType: 'once', cron_expression: undefined })
      expect(calls[1]?.args[0]).toEqual({ id: 'schedule-1', scheduleId: 'schedule-1', enabled: true })
      expect(calls[3]?.args[0]).toEqual({ id: 'run-1', runId: 'run-1' })
      expect(calls[5]?.args[0]).toMatchObject({
        autonomousPeerDialogues: true,
        autonomous_peer_dialogues: true,
        autonomousPeerDailyLimit: 3,
        autonomous_peer_daily_limit: 3,
        autonomousPeerCooldownMinutes: 90,
        autonomous_peer_cooldown_minutes: 90,
      })
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('subscribes to notification events and gates native delivery on explicit flags', async () => {
    const bus = createWailsEventBus()
    const bridge = { ListConversations: () => [] }
    const previousWindow = (globalThis as { window?: unknown }).window
    const previousNotification = (globalThis as { Notification?: unknown }).Notification
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: {
        go: { main: { Bridge: bridge } },
        runtime: bus.runtime,
      },
    })
    resetYuriClientForTests()

    try {
      const received: string[] = []
      const unsubscribe = subscribeNotifications((notification) => received.push(notification.id))
      bus.emit('yuri:notification', {
        data: {
          notification: {
            notification_id: 'notification-1',
            type: 'task.completed',
            title: 'Задача завершена',
            body: 'Утренняя сводка готова.',
            allow_native: 'true',
            created_at: '2026-08-28T10:00:00.000Z',
          },
        },
      })
      expect(received).toEqual(['notification-1'])
      unsubscribe()
      expect(bus.listenerCount('yuri:notification')).toBe(0)

      const fakeNotification = { permission: 'granted' as NotificationPermission, requestPermission: async () => 'granted' as NotificationPermission }
      Object.defineProperty(globalThis, 'Notification', { configurable: true, value: fakeNotification })
      const nativeAllowed = {
        id: 'notification-2',
        type: 'agent.message' as const,
        title: 'Yuri',
        body: 'Я здесь.',
        createdAt: '2026-08-28T10:00:00.000Z',
        allowNative: true,
      }
      expect(canUseNativeNotification(nativeAllowed)).toBe(true)
      expect(canUseNativeNotification({ ...nativeAllowed, allowNative: false })).toBe(false)
      expect(canUseNativeNotification({ ...nativeAllowed, permission: 'denied' })).toBe(false)
      fakeNotification.permission = 'default'
      expect(canUseNativeNotification(nativeAllowed)).toBe(false)
      let requestCount = 0
      fakeNotification.requestPermission = async () => {
        requestCount += 1
        return 'granted'
      }
      expect(await requestBrowserNotificationPermission()).toBe('granted')
      expect(requestCount).toBe(1)
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      if (previousNotification === undefined) delete (globalThis as { Notification?: unknown }).Notification
      else Object.defineProperty(globalThis, 'Notification', { configurable: true, value: previousNotification })
      resetYuriClientForTests()
    }
  })

  it('propagates rejected Stage 4 read calls instead of replacing them with fallback data', async () => {
    const bridge = {
      ListConversations: () => [],
      ListSchedules: () => Promise.reject(new Error('schedule storage unavailable')),
      ListJobRuns: () => Promise.reject(new Error('job history unavailable')),
      CancelJobRun: () => Promise.reject(new Error('cancel unavailable')),
      GetProactivitySettings: () => Promise.reject(new Error('proactivity settings unavailable')),
      ListActivity: () => Promise.reject(new Error('activity unavailable')),
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      const client = createYuriClient()
      await expect(client.listSchedules()).rejects.toThrow('schedule storage unavailable')
      await expect(client.listJobRuns({ limit: 5 })).rejects.toThrow('job history unavailable')
      await expect(client.cancelJobRun('run-1')).rejects.toThrow('cancel unavailable')
      await expect(client.getProactivitySettings()).rejects.toThrow('proactivity settings unavailable')
      await expect(client.listActivity({ limit: 5 })).rejects.toThrow('activity unavailable')
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('uses Stage 4 fallback data only when the Wails methods are absent', async () => {
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: { ListConversations: () => [] } } } } })
    resetYuriClientForTests()

    try {
      const client = createYuriClient()
      await expect(client.listSchedules()).resolves.toEqual([])
      await expect(client.listJobRuns()).resolves.toEqual([])
      await expect(client.cancelJobRun('run-1')).resolves.toBeUndefined()
      await expect(client.listActivity()).resolves.toEqual([])
      await expect(client.getProactivitySettings()).resolves.toMatchObject({
        enabled: false,
        quietHoursStart: '23:00',
        dailyLimit: 5,
      })
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('exposes bounded peer dialogues in the offline preview and can cancel a live one', async () => {
    const client = createYuriClient()
    const dialogues = await client.listPeerDialogues()

    expect(dialogues).toHaveLength(2)
    expect(dialogues[0]).toMatchObject({
      id: 'peer-dialogue-briefing',
      initiatorName: 'Юри',
      peerName: 'Мира',
      status: 'completed',
      maxTurns: 1,
      maxTokens: 1200,
    })
    expect(dialogues[0]?.messages[1]).toMatchObject({ senderName: 'Мира', recipientName: 'Юри' })

    await client.cancelPeerDialogue('peer-dialogue-research')
    await expect(client.listPeerDialogues()).resolves.toMatchObject([{ id: 'peer-dialogue-briefing' }, { id: 'peer-dialogue-research', status: 'cancelled' }])
  })

  it('keeps offline peer relationship recovery append-only', async () => {
    const client = createYuriClient()
    const [relationship] = await client.listPeerRelationships()
    expect(relationship).toMatchObject({ peerAgentId: 'agent-mira', version: 2, opinions: [{ label: 'opinion' }] })

    const reset = await client.resetPeerRelationship('agent-mira')
    expect(reset?.relationship).toMatchObject({ version: 3, opinions: [] })
    expect(reset?.versions[0]).toMatchObject({ version: 3, operation: 'reset' })

    const restored = await client.rollbackPeerRelationship('agent-mira', 'peer-relationship-version-2')
    expect(restored?.relationship).toMatchObject({ version: 4, opinions: [{ label: 'opinion' }] })
    expect(restored?.versions[0]).toMatchObject({ version: 4, operation: 'rollback' })
  })

  it('normalizes and forwards bounded peer dialogue methods through Wails', async () => {
    const calls: Array<{ name: string; args: unknown[] }> = []
    const bridge = {
      ListConversations: () => [],
      ListPeerDialogues: (input: unknown) => {
        calls.push({ name: 'ListPeerDialogues', args: [input] })
        return {
          items: [{
            dialogue_id: 'peer-1',
            initiator_agent_id: 'agent-yuri', initiator_name: 'Юри',
            peer_agent_id: 'agent-mira', peer_name: 'Мира',
            trigger_kind: 'autonomous', trigger_reason: 'Нужна независимая проверка плана.',
            purpose: 'Сверить план.', state: 'running',
            turn_count: 0, max_turns: 1, tokens_used: 0, max_tokens: 1200,
            created_at: '2026-08-29T09:00:00.000Z',
            messages: [{
              id: 'message-1', sequence: 0, sender_agent_id: 'agent-yuri', sender_name: 'Юри',
              recipient_agent_id: 'agent-mira', recipient_name: 'Мира', content: 'Готова?', created_at: '2026-08-29T09:00:00.000Z',
            }],
          }],
        }
      },
      CancelPeerDialogue: (input: unknown) => { calls.push({ name: 'CancelPeerDialogue', args: [input] }) },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      const client = createYuriClient()
      await expect(client.listPeerDialogues({ limit: 17 })).resolves.toEqual([{
        id: 'peer-1',
        initiatorAgentId: 'agent-yuri', initiatorName: 'Юри',
        peerAgentId: 'agent-mira', peerName: 'Мира', purpose: 'Сверить план.', status: 'running',
        triggerKind: 'autonomous', triggerReason: 'Нужна независимая проверка плана.',
        turnCount: 0, maxTurns: 1, tokensUsed: 0, maxTokens: 1200,
        createdAt: '2026-08-29T09:00:00.000Z', finishedAt: undefined, failure: undefined,
        messages: [{
          id: 'message-1', sequence: 0, senderAgentId: 'agent-yuri', senderName: 'Юри',
          recipientAgentId: 'agent-mira', recipientName: 'Мира', content: 'Готова?', createdAt: '2026-08-29T09:00:00.000Z',
        }],
      }])
      await client.cancelPeerDialogue('peer-1')
      expect(calls).toEqual([
        { name: 'ListPeerDialogues', args: [{ limit: 17 }] },
        { name: 'CancelPeerDialogue', args: [{ id: 'peer-1' }] },
      ])
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('scopes peer relationship history and owner recovery calls through Wails', async () => {
    const calls: Array<{ name: string; args: unknown[] }> = []
    const relationship = {
      observer_agent_id: 'agent-yuri', peer_agent_id: 'agent-mira', peer_name: 'Мира', relationship_id: 'rel-1',
      version: 2, current_version_id: 'rel-version-2', summary: 'Надёжная собеседница.',
      dimensions: { trust: 72, warmth: 0.6 },
      opinions: [{ id: 'opinion-1', subject: 'Мира', content: 'Хорошо проверяет планы.', label: 'opinion', confidence: 68 }],
      evidence: [], updated_at: '2026-08-30T10:00:00.000Z',
    }
    const detail = {
      relationship,
      versions: [{
        id: 'rel-version-2', version: 2, parent_id: 'rel-version-1', operation: 'update', summary: 'Надёжная собеседница.',
        dimensions: { trust: 0.72 }, opinions: relationship.opinions, reason: 'Рефлексия.', evidence: [], created_at: '2026-08-30T10:00:00.000Z',
      }],
    }
    const bridge = {
      ListConversations: () => [],
      ListPeerRelationships: (input: unknown) => { calls.push({ name: 'ListPeerRelationships', args: [input] }); return { relationships: [relationship] } },
      GetPeerRelationship: (input: unknown) => { calls.push({ name: 'GetPeerRelationship', args: [input] }); return detail },
      RollbackPeerRelationship: (input: unknown) => { calls.push({ name: 'RollbackPeerRelationship', args: [input] }); return detail },
      ResetPeerRelationship: (input: unknown) => { calls.push({ name: 'ResetPeerRelationship', args: [input] }); return detail },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()

    try {
      const client = createYuriClient()
      await expect(client.listPeerRelationships({ limit: 9 })).resolves.toMatchObject([{
        observerAgentId: 'agent-yuri', peerAgentId: 'agent-mira', relationshipId: 'rel-1',
        dimensions: { trust: 0.72, warmth: 0.6 }, opinions: [{ label: 'opinion', confidence: 0.68 }],
      }])
      await expect(client.getPeerRelationship('agent-mira')).resolves.toMatchObject({ versions: [{ operation: 'update' }] })
      await client.rollbackPeerRelationship('agent-mira', 'rel-version-1')
      await client.resetPeerRelationship('agent-mira')
      expect(calls).toEqual([
        { name: 'ListPeerRelationships', args: [{ limit: 9 }] },
        { name: 'GetPeerRelationship', args: [{ peerAgentId: 'agent-mira' }] },
        { name: 'RollbackPeerRelationship', args: [{ peerAgentId: 'agent-mira', versionId: 'rel-version-1' }] },
        { name: 'ResetPeerRelationship', args: [{ peerAgentId: 'agent-mira' }] },
      ])
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })
})
