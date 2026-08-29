import { beforeEach, describe, expect, it } from 'vitest'

import {
  canUseNativeNotification,
  createYuriClient,
  requestBrowserNotificationPermission,
  resetYuriClientForTests,
  subscribeNotifications,
} from './client'
import { defaultAgentDraft } from './agents'

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
        apiKeyConfigured: false,
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

  it('normalizes and forwards named-agent roster methods through Wails', async () => {
    const calls: Array<{ name: string; args: unknown[] }> = []
    const wireAgent = {
      id: 'agent-yuri', name: 'Юри', age: 21, gender: 'female', preferences: 'Коротко',
      traits: { warmth: 0.6 }, active: true, created_at: '2026-08-29T00:00:00Z', updated_at: '2026-08-29T00:00:00Z',
    }
    const bridge = {
      ListConversations: () => [],
      ListAgents: () => { calls.push({ name: 'ListAgents', args: [] }); return [wireAgent] },
      GetActiveAgent: () => { calls.push({ name: 'GetActiveAgent', args: [] }); return wireAgent },
      CreateAgent: (input: unknown) => { calls.push({ name: 'CreateAgent', args: [input] }); return wireAgent },
      SetActiveAgent: (input: unknown) => { calls.push({ name: 'SetActiveAgent', args: [input] }); return wireAgent },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { go: { main: { Bridge: bridge } } } })
    resetYuriClientForTests()
    try {
      const client = createYuriClient()
      await expect(client.listAgents()).resolves.toMatchObject([{ id: 'agent-yuri', name: 'Юри', traits: { warmth: 0.6 }, active: true }])
      await expect(client.getActiveAgent()).resolves.toMatchObject({ id: 'agent-yuri' })
      await expect(client.createAgent(defaultAgentDraft)).resolves.toMatchObject({ id: 'agent-yuri' })
      await expect(client.setActiveAgent('agent-yuri')).resolves.toMatchObject({ id: 'agent-yuri', active: true })
      expect(calls).toEqual([
        { name: 'ListAgents', args: [] },
        { name: 'GetActiveAgent', args: [] },
        { name: 'CreateAgent', args: [defaultAgentDraft] },
        { name: 'SetActiveAgent', args: [{ id: 'agent-yuri' }] },
      ])
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
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

  it('preserves repeated live streaming deltas while suppressing returned replay events', async () => {
    const listeners = new Map<string, (value: unknown) => void>()
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
        streamed.forEach((event) => listeners.get('yuri:chat')?.(event))
        return { runId: 'run-1', status: 'complete', events: streamed }
      },
    }
    const previousWindow = (globalThis as { window?: unknown }).window
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: {
        go: { main: { Bridge: bridge } },
        runtime: {
          EventsOn: (name: string, callback: (value: unknown) => void) => { listeners.set(name, callback) },
          EventsOff: (name: string) => { listeners.delete(name) },
        },
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

  it('exposes the plugin lifecycle contract in the offline preview', async () => {
    const client = createYuriClient()

    expect(await client.listPlugins()).toEqual([])
    await expect(client.inspectPluginPackage('/tmp/reference-plugin.zip')).resolves.toMatchObject({
      path: '/tmp/reference-plugin.zip',
      valid: false,
      compatible: false,
    })
    await expect(client.enablePlugin('reference.demo')).resolves.toBeUndefined()
    await expect(client.startPlugin('reference.demo')).resolves.toBeUndefined()
    await expect(client.stopPlugin('reference.demo')).resolves.toBeUndefined()
    await expect(client.disablePlugin('reference.demo')).resolves.toBeUndefined()
    await expect(client.uninstallPlugin('reference.demo')).resolves.toBeUndefined()
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
      permissions: [{ capability: 'network.http', scope: 'example.test', granted: true }],
      tools: [{ id: 'demo.echo', name: 'Echo', risk: 'low' }],
    }
    const bridge = {
      ListConversations: () => [],
      ListPlugins: () => [plugin],
      InspectPluginPackage: (request: unknown) => {
        calls.push({ name: 'InspectPluginPackage', args: [request] })
        return { path: '/tmp/reference-plugin.zip', valid: true, compatible: true, signatureStatus: 'signed', manifest: plugin, warnings: [], errors: [] }
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
        permissions: [{ capability: 'network.http', granted: true }],
        tools: [{ id: 'demo.echo', name: 'Echo' }],
      }])
      await expect(client.inspectPluginPackage('/tmp/reference-plugin.zip')).resolves.toMatchObject({ valid: true, compatible: true, manifest: { id: 'reference.demo' } })
      await expect(client.enablePlugin('reference.demo')).resolves.toMatchObject({ enabled: true, status: 'enabled' })
      await client.uninstallPlugin('reference.demo')
      expect(calls).toEqual([
        { name: 'InspectPluginPackage', args: [{ path: '/tmp/reference-plugin.zip', devMode: false, allowUnsigned: false }] },
        { name: 'EnablePlugin', args: [{ id: 'reference.demo', pluginId: 'reference.demo' }] },
        { name: 'UninstallPlugin', args: [{ id: 'reference.demo', pluginId: 'reference.demo' }] },
      ])
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
    await client.saveProactivitySettings({ ...settings, enabled: false, dailyLimit: 0 })
    expect(await client.getProactivitySettings()).toMatchObject({ enabled: false, dailyLimit: 0 })
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
      await expect(client.getProactivitySettings()).resolves.toMatchObject({ enabled: false, quietHoursStart: '22:30', dailyLimit: 4 })
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
    } finally {
      if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window
      else Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
      resetYuriClientForTests()
    }
  })

  it('subscribes to notification events and gates native delivery on explicit flags', async () => {
    const listeners = new Map<string, (value: unknown) => void>()
    const bridge = { ListConversations: () => [] }
    const previousWindow = (globalThis as { window?: unknown }).window
    const previousNotification = (globalThis as { Notification?: unknown }).Notification
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: {
        go: { main: { Bridge: bridge } },
        runtime: {
          EventsOn: (name: string, callback: (value: unknown) => void) => { listeners.set(name, callback) },
          EventsOff: (name: string) => { listeners.delete(name) },
        },
      },
    })
    resetYuriClientForTests()

    try {
      const received: string[] = []
      const unsubscribe = subscribeNotifications((notification) => received.push(notification.id))
      listeners.get('yuri:notification')?.({
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
      expect(listeners.has('yuri:notification')).toBe(false)

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
})
