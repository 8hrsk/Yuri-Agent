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
  initiatorProviderId: 'codex', initiatorModel: 'gpt-5.6', peerProviderId: 'openrouter', peerModel: 'openrouter/free',
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
	const dialogueWithHistory: PeerDialogue = {
	  ...autonomousDialogue,
	  messages: [{
		id: 'peer-message-1', sequence: 1, sourceRunId: 'run-peer-1', senderAgentId: 'agent-mira', senderName: 'Мира',
		recipientAgentId: 'agent-yuri', recipientName: 'Юри', content: 'Исторический ответ.', createdAt: '2026-08-30T10:00:04.000Z',
		providerId: 'openrouter', model: 'historic/model', totalTokens: 150,
	  }],
	}
    clientStub = {
      mode: 'mock',
      listPeerDialogues: async () => [dialogueWithHistory],
      listPeerRelationships: async () => [],
    } as unknown as YuriClient
    render(<CollaborationView activeAgentId="agent-yuri" />)

    expect(await screen.findByText('автономный триггер')).toBeInTheDocument()
    expect(screen.getByText('Нужна независимая проверка архитектурного решения.')).toBeInTheDocument()
    expect(screen.getByText('Сверить границы нового среза')).toBeInTheDocument()
    const routes = screen.getByLabelText('Текущие маршруты моделей участников')
    expect(routes).toHaveTextContent('codex · gpt-5.6')
    expect(routes).toHaveTextContent('openrouter · openrouter/free')
    expect(screen.getByLabelText('Бюджет диалога')).toHaveTextContent('180')
    expect(screen.getByText('Исторический ответ.')).toBeInTheDocument()
    expect(screen.getByText('openrouter · historic/model')).toBeInTheDocument()
    expect(screen.getByText('150 ток.')).toBeInTheDocument()
  })

  it('explains a typed peer failure and keeps route choice explicit', async () => {
    clientStub = {
      mode: 'mock',
      listPeerDialogues: async () => [{
        ...autonomousDialogue,
        id: 'dialogue-failed',
        status: 'failed',
        failure: 'Провайдер временно недоступен',
        failureKind: 'transient',
        retryable: true,
      }],
      listPeerRelationships: async () => [],
    } as unknown as YuriClient
    render(<CollaborationView activeAgentId="agent-yuri" />)

    expect(await screen.findByText('Провайдер временно недоступен')).toBeInTheDocument()
    expect(screen.getByText('Маршрут не переключён. Повторите запрос позже.')).toBeInTheDocument()
  })

  it('opens provider recovery from a failed peer dialogue without retrying it automatically', async () => {
    const user = userEvent.setup()
    const openSettings = vi.fn()
    clientStub = {
      mode: 'mock',
      listPeerDialogues: async () => [{
        ...autonomousDialogue,
        id: 'dialogue-auth-failed',
        status: 'failed',
        failure: 'Авторизация провайдера недоступна',
        failureKind: 'authentication',
      }],
      listPeerRelationships: async () => [],
    } as unknown as YuriClient
    render(<CollaborationView activeAgentId="agent-yuri" onOpenSettings={openSettings} />)

    await user.click(await screen.findByRole('button', { name: 'Открыть Settings' }))
    expect(openSettings).toHaveBeenCalledTimes(1)
  })

  it('opens Personality for the agent whose peer turn actually failed', async () => {
    const user = userEvent.setup()
    const openAgentPersonality = vi.fn()
    clientStub = {
      mode: 'mock',
      listPeerDialogues: async () => [{
        ...autonomousDialogue,
        id: 'dialogue-model-failed',
        status: 'failed',
        failure: 'Выбранная модель недоступна у провайдера',
        failureKind: 'model_unavailable',
        messages: [{
          id: 'opening-message', sequence: 0, sourceRunId: 'run-opening', senderAgentId: 'agent-yuri', senderName: 'Юри',
          recipientAgentId: 'agent-mira', recipientName: 'Мира', content: 'Проверь решение.', createdAt: '2026-08-30T10:00:01.000Z',
        }],
      }],
      listPeerRelationships: async () => [],
    } as unknown as YuriClient
    render(<CollaborationView activeAgentId="agent-yuri" onOpenAgentPersonality={openAgentPersonality} />)

    await user.click(await screen.findByRole('button', { name: 'Выбрать модель агента' }))
    expect(openAgentPersonality).toHaveBeenCalledWith('agent-mira')
  })
})
