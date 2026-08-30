// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { PluginEnableRequest, PluginPackageInspection, PluginRecord, YuriClient } from '../lib/contracts'
import { PluginView } from './PluginView'

let clientStub: YuriClient

vi.mock('../lib/client', () => ({
  createYuriClient: () => clientStub,
}))

function pluginFixture(): PluginRecord {
  return {
    id: 'reference.demo',
    name: 'Reference plugin',
    version: '0.1.0',
    enabled: false,
    running: false,
    status: 'installed',
    signatureStatus: 'signed',
    eventSources: [],
    tools: [],
    permissions: [
      { capability: 'network.http', scope: 'network', scopeValues: ['*.example.test'], granted: false },
      { capability: 'filesystem.read', scope: 'filesystem', scopeValues: ['/tmp/reference'], granted: false },
    ],
  }
}

/**
 * An unsigned package as desktop.PluginPackageInspection describes it:
 * `requires_dev_mode` is true, and `installable` is the backend's own verdict
 * (`compatible && (!requiresDevMode || pluginDevMode())`), not something the
 * view is expected to derive.
 */
function inspectionFixture(overrides: Partial<PluginPackageInspection> = {}): PluginPackageInspection {
  return {
    path: '/tmp/reference-plugin',
    valid: true,
    compatible: true,
    manifest: pluginFixture(),
    signatureStatus: 'unsigned',
    warnings: [],
    errors: [],
    installable: false,
    requiresDevMode: true,
    ...overrides,
  }
}

function harness(overrides: Partial<YuriClient> = {}) {
  clientStub = {
    mode: 'mock',
    listPlugins: async () => [pluginFixture()],
    // Dev mode is backend state; a stub that omitted these would be testing a
    // view that cannot exist.
    pluginDevMode: async () => false,
    setPluginDevMode: async () => undefined,
    enablePlugin: async () => undefined,
    ...overrides,
  } as unknown as YuriClient
}

async function reviewPackage(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByRole('textbox', { name: 'Путь к пакету' }), '/tmp/reference-plugin')
  await user.click(screen.getByRole('button', { name: 'Проверить пакет' }))
}

function devModeToggle(): HTMLElement {
  return screen.getByRole('checkbox', { name: /Разрешить dev mode/ })
}

describe('PluginView enable flow', () => {
  beforeEach(() => {
    harness()
  })

  it('asks for consent instead of enabling straight from the card', async () => {
    const user = userEvent.setup()
    const enablePlugin = vi.fn(async () => ({ ...pluginFixture(), enabled: true, status: 'enabled' as const }))
    harness({ enablePlugin })
    render(<PluginView />)

    await user.click(await screen.findByRole('button', { name: 'Включить' }))

    // No bridge call has happened yet: the manifest declaration is only a
    // request until the owner answers this dialog.
    expect(enablePlugin).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toHaveAccessibleName('Включить «Reference plugin»')

    await user.click(screen.getByRole('checkbox', { name: /network\.http/ }))
    await user.click(screen.getByRole('button', { name: /Включить с выбранными доступами/ }))

    await waitFor(() => expect(enablePlugin).toHaveBeenCalledTimes(1))
    expect(enablePlugin).toHaveBeenCalledWith<[PluginEnableRequest]>({
      pluginId: 'reference.demo',
      capabilities: [{ capability: 'network.http' }],
    })
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.getByRole('status')).toHaveTextContent('Выдано доступов: 1')
  })

  it('keeps the dialog open and shows the backend rejection verbatim', async () => {
    const user = userEvent.setup()
    const enablePlugin = vi.fn(async () => {
      throw new Error('consented scope for "filesystem.read" is broader than the manifest declaration')
    })
    harness({ enablePlugin })
    render(<PluginView />)

    await user.click(await screen.findByRole('button', { name: 'Включить' }))
    await user.click(screen.getByRole('checkbox', { name: /filesystem\.read/ }))
    await user.click(screen.getByRole('button', { name: /Включить с выбранными доступами/ }))

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('broader than the manifest declaration'))
    // The owner's answers survive the rejection, so the scope can be corrected.
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: /filesystem\.read/ })).toBeChecked()
  })
})

