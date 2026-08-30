// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import type { PeerDialogue, PeerRelationshipDetail, YuriClient } from '../lib/contracts'
import { CollaborationView } from './CollaborationView'

const detail: PeerRelationshipDetail = {
  relationship: {
    observerAgentId: 'agent-yuri', peerAgentId: 'agent-mira', peerName: 'Мира', relationshipId: 'rel-1',
    version: 2, currentVersionId: 'version-2', summary: 'Считает Миру полезной собеседницей.',
    dimensions: { trust: 0.72 },
    opinions: [{ id: 'opinion-1', subject: 'Мира', content: 'Она замечает пробелы в плане.', label: 'opinion', confidence: 0.68, evidence: [] }],
    evidence: [], updatedAt: '2026-08-30T10:00:00.000Z',
  },
  versions: [
    { id: 'version-2', version: 2, parentId: 'version-1', operation: 'update', summary: 'Считает Миру полезной собеседницей.', dimensions: { trust: 0.72 }, opinions: [], reason: 'Рефлексия.', evidence: [], createdAt: '2026-08-30T10:00:00.000Z' },
    { id: 'version-1', version: 1, operation: 'create', summary: 'Отношение ещё не сформировано.', dimensions: {}, opinions: [], reason: 'Создание.', evidence: [], createdAt: '2026-08-30T09:00:00.000Z' },
  ],
}

const autonomousDialogue: PeerDialogue = {
  id: 'dialogue-auto', initiatorAgentId: 'agent-yuri', initiatorName: 'Юри', peerAgentId: 'agent-mira', peerName: 'Мира',
  triggerKind: 'autonomous', triggerReason: 'Нужна независимая проверка архитектурного решения.', purpose: 'Сверить границы нового среза',
  status: 'completed', turnCount: 1, maxTurns: 2, tokensUsed: 180, maxTokens: 2400,
  createdAt: '2026-08-30T10:00:00.000Z', finishedAt: '2026-08-30T10:00:05.000Z', messages: [],
}

let clientStub: YuriClient

vi.mock('../lib/client', () => ({ createYuriClient: () => clientStub }))

describe('Collaboration peer relationships', () => {
  it('marks opinions as subjective and exposes append-only recovery controls', async () => {
    const reset = vi.fn(async () => detail)
    const rollback = vi.fn(async () => detail)
    clientStub = {
      mode: 'mock',
      listPeerDialogues: async () => [],
      listPeerRelationships: async () => [detail.relationship],
      getPeerRelationship: async () => detail,
      resetPeerRelationship: reset,
      rollbackPeerRelationship: rollback,
    } as unknown as YuriClient
    const user = userEvent.setup()
    render(<CollaborationView activeAgentId="agent-yuri" />)

    await screen.findByText('Она замечает пробелы в плане.')
    expect(screen.getByText('мнение, не факт')).toBeInTheDocument()
    expect(screen.getByText('Доверие').parentElement).toHaveTextContent('72%')

    await user.click(screen.getByText(/История изменений/))
    const returnButton = await screen.findByRole('button', { name: 'Вернуть эту версию' })
    await user.click(returnButton)
    await waitFor(() => expect(rollback).toHaveBeenCalledWith('agent-mira', 'version-1'))

    const card = screen.getByText('Она замечает пробелы в плане.').closest('article')
    expect(card).not.toBeNull()
    await user.click(within(card as HTMLElement).getByRole('button', { name: 'Сбросить мнение' }))
    await waitFor(() => expect(reset).toHaveBeenCalledWith('agent-mira'))
    expect(screen.getByText(/Предыдущая версия сохранена в истории/)).toBeInTheDocument()
  })

  it('shows autonomous dialogue provenance separately from its transcript', async () => {
    clientStub = {
      mode: 'mock',
      listPeerDialogues: async () => [autonomousDialogue],
      listPeerRelationships: async () => [],
    } as unknown as YuriClient
    render(<CollaborationView activeAgentId="agent-yuri" />)

    expect(await screen.findByText('автономный триггер')).toBeInTheDocument()
    expect(screen.getByText('Нужна независимая проверка архитектурного решения.')).toBeInTheDocument()
    expect(screen.getByText('Сверить границы нового среза')).toBeInTheDocument()
  })
})
