import { memo } from 'react'

import type { Conversation } from '../lib/contracts'
import { formatClock } from '../lib/datetime'

type ConversationListItemProps = {
  conversation: Conversation
  selected: boolean
  onSelect: (conversationId: string) => void
}

const ConversationListItem = memo(function ConversationListItem({ conversation, selected, onSelect }: ConversationListItemProps) {
  return (
    <button
      aria-current={selected ? 'true' : undefined}
      className={`conversation-item${selected ? ' conversation-item--active' : ''}`}
      onClick={() => onSelect(conversation.id)}
      role="listitem"
      type="button"
    >
      <span className="conversation-item__mark"><span /></span>
      <span className="conversation-item__copy">
        <strong>{conversation.title}</strong>
        <small>{conversation.preview || 'Пока нет сообщений'}</small>
      </span>
      <time dateTime={conversation.updatedAt}>{formatClock(new Date(conversation.updatedAt))}</time>
    </button>
  )
})

type ConversationListProps = {
  conversations: Conversation[]
  loading: boolean
  onSelect: (conversationId: string) => void
  selectedId: string
}

/**
 * The sidebar list. Memoized because it is redrawn by nothing that happens
 * during a run: a streaming answer never touches the conversation summaries,
 * yet it used to re-render — and re-format a timestamp for — every row.
 */
export const ConversationList = memo(function ConversationList({ conversations, loading, onSelect, selectedId }: ConversationListProps) {
  return (
    <div className="conversation-list" role="list">
      {loading && <div className="conversation-empty">Загружаю локальные диалоги…</div>}
      {!loading && conversations.length === 0 && <div className="conversation-empty">Диалоги не найдены</div>}
      {conversations.map((conversation) => (
        <ConversationListItem
          conversation={conversation}
          key={conversation.id}
          onSelect={onSelect}
          selected={conversation.id === selectedId}
        />
      ))}
    </div>
  )
})
