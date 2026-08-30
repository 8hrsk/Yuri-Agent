// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import type { PersonalitySnapshot, YuriClient } from '../lib/contracts'
import { createStarterPersonalitySnapshot } from '../lib/personality'
import { PersonaRelationshipView } from './PersonaRelationshipView'

const snapshot: PersonalitySnapshot = createStarterPersonalitySnapshot()

const clientStub = {
  mode: 'mock',
  getPersonaSnapshot: async () => snapshot,
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
})
