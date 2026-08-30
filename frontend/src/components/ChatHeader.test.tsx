// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import type { AvatarState, RunStatus } from '../lib/contracts'
import { ChatHeader } from './ChatHeader'

const props: {
  agentName: string
  avatarState: AvatarState
  runLabel: string
  runStatus: RunStatus
  title: string
} = {
  agentName: 'Yuri',
  avatarState: 'idle',
  runLabel: 'Жду задачу',
  runStatus: 'idle',
  title: 'Новый диалог',
}

describe('ChatHeader conversation title editor', () => {
  it('renames the conversation through the owner callback', async () => {
    const user = userEvent.setup()
    const onRename = vi.fn(async () => undefined)
    render(<ChatHeader {...props} onRename={onRename} />)

    await user.click(screen.getByRole('button', { name: 'Переименовать диалог' }))
    const input = screen.getByRole('textbox', { name: 'Название диалога' })
    await user.clear(input)
    await user.type(input, 'План на неделю')
    await user.click(screen.getByRole('button', { name: 'Сохранить название диалога' }))

    expect(onRename).toHaveBeenCalledWith('План на неделю')
    expect(await screen.findByRole('heading', { name: 'Новый диалог' })).toBeInTheDocument()
  })

  it('keeps the editor open when the rename fails', async () => {
    const user = userEvent.setup()
    const onRename = vi.fn(async () => { throw new Error('Backend недоступен') })
    render(<ChatHeader {...props} onRename={onRename} />)

    await user.click(screen.getByRole('button', { name: 'Переименовать диалог' }))
    const input = screen.getByRole('textbox', { name: 'Название диалога' })
    await user.clear(input)
    await user.type(input, 'План на неделю')
    await user.click(screen.getByRole('button', { name: 'Сохранить название диалога' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('Backend недоступен')
    expect(screen.getByRole('textbox', { name: 'Название диалога' })).toBeInTheDocument()
  })
})
