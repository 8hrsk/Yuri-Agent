// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import type { PluginRecord } from '../lib/contracts'
import { PluginConsentDialog } from './PluginConsentDialog'

const plugin: PluginRecord = {
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
    { capability: 'system.exec', scope: 'unrestricted', granted: false },
  ],
}

function approve(capability: string) {
  return screen.getByRole('checkbox', { name: new RegExp(capability.replace('.', '\\.')) })
}

describe('PluginConsentDialog', () => {
  it('presents every declared capability as a request, granting none by default', () => {
    render(<PluginConsentDialog busy={false} onCancel={vi.fn()} onConfirm={vi.fn()} plugin={plugin} />)

    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(dialog).toHaveAccessibleName('Включить «Reference plugin»')

    for (const permission of plugin.permissions) {
      expect(approve(permission.capability)).not.toBeChecked()
    }
    expect(screen.getByText('Запрошено: network: *.example.test')).toBeInTheDocument()
    expect(screen.getByText('Запрошено: без ограничений')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /0\/3/ })).toBeInTheDocument()
  })

  it('sends exactly the approved subset', async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    render(<PluginConsentDialog busy={false} onCancel={vi.fn()} onConfirm={onConfirm} plugin={plugin} />)

    await user.click(approve('network.http'))
    await user.click(screen.getByRole('button', { name: /Включить с выбранными доступами/ }))

    expect(onConfirm).toHaveBeenCalledWith({
      pluginId: 'reference.demo',
      capabilities: [{ capability: 'network.http' }],
    })
  })

  it('enables with no grants at all when nothing is approved', async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    render(<PluginConsentDialog busy={false} onCancel={vi.fn()} onConfirm={onConfirm} plugin={plugin} />)

    await user.click(screen.getByRole('button', { name: /Включить с выбранными доступами/ }))

    expect(onConfirm).toHaveBeenCalledWith({ pluginId: 'reference.demo', capabilities: [] })
  })

  it('narrows a declared scope into scopeKind and scopeValues', async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    render(<PluginConsentDialog busy={false} onCancel={vi.fn()} onConfirm={onConfirm} plugin={plugin} />)

    await user.click(approve('network.http'))
    await user.selectOptions(screen.getByLabelText('Вид scope для network.http'), 'network')
    await user.type(screen.getByLabelText('Значения scope для network.http'), 'api.example.test')
    await user.type(screen.getByLabelText('Срок действия в часах для network.http'), '48')
    await user.click(screen.getByRole('button', { name: /Включить с выбранными доступами/ }))

    expect(onConfirm).toHaveBeenCalledWith({
      pluginId: 'reference.demo',
      capabilities: [{
        capability: 'network.http',
        scopeKind: 'network',
        scopeValues: ['api.example.test'],
        expiresInHours: 48,
      }],
    })
  })

  it('makes a declared-unrestricted grant a separate decision', async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    render(<PluginConsentDialog busy={false} onCancel={vi.fn()} onConfirm={onConfirm} plugin={plugin} />)

    await user.click(approve('system.exec'))
    const confirmation = screen.getByRole('checkbox', { name: 'Подтверждаю неограниченный доступ для system.exec' })
    expect(confirmation).not.toBeChecked()

    await user.click(screen.getByRole('button', { name: /Включить с выбранными доступами/ }))
    expect(onConfirm).not.toHaveBeenCalled()
    expect(screen.getByRole('alert')).toHaveTextContent('ничем не ограничен')

    await user.click(confirmation)
    await user.click(screen.getByRole('button', { name: /Включить с выбранными доступами/ }))
    expect(onConfirm).toHaveBeenCalledWith({
      pluginId: 'reference.demo',
      capabilities: [{ capability: 'system.exec', allowUnrestricted: true }],
    })
  })

  it('treats a bare "*" scope value exactly like kind unrestricted (N-8)', async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    render(<PluginConsentDialog busy={false} onCancel={vi.fn()} onConfirm={onConfirm} plugin={plugin} />)

    await user.click(approve('network.http'))
    // A bounded wildcard asks for nothing extra.
    await user.selectOptions(screen.getByLabelText('Вид scope для network.http'), 'network')
    await user.type(screen.getByLabelText('Значения scope для network.http'), '*.example.test')
    expect(screen.queryByRole('checkbox', { name: /Подтверждаю неограниченный доступ/ })).not.toBeInTheDocument()

    // A bare "*" is every host, and the gate appears even though the kind is
    // still `network`.
    await user.clear(screen.getByLabelText('Значения scope для network.http'))
    await user.type(screen.getByLabelText('Значения scope для network.http'), '*')
    const confirmation = screen.getByRole('checkbox', { name: 'Подтверждаю неограниченный доступ для network.http' })

    await user.click(screen.getByRole('button', { name: /Включить с выбранными доступами/ }))
    expect(onConfirm).not.toHaveBeenCalled()

    await user.click(confirmation)
    await user.click(screen.getByRole('button', { name: /Включить с выбранными доступами/ }))
    expect(onConfirm).toHaveBeenCalledWith({
      pluginId: 'reference.demo',
      capabilities: [{ capability: 'network.http', scopeKind: 'network', scopeValues: ['*'], allowUnrestricted: true }],
    })
  })

  it('shows a backend rejection without losing the owner input', () => {
    render(
      <PluginConsentDialog
        busy={false}
        error='consented scope for "filesystem.read" is broader than the manifest declaration'
        onCancel={vi.fn()}
        onConfirm={vi.fn()}
        plugin={plugin}
      />,
    )

    expect(screen.getByRole('alert')).toHaveTextContent('broader than the manifest declaration')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('keeps the modal contract it inherits from ApprovalDialog', async () => {
    const user = userEvent.setup()
    const onCancel = vi.fn()
    render(<PluginConsentDialog busy={false} onCancel={onCancel} onConfirm={vi.fn()} plugin={plugin} />)

    expect(screen.getByRole('button', { name: 'Отмена' })).toHaveFocus()

    // Focus stays inside the dialog in both directions.
    await user.tab({ shift: true })
    expect(screen.getByRole('dialog').contains(document.activeElement)).toBe(true)

    await user.keyboard('{Escape}')
    expect(onCancel).toHaveBeenCalledTimes(1)
  })

  it('ignores dismissal while the enable request is in flight', async () => {
    const user = userEvent.setup()
    const onCancel = vi.fn()
    render(<PluginConsentDialog busy onCancel={onCancel} onConfirm={vi.fn()} plugin={plugin} />)

    await user.keyboard('{Escape}')
    expect(onCancel).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})
