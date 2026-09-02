import { memo } from 'react'

import type { Conversation } from '../lib/contracts'
import { formatClock } from '../lib/datetime'
import { Icon } from './Icon'

type ConversationListItemProps = {
  conversation: Conversation
  selected: boolean
  onDelete: (conversation: Conversation) => void
  onSelect: (conversationId: string) => void
}

const ConversationListItem = memo(function ConversationListItem({ conversation, selected, onDelete, onSelect }: ConversationListItemProps) {
  return (
    <div className={`conversation-row${selected ? ' conversation-row--active' : ''}`} role="listitem">
      <button aria-current={selected ? 'true' : undefined} className="conversation-item" onClick={() => onSelect(conversation.id)} type="button">
        <span className="conversation-item__mark"><span /></span>
        <span className="conversation-item__copy">
          <strong>{conversation.title}</strong>
          <small>{conversation.preview || 'Пока нет сообщений'}</small>
        </span>
        <time dateTime={conversation.updatedAt}>{formatClock(new Date(conversation.updatedAt))}</time>
      </button>
      <button aria-label={`Удалить диалог «${conversation.title}»`} className="conversation-item__delete" onClick={() => onDelete(conversation)} title="Удалить диалог" type="button">
        <Icon name="trash" width={13} height={13} />
      </button>
    </div>
  )
})

type ConversationListProps = {
  conversations: Conversation[]
  loading: boolean
  onDelete: (conversation: Conversation) => void
  onSelect: (conversationId: string) => void
  selectedId: string
}

/**
 * The sidebar list. Memoized because it is redrawn by nothing that happens
 * during a run: a streaming answer never touches the conversation summaries,
 * yet it used to re-render — and re-format a timestamp for — every row.
 */
export const ConversationList = memo(function ConversationList({ conversations, loading, onDelete, onSelect, selectedId }: ConversationListProps) {
  return (
    <div className="conversation-list" role="list">
      {loading && <div className="conversation-empty">Загружаю локальные диалоги…</div>}
      {!loading && conversations.length === 0 && <div className="conversation-empty">Диалоги не найдены</div>}
      {conversations.map((conversation) => (
        <ConversationListItem
          conversation={conversation}
          key={conversation.id}
          onDelete={onDelete}
          onSelect={onSelect}
          selected={conversation.id === selectedId}
        />
      ))}
    </div>
  )
})
