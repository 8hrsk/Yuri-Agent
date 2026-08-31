// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import type { AgentPersonalizationProfile, AgentProfile, PersonalitySnapshot, YuriClient } from '../lib/contracts'
import { clonePersonalization, defaultAgentDraft } from '../lib/agents'
import { createStarterPersonalitySnapshot } from '../lib/personality'
import { PersonaRelationshipView } from './PersonaRelationshipView'

const snapshot: PersonalitySnapshot = createStarterPersonalitySnapshot()
const activeAgent: AgentProfile = {
  id: 'agent-yuri', name: 'Yuri', age: 21, gender: 'female', preferences: defaultAgentDraft.preferences,
  backstory: '', traits: { ...defaultAgentDraft.traits }, active: true,
  createdAt: '2026-08-31T08:00:00Z', updatedAt: '2026-08-31T08:00:00Z',
}
const ownerSeed: AgentPersonalizationProfile = {
  ...clonePersonalization(defaultAgentDraft.personalization),
  agentId: activeAgent.id, schemaVersion: 2, version: 1, revisionId: 'seed-1',
  operation: 'owner_create', reason: 'initial owner seed', createdAt: activeAgent.createdAt, updatedAt: activeAgent.updatedAt,
  temperament: { ...defaultAgentDraft.traits },
}
const updateActiveAgentPersonalization = vi.fn(async (input: { personalization: AgentPersonalizationProfile }) => ({ ...ownerSeed, ...clonePersonalization(input.personalization), version: 2, revisionId: 'seed-2' }))

const clientStub = {
  mode: 'mock',
  getPersonaSnapshot: async () => snapshot,
  getActiveAgent: async () => activeAgent,
  getActiveAgentPersonalization: async () => ownerSeed,
  updateActiveAgentPersonalization,
} as unknown as YuriClient

vi.mock('../lib/client', () => ({
  createYuriClient: () => clientStub,
  createStarterPersonalitySnapshot: () => snapshot,
  normalizePersonalitySnapshot: (value: PersonalitySnapshot) => value,
  subscribePersonaUpdates: () => () => undefined,
}))

/**
 * Both destinations used to render the same page, with the section changing
 * nothing but a heading: traits, opinions, relationship signals and the
 * recovery controls were all on screen either way.
 */
describe('Personality and Relationship are two destinations, not one page', () => {
  it('shows the persona surfaces on Personality and none of them on Relationship', async () => {
    render(<PersonaRelationshipView section="personality" />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Черты характера' })).toBeInTheDocument())

    expect(screen.getByRole('heading', { name: 'Профиль Yuri' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'История и причины' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Управление состоянием' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Сигналы связи' })).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Мнение о пользователе' })).not.toBeInTheDocument()
  })

  it('shows the bond surfaces on Relationship and none of the persona editing', async () => {
    render(<PersonaRelationshipView section="relationship" />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Сигналы связи' })).toBeInTheDocument())

    expect(screen.getByRole('heading', { name: 'Мнение о пользователе' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'История связи' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Управление связью' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Черты характера' })).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'История и причины' })).not.toBeInTheDocument()
    // Persona recovery remains separate from relationship recovery.
    expect(screen.queryByRole('heading', { name: 'Управление состоянием' })).not.toBeInTheDocument()
  })

  it('moves the whole shell from the in-view tabs instead of swapping content behind the rail', async () => {
    const user = userEvent.setup()
    const onSelectSection = vi.fn()
    render(<PersonaRelationshipView onSelectSection={onSelectSection} section="personality" />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Черты характера' })).toBeInTheDocument())

    await user.click(screen.getByRole('tab', { name: /Relationship/ }))

    expect(onSelectSection).toHaveBeenCalledWith('relationship')
    // The view does not move itself: the shell owns the destination, so the
    // nav rail and the page can never disagree about where the user is.
    expect(screen.getByRole('heading', { name: 'Черты характера' })).toBeInTheDocument()
  })

  it('creates an append-only owner baseline revision without unlocking core identity', async () => {
    const user = userEvent.setup()
    updateActiveAgentPersonalization.mockClear()
    render(<PersonaRelationshipView section="personality" />)
    await waitFor(() => expect(screen.getByRole('button', { name: /Редактировать baseline/ })).toBeEnabled())

    await user.click(screen.getByRole('button', { name: /Редактировать baseline/ }))
    expect(screen.getByRole('textbox', { name: /Имя агента/ })).toBeDisabled()
    await user.type(screen.getByRole('textbox', { name: /Причина изменения/ }), 'Сделать исходный стиль мягче')
    await user.click(screen.getByRole('button', { name: /Review/ }))
    await user.click(screen.getByRole('button', { name: /Сохранить revision/ }))

    await waitFor(() => expect(updateActiveAgentPersonalization).toHaveBeenCalledTimes(1))
    expect(updateActiveAgentPersonalization).toHaveBeenCalledWith(expect.objectContaining({
      expectedVersion: 1,
      reason: 'Сделать исходный стиль мягче',
    }))
    expect(await screen.findByText(/Owner baseline сохранён как revision v2/)).toBeInTheDocument()
  })

  it('saves per-agent reflection budget and layer locks as one owner revision', async () => {
    const user = userEvent.setup()
    updateActiveAgentPersonalization.mockClear()
    render(<PersonaRelationshipView section="personality" />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Границы развития' })).toBeInTheDocument())

    const tokenBudget = screen.getByRole('spinbutton', { name: 'Token budget рефлексии' })
    await user.clear(tokenBudget)
    await user.type(tokenBudget, '1800')
    await user.click(screen.getByRole('checkbox', { name: /Mutable persona/ }))
    await user.click(screen.getByRole('button', { name: /Сохранить policy revision/ }))

    await waitFor(() => expect(updateActiveAgentPersonalization).toHaveBeenCalledTimes(1))
    expect(updateActiveAgentPersonalization).toHaveBeenCalledWith(expect.objectContaining({
      expectedVersion: 1,
      reason: 'Владелец обновил policy развития агента',
      personalization: expect.objectContaining({ evolutionPolicy: expect.objectContaining({ reflectionMaxTokens: 1800, lockedFields: expect.arrayContaining(['identity', 'backstory', 'mutable_persona']) }) }),
    }))
    expect(await screen.findByText(/Evolution policy сохранена как owner revision v2/)).toBeInTheDocument()
  })
})
