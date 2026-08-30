import type { RefObject, UIEvent } from 'react'

import type { ChatTimelineEntry } from '../lib/chat-trace'
import { ChatTimeline } from './ChatTimeline'
import { Icon } from './Icon'
import { YuriAvatar } from './YuriAvatar'

type TranscriptFeedProps = {
  agentName: string
  /** The windowed slice actually put in the DOM (H-18). */
  entries: ChatTimelineEntry[]
  /** True while the previous page is in flight (M-35). */
  fetchingEarlier: boolean
  followingBottom: boolean
  /** The backend still holds transcript older than the oldest message in state. */
  hasMoreMessages: boolean
  /** Entries trimmed off the front of the timeline, never off the end. */
  hiddenCount: number
  messagesEndRef: RefObject<HTMLDivElement>
  messagesRef: RefObject<HTMLDivElement>
  onJumpToBottom: () => void
  onRetry: (messageId: string) => void
  onScroll: (event: UIEvent<HTMLDivElement>) => void
  onShowEarlier: () => void
  onSpeak: (messageId: string, text: string) => void
  onStopSpeaking: () => void
  speakingId?: string
  speechSupported: boolean
  /** Length of the whole timeline, not of `entries`: the empty state is about the conversation. */
  timelineLength: number
}

export function TranscriptFeed({
  agentName,
  entries,
  fetchingEarlier,
  followingBottom,
  hasMoreMessages,
  hiddenCount,
  messagesEndRef,
  messagesRef,
  onJumpToBottom,
  onRetry,
  onScroll,
  onShowEarlier,
  onSpeak,
  onStopSpeaking,
  speakingId,
  speechSupported,
  timelineLength,
}: TranscriptFeedProps) {
  return (
    <div className="messages" onScroll={onScroll} ref={messagesRef}>
      {timelineLength === 0 && (
        <div className="empty-conversation">
          <YuriAvatar label={`${agentName} · можно начинать диалог`} size="md" state="idle" />
          <h3>С чего начнём?</h3>
          <p>Напишите агенту {agentName} задачу. Ответ появится потоково, а рискованные действия будут показаны до выполнения.</p>
        </div>
      )}
      {(hiddenCount > 0 || hasMoreMessages) && (
        <button
          className="text-button conversation-earlier"
          disabled={fetchingEarlier}
          onClick={onShowEarlier}
          type="button"
        >
          <Icon height={13} name="chevron-right" style={{ transform: 'rotate(-90deg)' }} width={13} />
          {hiddenCount > 0 ? `Показать более ранние (${hiddenCount})` : 'Показать более ранние'}
        </button>
      )}
      <ChatTimeline
        agentName={agentName}
        entries={entries}
        onRetry={onRetry}
        onSpeak={onSpeak}
        onStopSpeaking={onStopSpeaking}
        speakingId={speakingId}
        speechSupported={speechSupported}
      />
      {!followingBottom && (
        <button
          className="text-button"
          onClick={onJumpToBottom}
          style={{ position: 'sticky', bottom: 0, alignSelf: 'center', zIndex: 1 }}
          type="button"
        >
          К последним сообщениям <Icon height={13} name="chevron-right" style={{ transform: 'rotate(90deg)' }} width={13} />
        </button>
      )}
      <div ref={messagesEndRef} />
    </div>
  )
}
