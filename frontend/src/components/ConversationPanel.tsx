import type { Conversation } from '../lib/contracts'
import { ConversationList } from './ConversationList'
import { Icon } from './Icon'

type ConversationPanelProps = {
  clientMode: 'wails' | 'mock'
  conversations: Conversation[]
  filter: string
  /** The backend holds conversations older than the last one in the list. */
  hasMoreConversations: boolean
  loading: boolean
  /** True while the next page of the sidebar is in flight. */
  loadingMore: boolean
  onFilterChange: (value: string) => void
  onLoadMore: () => void
  onNewConversation: () => void
  onOpenSettings: () => void
  onSelect: (conversationId: string) => void
  selectedId: string
}

export function ConversationPanel({
  clientMode,
  conversations,
  filter,
  hasMoreConversations,
  loading,
  loadingMore,
  onFilterChange,
  onLoadMore,
  onNewConversation,
  onOpenSettings,
  onSelect,
  selectedId,
}: ConversationPanelProps) {
  return (
    <aside aria-label="Список диалогов" className="conversation-panel">
      <div className="conversation-panel__header">
        <div>
          <span className="section-heading__overline">Workspace</span>
          <h1>Диалоги</h1>
        </div>
        <button aria-label="Создать новый диалог" className="round-button" onClick={onNewConversation} type="button"><Icon name="plus" width={16} height={16} /></button>
      </div>
      <label className="conversation-search">
        <Icon name="search" width={15} height={15} />
        <span className="sr-only">Поиск диалогов</span>
        <input onChange={(event) => onFilterChange(event.target.value)} placeholder="Найти диалог" value={filter} />
      </label>
      <ConversationList
        conversations={conversations}
        loading={loading}
        onSelect={onSelect}
        selectedId={selectedId}
      />
      {/*
        * Shown whenever the backend holds more than the sidebar has, including
        * while a filter is applied: the filter runs over what is loaded, so
        * pulling the rest in is exactly what a reader searching for an old
        * conversation needs to be able to do.
        */}
      {hasMoreConversations && (
        <button
          className="text-button conversation-more"
          disabled={loadingMore}
          onClick={onLoadMore}
          type="button"
        >
          {loadingMore ? 'Загружаю…' : 'Показать ещё диалоги'}
        </button>
      )}
      <div className="conversation-panel__footer">
        <span className={`client-mode client-mode--${clientMode}`}><span /> {clientMode === 'wails' ? 'Wails backend' : 'Локальный preview'}</span>
        <button className="text-button" onClick={onOpenSettings} type="button">Провайдеры <Icon name="chevron-right" width={13} height={13} /></button>
      </div>
    </aside>
  )
}
