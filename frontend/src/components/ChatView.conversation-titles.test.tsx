// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'

import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { BackendConnection } from '../lib/backend'
import type { Conversation, YuriClient } from '../lib/contracts'
import { ChatView } from './ChatView'

type RuntimeListener = (value: unknown) => void

let clientStub: YuriClient
let emitConversationEvent: (value: unknown) => void = () => undefined

vi.mock('../lib/client', () => ({
  createYuriClient: () => clientStub,
}))

const backend: BackendConnection = { status: 'connected', label: 'Backend connected', detail: 'Wails runtime is ready' }

function conversationFixture(): Conversation {
  return {
    id: 'conversation-1',
    title: 'Новый диалог',
    titleSource: 'default',
    preview: '',
    updatedAt: '2026-08-30T10:00:00.000Z',
    messages: [],
  }
}

beforeEach(() => {
  const listeners: RuntimeListener[] = []
  emitConversationEvent = (value) => listeners.slice().forEach((listener) => listener(value))
  Object.defineProperty(window, 'runtime', {
    configurable: true,
    value: {
      EventsOn: (name: string, listener: RuntimeListener) => {
        if (name === 'yuri:conversation') listeners.push(listener)
        return () => {
          const index = listeners.indexOf(listener)
          if (index >= 0) listeners.splice(index, 1)
        }
      },
    },
  })
  Object.defineProperty(window, 'speechSynthesis', {
    configurable: true,
    value: { cancel: vi.fn(), speak: vi.fn() },
  })
  Element.prototype.scrollIntoView = vi.fn()
})

afterEach(() => {
  cleanup()
  delete (window as { runtime?: unknown }).runtime
})

describe('ChatView conversation title updates', () => {
  it('updates both sidebar and header from the separate conversation event', async () => {
    clientStub = {
      mode: 'mock',
      listConversations: async () => [conversationFixture()],
      listMessages: async (conversationId: string) => ({ conversationId, messages: [], traces: [], hasMore: false }),
      createConversation: async () => conversationFixture(),
      listChatTools: async () => [],
      getAllowedDirectories: async () => [],
      transcribeAudio: async () => '',
      renameConversation: vi.fn(async () => undefined),
    } as unknown as YuriClient

    render(<ChatView agentName="Yuri" backend={backend} onOpenSettings={vi.fn()} />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Новый диалог' })).toBeInTheDocument())

    act(() => {
      emitConversationEvent({
        type: 'conversation.title.updated',
        conversationId: 'conversation-1',
        title: 'План релиза',
        titleSource: 'generated',
        updatedAt: '2026-08-30T10:01:00.000Z',
      })
    })

    expect(screen.getByRole('heading', { name: 'План релиза' })).toBeInTheDocument()
    expect(screen.getByText('План релиза', { selector: '.conversation-item__copy strong' })).toBeInTheDocument()
  })
})