describe('PluginView dev mode', () => {
  it('reads the switch from the backend instead of assuming it is off', async () => {
    // The setting lives in persisted config. A view that started from `false`
    // would tell the owner dev mode is off while unsigned plugins are in fact
    // installable and startable.
    const pluginDevMode = vi.fn(async () => true)
    harness({ pluginDevMode })
    render(<PluginView />)

    await waitFor(() => expect(devModeToggle()).toBeChecked())
    expect(pluginDevMode).toHaveBeenCalledTimes(1)
  })

  it('persists the decision and re-checks the reviewed package against it', async () => {
    const user = userEvent.setup()
    let enabled = false
    const setPluginDevMode = vi.fn(async (next: boolean) => { enabled = next })
    // `installable` follows the switch exactly as the bridge computes it.
    const inspectPluginPackage = vi.fn(async (path: string) => inspectionFixture({ path, installable: enabled }))
    harness({ pluginDevMode: async () => enabled, setPluginDevMode, inspectPluginPackage })
    render(<PluginView />)

    await reviewPackage(user)

    // Dev mode off: the backend refused the package, so the view offers no
    // install button that the backend would reject.
    await waitFor(() => expect(screen.getByRole('button', { name: /Установить плагин/ })).toBeDisabled())
    expect(screen.getByText(/нужны исправления/)).toBeInTheDocument()
    expect(screen.getByText(/Установить и запустить его можно только после включения dev mode/)).toBeInTheDocument()

    await waitFor(() => expect(devModeToggle()).toBeEnabled())
    await user.click(devModeToggle())

    // The decision is written to the backend, not kept in the component.
    await waitFor(() => expect(setPluginDevMode).toHaveBeenCalledWith(true))
    // And the package is re-inspected, because its verdict was computed
    // against the previous value.
    await waitFor(() => expect(inspectPluginPackage).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.getByRole('button', { name: /Установить плагин/ })).toBeEnabled())
    expect(screen.getByText(/Установка разрешена только потому, что включён dev mode/)).toBeInTheDocument()
  })

  it('sends nothing but the path to inspect and install', async () => {
    const user = userEvent.setup()
    // desktop.PluginPathRequest is `{Path string}`. There is no devMode and no
    // allowUnsigned field to waive signature verification per call.
    const inspectPluginPackage = vi.fn(async (path: string) => inspectionFixture({ path, installable: true }))
    const installPlugin = vi.fn(async () => ({ ...pluginFixture(), status: 'installed' as const }))
    harness({ pluginDevMode: async () => true, inspectPluginPackage, installPlugin })
    render(<PluginView />)

    await reviewPackage(user)
    await waitFor(() => expect(inspectPluginPackage).toHaveBeenCalledWith('/tmp/reference-plugin'))
    expect(inspectPluginPackage.mock.calls[0]).toEqual(['/tmp/reference-plugin'])

    await user.click(await screen.findByRole('button', { name: /Установить плагин/ }))
    await waitFor(() => expect(installPlugin).toHaveBeenCalledWith({ path: '/tmp/reference-plugin' }))
  })

  it('offers install strictly on the backend verdict, not on the local flags', async () => {
    const user = userEvent.setup()
    // Everything a locally recomputed guard would look at says yes: valid,
    // compatible, signed, dev mode on. Only `installable` says no, and that is
    // the field InstallPlugin itself enforces.
    const inspectPluginPackage = vi.fn(async (path: string) => inspectionFixture({
      path,
      signatureStatus: 'signed',
      requiresDevMode: false,
      installable: false,
    }))
    const installPlugin = vi.fn(async () => undefined)
    harness({ pluginDevMode: async () => true, inspectPluginPackage, installPlugin })
    render(<PluginView />)

    await reviewPackage(user)

    await waitFor(() => expect(screen.getByRole('button', { name: /Установить плагин/ })).toBeDisabled())
    expect(installPlugin).not.toHaveBeenCalled()
  })

  it('keeps the previous state when the backend refuses to persist the switch', async () => {
    const user = userEvent.setup()
    const setPluginDevMode = vi.fn(async () => { throw new Error('config is read-only') })
    harness({ setPluginDevMode })
    render(<PluginView />)

    await waitFor(() => expect(devModeToggle()).toBeEnabled())
    await user.click(devModeToggle())

    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('config is read-only'))
    // The switch reports the backend, so a write that failed leaves it off.
    expect(devModeToggle()).not.toBeChecked()
  })
})
