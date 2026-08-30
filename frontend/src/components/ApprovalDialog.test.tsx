// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'

import type { ApprovalRequest } from '../lib/contracts'
import { ApprovalDialog } from './ApprovalDialog'

const approval: ApprovalRequest = {
  id: 'approval-1',
  toolCallId: 'call-1',
  title: 'Записать файл в Documents',
  explanation: 'Yuri хочет создать файл notes.md.',
  risk: 'high',
  scope: 'filesystem.write ~/Documents/notes.md',
}

function Harness({ error }: { error?: string }) {
  const [open, setOpen] = useState(false)
  return (
    <div>
      <button onClick={() => setOpen(true)} type="button">Открыть подтверждение</button>
      {open && (
        <ApprovalDialog
          approval={approval}
          busy={false}
          error={error}
          onDecision={() => setOpen(false)}
          onDismiss={() => setOpen(false)}
        />
      )}
    </div>
  )
}

describe('ApprovalDialog', () => {
  it('exposes a labelled modal dialog', () => {
    render(<ApprovalDialog approval={approval} busy={false} onDecision={vi.fn()} onDismiss={vi.fn()} />)
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(dialog).toHaveAccessibleName('Записать файл в Documents')
    expect(dialog).toHaveAccessibleDescription('Yuri хочет создать файл notes.md.')
  })

  it('moves focus to the safe default on open and restores it to the trigger on close', async () => {
    const user = userEvent.setup()
    render(<Harness />)

    const trigger = screen.getByRole('button', { name: 'Открыть подтверждение' })
    await user.click(trigger)

    expect(screen.getByRole('button', { name: 'Отклонить' })).toHaveFocus()

    await user.keyboard('{Escape}')

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()
  })

  it('keeps Tab inside the dialog in both directions', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    await user.click(screen.getByRole('button', { name: 'Открыть подтверждение' }))

    const deny = screen.getByRole('button', { name: 'Отклонить' })
    const approve = screen.getByRole('button', { name: 'Разрешить действие' })

    expect(deny).toHaveFocus()
    await user.tab()
    expect(approve).toHaveFocus()
    // Past the last control the trap wraps instead of escaping into the page.
    await user.tab()
    expect(deny).toHaveFocus()
    await user.tab({ shift: true })
    expect(approve).toHaveFocus()
  })

  it('denies the action on Escape and on a backdrop click', async () => {
    const user = userEvent.setup()
    const onDecision = vi.fn()
    const { unmount } = render(
      <ApprovalDialog approval={approval} busy={false} onDecision={onDecision} onDismiss={vi.fn()} />,
    )

    await user.keyboard('{Escape}')
    expect(onDecision).toHaveBeenCalledWith('deny')

    unmount()
    onDecision.mockClear()

    const { container } = render(
      <ApprovalDialog approval={approval} busy={false} onDecision={onDecision} onDismiss={vi.fn()} />,
    )
    await user.click(container.querySelector('.approval-backdrop') as HTMLElement)
    expect(onDecision).toHaveBeenCalledWith('deny')
  })

  it('ignores dismissal gestures while a decision is in flight', async () => {
    const user = userEvent.setup()
    const onDecision = vi.fn()
    const onDismiss = vi.fn()
    render(<ApprovalDialog approval={approval} busy onDecision={onDecision} onDismiss={onDismiss} />)

    await user.keyboard('{Escape}')

    expect(onDecision).not.toHaveBeenCalled()
    expect(onDismiss).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('shows a failed decision and offers an explicit way out', async () => {
    const user = userEvent.setup()
    const onDecision = vi.fn()
    const onDismiss = vi.fn()
    render(
      <ApprovalDialog
        approval={approval}
        busy={false}
        error="Мост недоступен"
        onDecision={onDecision}
        onDismiss={onDismiss}
      />,
    )

    expect(screen.getByRole('alert')).toHaveTextContent('Мост недоступен')
    // Both decisions stay live so the user can retry after a transient failure.
    expect(screen.getByRole('button', { name: 'Отклонить' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Разрешить действие' })).toBeEnabled()

    await user.keyboard('{Escape}')
    // Escape stops retrying a bridge that already failed and just closes.
    expect(onDecision).not.toHaveBeenCalled()
    expect(onDismiss).toHaveBeenCalledTimes(1)

    await user.click(screen.getByRole('button', { name: 'Закрыть' }))
    expect(onDismiss).toHaveBeenCalledTimes(2)
  })

  it('hides the rest of the view while it is open', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    const trigger = screen.getByRole('button', { name: 'Открыть подтверждение' })

    await user.click(trigger)
    expect(trigger).toHaveAttribute('inert')
    expect(trigger).toHaveAttribute('aria-hidden', 'true')

    await user.keyboard('{Escape}')
    expect(trigger).not.toHaveAttribute('inert')
    expect(trigger).not.toHaveAttribute('aria-hidden')
  })
})
