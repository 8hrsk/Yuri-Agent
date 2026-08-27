import { beforeEach, describe, expect, it } from 'vitest'

import { createYuriClient, resetYuriClientForTests } from './client'

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
})
