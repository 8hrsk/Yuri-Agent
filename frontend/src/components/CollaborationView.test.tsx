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
  status: 'completed', turnCount: 1, minTurns: 1, maxTurns: 2, tokensUsed: 180, maxTokens: 2400,
  maxDurationSeconds: 90, durationUsedSeconds: 5, cooldownSeconds: 300, budgetOrigin: 'agent_default', completionReason: 'semantic',
  createdAt: '2026-08-30T10:00:00.000Z', finishedAt: '2026-08-30T10:00:05.000Z', messages: [],
}

let clientStub: YuriClient

vi.mock('../lib/client', () => ({ createYuriClient: () => clientStub }))

describe('Collaboration peer relationships', () => {
  it('applies a read-only adaptive budget recommendation before launch', async () => {
    const recommendPeerDialogueBudget = vi.fn(async () => ({
      recommended: { minTurns: 2, maxTurns: 3, maxTokens: 6500, maxDurationSeconds: 70 },
      ceiling: { minTurns: 2, maxTurns: 4, maxTokens: 8000, maxDurationSeconds: 90 },
      basis: 'similar_history' as const, sampleCount: 3, confidence: 'high' as const,
      rationale: 'Учтены похожие завершённые диалоги этой пары: 3.',
    }))
    clientStub = {
      mode: 'mock',
      listAgents: async () => [
        { id: 'agent-yuri', name: 'Юри', gender: 'female', preferences: '', backstory: '', traits: {}, active: true, executionBudget: 'balanced', createdAt: '2026-08-30T09:00:00Z', updatedAt: '2026-08-30T09:00:00Z' },
        { id: 'agent-mira', name: 'Мира', gender: 'female', preferences: '', backstory: '', traits: {}, active: false, executionBudget: 'efficient', createdAt: '2026-08-30T09:00:00Z', updatedAt: '2026-08-30T09:00:00Z' },
      ],
      listPeerDialogues: async () => [],
      listPeerRelationships: async () => [],
      recommendPeerDialogueBudget,
    } as unknown as YuriClient
    const user = userEvent.setup()
    render(<CollaborationView activeAgentId="agent-yuri" />)

    await user.type(await screen.findByRole('textbox', { name: 'Цель' }), 'Проверить архитектуру')
    await user.click(screen.getByRole('button', { name: 'Подобрать лимит' }))

    await waitFor(() => expect(recommendPeerDialogueBudget).toHaveBeenCalledWith('agent-mira', 'Проверить архитектуру'))
    expect(screen.getByRole('spinbutton', { name: 'Макс. ходов' })).toHaveValue(3)
    expect(screen.getByRole('spinbutton', { name: 'Макс. токенов' })).toHaveValue(6500)
    expect(screen.getByRole('spinbutton', { name: 'Макс. время, сек.' })).toHaveValue(70)
    expect(screen.getByText(/Уверенность: высокая · примеров: 3/)).toBeInTheDocument()
    expect(screen.getByText(/Жёсткий потолок: ходы 4 · токены 8 000 · время 1 мин 30 с/)).toBeInTheDocument()

    await user.clear(screen.getByRole('spinbutton', { name: 'Макс. токенов' }))
    await user.type(screen.getByRole('spinbutton', { name: 'Макс. токенов' }), '6000')
    expect(screen.queryByText('Рекомендация применена')).not.toBeInTheDocument()
  })

  it('starts an owner-initiated exchange with an explicit narrowing budget', async () => {
    const startPeerDialogue = vi.fn(async () => ({ id: 'manual-1', minTurns: 1, maxTurns: 1, maxTokens: 2000, maxDurationSeconds: 30 }))
    clientStub = {
      mode: 'mock',
      listAgents: async () => [
        { id: 'agent-yuri', name: 'Юри', gender: 'female', preferences: '', backstory: '', traits: {}, active: true, executionBudget: 'balanced', createdAt: '2026-08-30T09:00:00Z', updatedAt: '2026-08-30T09:00:00Z' },
        { id: 'agent-mira', name: 'Мира', gender: 'female', preferences: '', backstory: '', traits: {}, active: false, executionBudget: 'efficient', createdAt: '2026-08-30T09:00:00Z', updatedAt: '2026-08-30T09:00:00Z' },
      ],
      listPeerDialogues: async () => [],
      listPeerRelationships: async () => [],
      startPeerDialogue,
    } as unknown as YuriClient
    const user = userEvent.setup()
    render(<CollaborationView activeAgentId="agent-yuri" />)

    await user.type(await screen.findByRole('textbox', { name: 'Цель' }), 'Проверить план')
    await user.type(screen.getByRole('textbox', { name: 'Первое сообщение' }), 'Посмотри архитектуру.')
    await user.clear(screen.getByRole('spinbutton', { name: 'Макс. ходов' }))
    await user.type(screen.getByRole('spinbutton', { name: 'Макс. ходов' }), '1')
    await user.clear(screen.getByRole('spinbutton', { name: 'Макс. токенов' }))
    await user.type(screen.getByRole('spinbutton', { name: 'Макс. токенов' }), '2000')
    await user.clear(screen.getByRole('spinbutton', { name: 'Макс. время, сек.' }))
    await user.type(screen.getByRole('spinbutton', { name: 'Макс. время, сек.' }), '30')
    await user.click(screen.getByRole('button', { name: 'Начать bounded-диалог' }))

    await waitFor(() => expect(startPeerDialogue).toHaveBeenCalledWith({
      peerAgentId: 'agent-mira', purpose: 'Проверить план', message: 'Посмотри архитектуру.',
      maxTurns: 1, maxTokens: 2000, maxDurationSeconds: 30, budgetSource: 'custom',
    }))
    expect(await screen.findByText(/1–1 ходов, до 2 000 токенов и 30 с/)).toBeInTheDocument()
  })

  it('starts with recommendation provenance when the owner keeps the proposed values', async () => {
    const recommendPeerDialogueBudget = vi.fn(async () => ({
      recommended: { minTurns: 2, maxTurns: 3, maxTokens: 6500, maxDurationSeconds: 70 },
      ceiling: { minTurns: 2, maxTurns: 4, maxTokens: 8000, maxDurationSeconds: 90 },
      basis: 'pair_history' as const, sampleCount: 2, confidence: 'medium' as const, rationale: 'История пары.',
    }))
    const startPeerDialogue = vi.fn(async () => ({ id: 'manual-recommended', minTurns: 2, maxTurns: 3, maxTokens: 6500, maxDurationSeconds: 70 }))
    clientStub = {
      mode: 'mock',
      listAgents: async () => [
        { id: 'agent-yuri', name: 'Юри', gender: 'female', preferences: '', backstory: '', traits: {}, active: true, executionBudget: 'balanced', createdAt: '2026-08-30T09:00:00Z', updatedAt: '2026-08-30T09:00:00Z' },
        { id: 'agent-mira', name: 'Мира', gender: 'female', preferences: '', backstory: '', traits: {}, active: false, executionBudget: 'efficient', createdAt: '2026-08-30T09:00:00Z', updatedAt: '2026-08-30T09:00:00Z' },
      ],
      listPeerDialogues: async () => [], listPeerRelationships: async () => [], recommendPeerDialogueBudget, startPeerDialogue,
    } as unknown as YuriClient
    const user = userEvent.setup()
    render(<CollaborationView activeAgentId="agent-yuri" />)

    await user.type(await screen.findByRole('textbox', { name: 'Цель' }), 'Сверить архитектуру')
    await user.type(screen.getByRole('textbox', { name: 'Первое сообщение' }), 'Посмотри границы среза.')
    await user.click(screen.getByRole('button', { name: 'Подобрать лимит' }))
    await user.click(await screen.findByRole('button', { name: 'Начать bounded-диалог' }))

    await waitFor(() => expect(startPeerDialogue).toHaveBeenCalledWith({
      peerAgentId: 'agent-mira', purpose: 'Сверить архитектуру', message: 'Посмотри границы среза.',
      maxTurns: 3, maxTokens: 6500, maxDurationSeconds: 70, budgetSource: 'recommendation',
    }))
    expect(screen.queryByText('Рекомендация применена')).not.toBeInTheDocument()
  })

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
    expect(screen.getByLabelText('Бюджет диалога')).toHaveTextContent('1 · 1–2')
    expect(screen.getByLabelText('Причина завершения')).toHaveTextContent('агент завершил диалог по смыслу')
    expect(screen.getByText('Исторический ответ.')).toBeInTheDocument()
    expect(screen.getByText('openrouter · historic/model')).toBeInTheDocument()
    expect(screen.getByText('150 ток.')).toBeInTheDocument()
  })

  it('distinguishes a hard turn limit from semantic completion', async () => {
    const hardLimitDialogue: PeerDialogue = {
      ...autonomousDialogue,
      id: 'dialogue-turn-limit',
      purpose: 'Сверить план до жёсткого лимита.',
      turnCount: 4,
      minTurns: 2,
      maxTurns: 4,
      completionReason: 'max_turns',
    }
    clientStub = {
      mode: 'mock',
      listPeerDialogues: async () => [hardLimitDialogue],
      listPeerRelationships: async () => [],
    } as unknown as YuriClient
    render(<CollaborationView activeAgentId="agent-yuri" />)

    expect(await screen.findByLabelText('Бюджет диалога')).toHaveTextContent('4 · 2–4')
    expect(screen.getByLabelText('Причина завершения')).toHaveTextContent('достигнут максимум ходов')
    expect(screen.getByLabelText('Причина завершения')).toHaveTextContent('max_turns')
  })

  it('compares an applied recommendation with actual usage and calibrates its historical route', async () => {
    const recommendedDialogue: PeerDialogue = {
      ...autonomousDialogue,
      id: 'dialogue-recommended',
      budgetOrigin: 'owner_recommendation',
      turnCount: 2,
      tokensUsed: 2800,
      durationUsedSeconds: 45,
      maxTurns: 3,
      maxTokens: 6500,
      maxDurationSeconds: 70,
      recommendation: {
        minTurns: 2, maxTurns: 3, maxTokens: 6500, maxDurationSeconds: 70,
        basis: 'pair_history', sampleCount: 2, confidence: 'medium',
      },
    }
    clientStub = {
      mode: 'mock',
      listPeerDialogues: async () => [recommendedDialogue],
      listPeerRelationships: async () => [],
    } as unknown as YuriClient
    render(<CollaborationView activeAgentId="agent-yuri" />)

    const comparison = await screen.findByLabelText('Рекомендация и фактический расход')
    expect(comparison).toHaveTextContent('Рекомендация попала в рабочий диапазон')
    expect(comparison).toHaveTextContent('история этой пары · 2 прим.')
    expect(comparison).toHaveTextContent('2 / 3')
    expect(comparison).toHaveTextContent('2 800 / 6 500')
    expect(comparison).toHaveTextContent('45 с / 1 мин 10 с')

    const calibration = screen.getByRole('heading', { name: 'Рекомендации по маршрутам' }).closest('section')
    expect(calibration).not.toBeNull()
    expect(calibration).toHaveTextContent('codex · gpt-5.6 ↔ openrouter · openrouter/free')
    expect(calibration).toHaveTextContent('1 пример')
    expect(calibration).toHaveTextContent('Ходы67%')
    expect(calibration).toHaveTextContent('Токены43%')
    expect(calibration).toHaveTextContent('Время64%')
    expect(calibration).toHaveTextContent('Жёсткие стопы0 / 1')
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
