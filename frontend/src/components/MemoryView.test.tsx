// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { MemoryRecord, YuriClient } from '../lib/contracts'
import { MemoryView } from './MemoryView'

const ownerSeedMemory: MemoryRecord = {
  id: 'backstory-1', agentId: 'agent-emily', agentName: 'Emily', scope: 'agent_private',
  kind: 'episodic', contentKind: 'fiction', content: 'Я одна закрывала старую библиотеку.',
  confidence: 1, salience: .8, lifecycleState: 'active', pinned: false, accessCount: 2,
  createdAt: '2026-08-31T10:00:00Z', updatedAt: '2026-08-31T10:00:00Z',
  sources: [{ sourceType: 'identity_seed', sourceId: 'agent-emily:personalization:v2' }],
  fiction: {
    provenance: 'owner_seed', recallState: 'remembered', epistemicStatus: 'fictional', ownerAuthored: true,
    episodeId: 'late-library', personalizationRevisionId: 'agent-emily:personalization:v2',
  },
  history: [{ version: 1, operation: 'create', reason: 'owner-authored fictional identity seed', createdAt: '2026-08-31T10:00:00Z' }],
}

let clientStub: YuriClient

vi.mock('../lib/client', () => ({
  createYuriClient: () => clientStub,
  subscribeMemoryUpdates: () => () => undefined,
}))

afterEach(() => vi.restoreAllMocks())

describe('MemoryView fictional backstory curation', () => {
  it('shows epistemic provenance and uses dedicated owner-seed actions', async () => {
    const updateBackstoryMemory = vi.fn(async (_id: string, content: string) => ({ ...ownerSeedMemory, content, history: [...ownerSeedMemory.history, { version: 2, operation: 'restore', createdAt: '2026-08-31T11:00:00Z' }] }))
    const disableBackstoryMemory = vi.fn(async () => ({ ...ownerSeedMemory, lifecycleState: 'deleted' as const }))
    const rehydrateBackstoryMemory = vi.fn(async () => ownerSeedMemory)
    clientStub = {
      mode: 'mock', listMemories: async () => [ownerSeedMemory],
      updateBackstoryMemory, disableBackstoryMemory, rehydrateBackstoryMemory,
    } as unknown as YuriClient
    vi.spyOn(globalThis, 'confirm').mockReturnValue(true)
    const user = userEvent.setup()

    render(<MemoryView />)

    expect(await screen.findByText('Исходник владельца')).toBeInTheDocument()
    expect(screen.getByText('Вспомнено агентом')).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: 'Видимость воспоминания' })).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Удалить' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Изменить' }))
    const editor = screen.getByRole('textbox', { name: 'Текст воспоминания' })
    await user.clear(editor)
    await user.type(editor, 'Я поняла, почему тот вечер был важен.')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    await waitFor(() => expect(updateBackstoryMemory).toHaveBeenCalledWith('backstory-1', 'Я поняла, почему тот вечер был важен.'))

    await user.click(screen.getByRole('button', { name: 'Отключить эпизод' }))
    await waitFor(() => expect(disableBackstoryMemory).toHaveBeenCalledWith('backstory-1'))
    expect(await screen.findByRole('button', { name: 'Перегидратировать из backstory' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Перегидратировать из backstory' }))
    await waitFor(() => expect(rehydrateBackstoryMemory).toHaveBeenCalledWith('backstory-1'))
  })
})
