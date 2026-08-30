import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'

import type { BackendConnection } from '../lib/backend'
import { createYuriClient } from '../lib/client'
import { subscribeConversationUpdates } from '../lib/client/events'
import { readChatAttachments } from '../lib/chat-attachments'
import type {
  ApprovalRequest,
  ChatAttachmentInput,
  ChatEvent,
  ChatTool,
  ChatMessage,
  Conversation,
  MessageStatus,
  RunStatus,
} from '../lib/contracts'
import { aggregateChatEvent, buildChatTimeline, mergeStreamingMessages } from '../lib/chat-trace'
import { mapAvatarState } from '../lib/personality'
import { loadAutoSpeakPreference, saveAutoSpeakPreference } from '../lib/voice'
import { useTTS } from '../hooks/useVoice'
import { type ApprovalDecision } from './ApprovalDialog'
import { ApprovalPortal } from './ApprovalPortal'
import { ChatComposer } from './ChatComposer'
import { ChatHeader } from './ChatHeader'
import { ChatStarters } from './ChatStarters'
import {
  appendConversations,
  applyConversationTitleUpdate,
  conversationPageSize,
  conversationTailSize,
  emptyStream,
  emptyStreamMessages,
  hydrateConversation,
  makeId,
  offscreenStyle,
  rememberCommittedStreamId,
  retireEarlierHistory,
  statusCopy,
  stickToBottomThresholdPx,
  timelineWindowSize,
  timelineWindowStep,
  updateConversation,
  upsertMessage,
  type StreamBuffer,
} from './chat-view-model'
import { ConversationPanel } from './ConversationPanel'
import { Icon } from './Icon'
import { ToolAvailabilityBar } from './ToolAvailabilityBar'
import { TranscriptFeed } from './TranscriptFeed'

type ChatViewProps = {
  agentName: string
  backend: BackendConnection
  /**
   * The user is looking at another tab. The view stays mounted — a run must
   * outlive navigation — it is only taken out of the layout and out of the
   * accessibility tree (H-9).
   */
  hidden?: boolean
  /** Brings the chat back on screen from wherever the user is. */
  onOpenChat?: () => void
  onOpenSettings: () => void
}

export function ChatView({ agentName, backend, hidden = false, onOpenChat, onOpenSettings }: ChatViewProps) {
  const client = useMemo(() => createYuriClient(), [])
  const labels = useMemo(() => statusCopy(agentName), [agentName])
  const [conversations, setConversations] = useState<Conversation[]>([])
  const [selectedId, setSelectedId] = useState('')
  const [conversationFilter, setConversationFilter] = useState('')
  const [chatTools, setChatTools] = useState<ChatTool[]>([])
  const [allowedDirectories, setAllowedDirectories] = useState<string[]>([])
  const [toolsLoading, setToolsLoading] = useState(true)
  const [draft, setDraft] = useState('')
  const [attachments, setAttachments] = useState<ChatAttachmentInput[]>([])
  const [runId, setRunId] = useState<string>()
  const [runStatus, setRunStatus] = useState<RunStatus>('idle')
  const [runLabel, setRunLabel] = useState(labels.idle)
  const [pendingApproval, setPendingApproval] = useState<ApprovalRequest>()
  const [approvalBusy, setApprovalBusy] = useState(false)
  const [approvalError, setApprovalError] = useState<string>()
  const [error, setError] = useState<string>()
  const [loading, setLoading] = useState(true)
  /**
   * The sidebar holds every conversation the backend has.
   *
   * It starts true so nothing offers to load more before the first page has
   * said whether more exists. `ListConversations` used to be called with no
   * offset and answered with the newest page, and an owner past that page had
   * no way — and no indication — to reach the rest (M-10).
   */
  const [conversationsExhausted, setConversationsExhausted] = useState(true)
  const [loadingMoreConversations, setLoadingMoreConversations] = useState(false)
  const loadingMoreConversationsRef = useRef(false)
  /**
   * Conversations whose transcript has been loaded.
   *
   * The list carries metadata only, so opening a conversation is what fetches
   * its messages. Membership is claimed before the request goes out, so a
   * re-render mid-flight cannot start a second one; a failed fetch gives the
   * claim back so re-opening retries.
   *
   * A conversation that arrived from the list *with* messages is already
   * hydrated: a bridge built before this split still answers with transcripts,
   * and re-fetching one would overwrite what it sent.
   */
  const hydratedRef = useRef<Set<string>>(new Set())
  const [transcribing, setTranscribing] = useState(false)
  const [autoSpeak, setAutoSpeak] = useState(loadAutoSpeakPreference)
  const [stream, setStream] = useState<StreamBuffer>(emptyStream)
  const [capturedVoice, setCapturedVoice] = useState<Blob>()
  const [clearVoiceToken, setClearVoiceToken] = useState(0)
  const [recording, setRecording] = useState(false)
  const [followingBottom, setFollowingBottom] = useState(true)
  /**
   * The one thing a screen reader is asked to speak about the transcript: the
   * text of the answer that just finished (M-44). Empty while a run streams.
   */
  const [announcement, setAnnouncement] = useState('')
  /** Id of the answer already announced, so a re-render cannot repeat it. */
  const announcedMessageRef = useRef<string>()
  /** Conversation whose backlog has been absorbed without being read out. */
  const announcedConversationRef = useRef<string>()
  const messagesRef = useRef<HTMLDivElement>(null)
  const messagesEndRef = useRef<HTMLDivElement>(null)
  const transcribedBlobRef = useRef<Blob>()
  const autoSpeakRunRef = useRef<string>()
  const spokenAutoRunRef = useRef<string>()
  // Run id of a stream that has not seen a terminal `run.completed` yet. It is
  // the fallback used to finalize partial messages when the backend ends a run
  // without emitting one (cancellation short-circuits the emitter).
  const unfinalizedRunRef = useRef<string>()
  const autoSpeakEnabledRef = useRef(autoSpeak)
  // Mirror of the stream buffer so a flush can read and rewrite it inside one
  // event handler without waiting for a render.
  const streamRef = useRef<StreamBuffer>(emptyStream)
  // Ids that already left the buffer for `conversations`. A late delta for one
  // of them has to be appended in place instead of opening a second, duplicate
  // bubble. Bounded rather than cleared on conversation switch — see
  // `committedStreamIdLimit` for why that is the safe way to bound it (N-20).
  const committedStreamIdsRef = useRef<Set<string>>(new Set())
  const stickToBottomRef = useRef(true)
  const scrollFrameRef = useRef<number>()
  const scrollPendingRef = useRef(false)
  const tts = useTTS()
  const speakTTS = tts.speak
  const stopTTS = tts.stop
  const ttsSupported = tts.supported
  const avatarState = mapAvatarState(runStatus, recording, Boolean(tts.speakingId))

  // `onOpenSettings` is an inline arrow in the parent, so it changes identity
  // on every App render. Keeping the callback handed to memoized children
  // stable is what makes their `React.memo` worth anything.
  const openSettingsRef = useRef(onOpenSettings)
  const openSettings = useCallback(() => openSettingsRef.current(), [])
  const openChatRef = useRef(onOpenChat)
  const openChat = useCallback(() => openChatRef.current?.(), [])
  // Read by the scroll scheduler, which must not chase an off-screen container.
  const hiddenRef = useRef(hidden)

  const selectedConversation = conversations.find((conversation) => conversation.id === selectedId)
  const selectedMessages = selectedConversation?.messages
  const selectedTraces = selectedConversation?.traces
  const committedTimeline = useMemo(
    () => selectedMessages ? buildChatTimeline(selectedMessages, selectedTraces) : [],
    [selectedMessages, selectedTraces],
  )
  const streamMessages = stream.conversationId === selectedId ? stream.messages : emptyStreamMessages
  const timeline = useMemo(
    () => mergeStreamingMessages(committedTimeline, streamMessages),
    [committedTimeline, streamMessages],
  )

  /**
   * Number of entries trimmed off the *front* of the timeline.
   *
   * Counting from the front rather than keeping "the last N" is what makes the
   * window stable while an answer streams: entries are only ever appended, so a
   * new token widens the window instead of pushing the oldest visible message
   * back out of the DOM under the reader.
   */
  const [hiddenEntryCount, setHiddenEntryCount] = useState(0)
  const [windowedConversationId, setWindowedConversationId] = useState('')
  let hiddenCount = hiddenEntryCount
  if (windowedConversationId !== selectedId) {
    // Adjusting state during render (rather than in an effect) so the first
    // paint of a freshly opened conversation is already the windowed one.
    hiddenCount = Math.max(0, timeline.length - timelineWindowSize)
    setWindowedConversationId(selectedId)
    setHiddenEntryCount(hiddenCount)
  }
  const visibleEntries = useMemo(
    () => hiddenCount > 0 ? timeline.slice(hiddenCount) : timeline,
    [hiddenCount, timeline],
  )

  const visibleConversations = useMemo(() => {
    // Normalizing the query once instead of per candidate: this used to run
    // `toLocaleLowerCase` twice per conversation on every render.
    const query = conversationFilter.trim().toLocaleLowerCase('ru-RU')
    if (!query) return conversations
    return conversations.filter((conversation) => `${conversation.title} ${conversation.preview}`.toLocaleLowerCase('ru-RU').includes(query))
  }, [conversationFilter, conversations])

  // Snapshot of the state that stable callbacks need to read at call time.
  const latestRef = useRef({ conversations, selectedId, startRun: undefined as undefined | ((text: string, retryOfMessageId?: string) => Promise<void>) })

  useEffect(() => {
    openSettingsRef.current = onOpenSettings
  }, [onOpenSettings])

  useEffect(() => {
    openChatRef.current = onOpenChat
  }, [onOpenChat])

  /**
   * Marks conversations that arrived carrying their own transcript, so opening
   * one does not re-fetch what the list already sent.
   */
  const claimHydratedFromList = useCallback((page: Conversation[]) => {
    for (const conversation of page) {
      if (conversation.messages.length > 0) hydratedRef.current.add(conversation.id)
    }
  }, [])

  useEffect(() => {
    let mounted = true
    void client.listConversations({ limit: conversationPageSize }).then(async (loaded) => {
      if (!mounted) return
      const available = loaded.length > 0 ? loaded : [await client.createConversation('Новый диалог')]
      if (!mounted) return
      claimHydratedFromList(available)
      // A short first page is the whole store; a full one means the offset the
      // sidebar never used to pass has something behind it.
      setConversationsExhausted(loaded.length < conversationPageSize)
      setConversations(available)
      setSelectedId(available[0]?.id ?? '')
      setLoading(false)
    }).catch(() => {
      if (!mounted) return
      setError('Не удалось загрузить список диалогов.')
      setLoading(false)
    })
    return () => { mounted = false }
  }, [claimHydratedFromList, client])

  /**
   * Automatic titles are generated after the chat run on a separate backend
   * task. They therefore cannot use the short-lived `yuri:chat` subscription:
   * by the time the title is ready that run has already released its listener.
   * A stale generated event is also not allowed to roll back an explicit user
   * rename that happened while the background request was in flight.
   */
  useEffect(() => {
    if (typeof subscribeConversationUpdates !== 'function') return undefined
    const unsubscribe = subscribeConversationUpdates((update) => {
      setConversations((current) => updateConversation(current, update.conversationId, (conversation) => applyConversationTitleUpdate(conversation, update)))
    })
    return () => unsubscribe()
  }, [])

  /**
   * Loads the next page of the sidebar.
   *
   * Terminates on the same predicate the transcript's "показать более ранние"
   * uses (N-18): the control stays armed only when the offset actually
   * advanced, which is exactly when the page added at least one conversation
   * the sidebar did not already hold. A backend answering a full page of
   * duplicates therefore retires the control instead of arming it for a click
   * that could only re-issue the identical request.
   */
  const handleLoadMoreConversations = useCallback(() => {
    if (loadingMoreConversationsRef.current) return
    loadingMoreConversationsRef.current = true
    setLoadingMoreConversations(true)
    const offset = latestRef.current.conversations.length
    void client.listConversations({ limit: conversationPageSize, offset }).then((page) => {
      const known = new Set(latestRef.current.conversations.map((conversation) => conversation.id))
      const fresh = page.filter((conversation) => !known.has(conversation.id))
      if (fresh.length === 0) {
        setConversationsExhausted(true)
        return
      }
      if (page.length < conversationPageSize) setConversationsExhausted(true)
      claimHydratedFromList(fresh)
      setConversations((existing) => appendConversations(existing, fresh))
    }).catch(() => {
      setError('Не удалось загрузить остальные диалоги.')
    }).finally(() => {
      loadingMoreConversationsRef.current = false
      setLoadingMoreConversations(false)
    })
  }, [claimHydratedFromList, client])

  /**
   * Loads the transcript of the conversation being opened.
   *
   * The conversation list carries metadata only — a title, a preview and a
   * timestamp are all the sidebar draws — so this is where a transcript comes
   * from. It used to arrive with the list for every conversation at once, which
   * read and shipped a whole page of transcripts to render exactly one.
   *
   * Two invariants have to be restored by hand once a transcript can arrive
   * after its conversation is already on screen:
   *
   *  - H-18's DOM budget. `hiddenEntryCount` is recomputed during render only
   *    when the selected conversation changes, and this commit does not change
   *    it, so without clearing the marker the freshly loaded transcript would
   *    render in full instead of windowed.
   *  - M-44's announcement rule. The announcer treats "first sight of this
   *    conversation" as the moment to absorb the backlog silently; that moment
   *    used to be the switch itself, and is now this commit, so clearing the
   *    marker is what keeps a screen reader from reading the backlog out loud
   *    to someone who only opened the conversation.
   *
   * The commit is deliberately not guarded by a mounted/active flag: it is a
   * merge keyed by conversation id, so it is correct whenever it lands, and
   * dropping it because the reader switched away would leave the conversation
   * permanently blank while still marked hydrated.
   */
  useEffect(() => {
    if (!selectedId || hydratedRef.current.has(selectedId)) return
    hydratedRef.current.add(selectedId)
    void client.listMessages(selectedId, conversationTailSize, '').then((page) => {
      if (page.messages.length === 0 && !page.hasMore) return
      announcedConversationRef.current = ''
      setWindowedConversationId('')
      setConversations((existing) => updateConversation(existing, selectedId, (conversation) => hydrateConversation(conversation, page)))
    }).catch(() => {
      hydratedRef.current.delete(selectedId)
      setError('Не удалось загрузить историю диалога.')
    })
  }, [client, selectedId])

  useEffect(() => {
    let mounted = true
    void Promise.all([client.listChatTools(), client.getAllowedDirectories()]).then(([tools, directories]) => {
      if (!mounted) return
      setChatTools(tools)
      setAllowedDirectories(directories)
    }).catch(() => {
      // Discovery is advisory. A missing ListChatTools bridge must not block
      // the conversation itself; the bar will show the unavailable state.
      if (mounted) {
        setChatTools([])
        setAllowedDirectories([])
      }
    }).finally(() => {
      if (mounted) setToolsLoading(false)
    })
    return () => { mounted = false }
  }, [client])

  /**
   * Follow the answer without fighting the user.
   *
   * Every token used to restart a smooth `scrollIntoView`, so the animation
   * never finished and reading the history mid-run was impossible. Now the
   * scroll is coalesced into one frame, and it is skipped entirely once the
   * user has scrolled away from the bottom (M-39).
   */
  const scrollToBottom = useCallback((behavior: ScrollBehavior) => {
    messagesEndRef.current?.scrollIntoView({ behavior, block: 'end' })
  }, [])

  const requestAutoScroll = useCallback(() => {
    // Nothing to follow while the surface is off-screen; the return to the tab
    // scrolls to the newest tokens in one go instead.
    if (hiddenRef.current || !stickToBottomRef.current || scrollPendingRef.current) return
    scrollPendingRef.current = true
    const run = () => {
      scrollPendingRef.current = false
      scrollFrameRef.current = undefined
      if (stickToBottomRef.current) scrollToBottom('auto')
    }
    const handle = typeof requestAnimationFrame === 'function'
      ? requestAnimationFrame(run)
      : (setTimeout(run, 16) as unknown as number)
    // A synchronous scheduler would already have run the callback, and storing
    // the handle then would leave a frame that is never cleared.
    if (scrollPendingRef.current) scrollFrameRef.current = handle
  }, [scrollToBottom])

  const handleMessagesScroll = useCallback(() => {
    const node = messagesRef.current
    if (!node) return
    const distance = node.scrollHeight - node.scrollTop - node.clientHeight
    const stick = distance <= stickToBottomThresholdPx
    if (stickToBottomRef.current === stick) return
    stickToBottomRef.current = stick
    setFollowingBottom(stick)
  }, [])

  const handleJumpToBottom = useCallback(() => {
    stickToBottomRef.current = true
    setFollowingBottom(true)
    scrollToBottom('smooth')
  }, [scrollToBottom])

  /**
   * Uncover the previous window of history.
   *
   * The scroll container grows upwards, which would otherwise throw the reader
   * an unpredictable distance down the transcript, so the entry they were
   * looking at is pinned by the height the reveal added.
   *
   * Two sources of history, in this order: whatever is already in state (the
   * window is a render bound, so uncovering it is free), and then the previous
   * page from the backend once the local history runs out. `ListConversations`
   * only returns the newest slice of a transcript, so the second source is what
   * makes a long conversation readable at all.
   */
  const revealAnchorRef = useRef<{ scrollHeight: number; scrollTop: number }>()
  /**
   * Bumped by every reveal, local or fetched. The anchoring effect is keyed on
   * it rather than on `hiddenEntryCount`, because a fetched page uncovers
   * history without changing the hidden count at all — that path would
   * otherwise commit new nodes above the reader with no re-pin.
   */
  const [revealTick, setRevealTick] = useState(0)
  const [fetchingEarlier, setFetchingEarlier] = useState(false)
  const fetchingEarlierRef = useRef(false)
  // `hiddenCount` is derived during render, so it is read through a ref rather
  // than closed over: a stable callback would otherwise branch on the count of
  // whichever render created it.
  const hiddenCountRef = useRef(hiddenCount)
  useEffect(() => {
    hiddenCountRef.current = hiddenCount
  })

  const handleShowEarlier = useCallback(() => {
    const node = messagesRef.current
    if (node) revealAnchorRef.current = { scrollHeight: node.scrollHeight, scrollTop: node.scrollTop }
    if (hiddenCountRef.current > 0) {
      setHiddenEntryCount((current) => Math.max(0, current - timelineWindowStep))
      setRevealTick((tick) => tick + 1)
      return
    }
    const { conversations: current, selectedId: currentId } = latestRef.current
    const conversation = current.find((candidate) => candidate.id === currentId)
    if (!conversation?.hasMoreMessages || fetchingEarlierRef.current) {
      revealAnchorRef.current = undefined
      return
    }
    fetchingEarlierRef.current = true
    setFetchingEarlier(true)
    // The cursor is the oldest message actually held, so the page the backend
    // returns abuts it exactly: nothing between the two pages is skipped, and
    // the cursor message itself never comes back a second time.
    const cursor = conversation.messages[0]?.id ?? ''
    void client.listMessages(currentId, timelineWindowStep, cursor).then((page) => {
      const known = new Set(conversation.messages.map((message) => message.id))
      const fresh = page.messages.filter((message) => !known.has(message.id))
      if (fresh.length === 0) {
        // `page.hasMore` is deliberately NOT written back here (N-18).
        //
        // The next request is a pure function of (conversation, limit, cursor),
        // and the cursor is `messages[0].id` — which this branch leaves exactly
        // where it was. Believing a `hasMore: true` would therefore keep the
        // control armed for a click that can only re-issue the identical
        // request and land here again: an unbounded loop, with the spinner
        // flashing on every click and nothing ever appearing.
        //
        // The invariant this restores is that the control stays armed only when
        // the cursor advanced. `fresh` is disjoint from `known`, so the cursor
        // moves iff `fresh.length > 0` — the same predicate that now guards
        // writing `hasMore` back. Termination follows: every fetch that leaves
        // the control armed also adds at least one message, so a conversation
        // of N messages admits at most N fetches whatever the backend answers.
        revealAnchorRef.current = undefined
        setConversations((existing) => updateConversation(existing, currentId, retireEarlierHistory))
        return
      }
      // Prepending history is not new output. The autoscroll effect is keyed on
      // the whole timeline — deliberately, so that uncovering local history
      // cannot be mistaken for a token — and a fetched page does change the
      // timeline, so this one commit is exempted explicitly instead.
      skipAutoScrollRef.current = true
      // The same fold the initial load uses. Reaching this line at all is the
      // N-18 predicate — the branch above owns the empty case and must not
      // write `hasMore` back — so writing it here is exactly "the cursor
      // advanced, so believe the backend about what is left".
      setConversations((existing) => updateConversation(existing, currentId, (candidate) => hydrateConversation(candidate, page)))
      setRevealTick((tick) => tick + 1)
    }).catch(() => {
      revealAnchorRef.current = undefined
      setError('Не удалось загрузить более раннюю историю.')
    }).finally(() => {
      fetchingEarlierRef.current = false
      setFetchingEarlier(false)
    })
  }, [client])

  useLayoutEffect(() => {
    const anchor = revealAnchorRef.current
    if (!anchor) return
    revealAnchorRef.current = undefined
    const node = messagesRef.current
    if (!node) return
    node.scrollTop = anchor.scrollTop + (node.scrollHeight - anchor.scrollHeight)
  }, [revealTick])

  // Deliberately keyed on the whole timeline, not on the visible window:
  // revealing older messages must not count as new output and yank the view
  // back down to the newest token.
  const skipAutoScrollRef = useRef(false)
  useEffect(() => {
    if (skipAutoScrollRef.current) {
      skipAutoScrollRef.current = false
      return
    }
    requestAutoScroll()
  }, [requestAutoScroll, timeline])

  // Coming back from another tab lands on whatever the run produced meanwhile.
  useEffect(() => {
    hiddenRef.current = hidden
    if (!hidden) requestAutoScroll()
  }, [hidden, requestAutoScroll])

  useEffect(() => () => {
    if (scrollFrameRef.current === undefined) return
    if (typeof cancelAnimationFrame === 'function') cancelAnimationFrame(scrollFrameRef.current)
    else clearTimeout(scrollFrameRef.current)
  }, [])

  // Opening another conversation is an explicit jump to its end.
  useEffect(() => {
    stickToBottomRef.current = true
    setFollowingBottom(true)
  }, [selectedId])

  useEffect(() => {
    const blob = capturedVoice
    if (!blob || transcribedBlobRef.current === blob) return
    transcribedBlobRef.current = blob
    let active = true
    setTranscribing(true)
    setError(undefined)
    void client.transcribeAudio(blob).then((text) => {
      if (!active) return
      setDraft(text)
      setCapturedVoice(undefined)
      setClearVoiceToken((current) => current + 1)
    }).catch((cause) => {
      if (active) setError(cause instanceof Error ? cause.message : 'Не удалось распознать голос.')
    }).finally(() => {
      if (active) setTranscribing(false)
    })
    return () => { active = false }
  }, [capturedVoice, client])

  useEffect(() => {
    autoSpeakEnabledRef.current = autoSpeak
  }, [autoSpeak])

  useEffect(() => {
    if (!autoSpeak || !ttsSupported) return
    const runId = autoSpeakRunRef.current
    if (!runId) return
    const message = [...(selectedMessages ?? [])]
      .reverse()
      .find((candidate) => candidate.role === 'assistant' && candidate.status === 'complete' && candidate.runId === runId && candidate.content.trim())
    if (!message || spokenAutoRunRef.current === runId) return
    spokenAutoRunRef.current = runId
    autoSpeakRunRef.current = undefined
    speakTTS(message.id, message.content)
  }, [autoSpeak, selectedMessages, speakTTS, ttsSupported])

  /**
   * Announce a finished answer once (M-44).
   *
   * Driven by committed messages rather than by the event handler: a streaming
   * answer lives in the stream buffer with status `streaming` and only reaches
   * `messages` as `complete`, so a delta cannot reach the live region however
   * the run ends. A cancelled or failed answer lands as `cancelled`/`error`
   * and is reported by the run-state indicator instead of being read out.
   */
  useEffect(() => {
    if (!selectedMessages) return
    let latest: ChatMessage | undefined
    for (let index = selectedMessages.length - 1; index >= 0; index -= 1) {
      const candidate = selectedMessages[index]
      if (candidate.role === 'assistant' && candidate.status === 'complete' && candidate.content.trim()) {
        latest = candidate
        break
      }
    }
    if (announcedConversationRef.current !== selectedId) {
      // Opening (or switching to) a conversation must not read its backlog out
      // loud — the last answer is already on screen, it is not news.
      announcedConversationRef.current = selectedId
      announcedMessageRef.current = latest?.id
      setAnnouncement('')
      return
    }
    if (!latest || announcedMessageRef.current === latest.id) return
    announcedMessageRef.current = latest.id
    // Named speaker: the reader is no longer inside the transcript, so the
    // utterance has to say whose answer it is on its own.
    setAnnouncement(`${agentName}: ${latest.content.trim()}`)
  }, [agentName, selectedId, selectedMessages])

  const setStreamBuffer = useCallback((next: StreamBuffer) => {
    streamRef.current = next
    setStream(next)
  }, [])

  /**
   * Move buffered messages of a finished run into the durable conversation.
   * `messageId` narrows the flush to a single answer (`assistant.completed`);
   * without it the whole run is finalized.
   */
  const flushStream = useCallback((conversationId: string, targetRunId: string | undefined, status: MessageStatus, messageId?: string) => {
    const buffer = streamRef.current
    if (buffer.conversationId !== conversationId || buffer.messages.length === 0) return
    const flushed: ChatMessage[] = []
    const remaining: ChatMessage[] = []
    for (const message of buffer.messages) {
      const matchesRun = !targetRunId || !message.runId || message.runId === targetRunId
      const matchesMessage = !messageId || message.id === messageId
      if (matchesRun && matchesMessage) flushed.push({ ...message, status })
      else remaining.push(message)
    }
    if (flushed.length === 0) return
    for (const message of flushed) rememberCommittedStreamId(committedStreamIdsRef.current, message.id)
    setStreamBuffer({ conversationId, messages: remaining })
    setConversations((current) => updateConversation(current, conversationId, (conversation) => {
      let messages = conversation.messages
      for (const message of flushed) messages = upsertMessage(messages, message)
      return { ...conversation, updatedAt: new Date().toISOString(), messages }
    }))
  }, [setStreamBuffer])

  /**
   * Moves every still-streaming message of a run out of the `streaming` state.
   * Without this a cancelled run leaves a permanent typing indicator and hides
   * the actions ("Повторить" / "Слушать") exactly where they are needed most.
   */
  const finalizeStreamingMessages = useCallback((conversationId: string, targetRunId: string | undefined, status: MessageStatus) => {
    flushStream(conversationId, targetRunId, status)
    setConversations((current) => updateConversation(current, conversationId, (conversation) => {
      let changed = false
      const messages = conversation.messages.map((message) => {
        if (message.status !== 'streaming') return message
        if (targetRunId && message.runId && message.runId !== targetRunId) return message
        changed = true
        return { ...message, status }
      })
      return changed ? { ...conversation, messages } : conversation
    }))
  }, [flushStream])

  const appendStreamDelta = useCallback((conversationId: string, messageId: string, eventRunId: string | undefined, delta: string) => {
    const buffer = streamRef.current
    const messages = buffer.conversationId === conversationId ? buffer.messages : emptyStreamMessages
    const index = messages.findIndex((message) => message.id === messageId)
    if (index === -1) {
      const message: ChatMessage = {
        id: messageId,
        role: 'assistant',
        content: delta,
        status: 'streaming',
        createdAt: new Date().toISOString(),
        runId: eventRunId,
      }
      setStreamBuffer({ conversationId, messages: [...messages, message] })
      return
    }
    setStreamBuffer({
      conversationId,
      messages: messages.map((message, candidate) => candidate === index
        ? { ...message, content: message.content + delta, status: 'streaming' }
        : message),
    })
  }, [setStreamBuffer])

  const handleEvent = useCallback((conversationId: string, event: ChatEvent) => {
    if (event.conversationId && event.conversationId !== conversationId) return

    if ('runId' in event && event.runId) unfinalizedRunRef.current = event.runId

    // Keep one operational timeline per run. Assistant output events are
    // excluded: they belong to the visible answer and must never become a
    // reasoning log.
    if (event.type !== 'assistant.delta' && event.type !== 'assistant.completed') {
      setConversations((current) => updateConversation(current, conversationId, (conversation) => ({
        ...conversation,
        traces: aggregateChatEvent(
          conversation.traces ?? [],
          event,
          event.createdAt ?? event.timestamp ?? new Date().toISOString(),
        ),
      })))
    }

    if (event.type === 'run.started') {
      setRunId(event.runId)
      setRunStatus('thinking')
      setRunLabel(labels.thinking)
      return
    }

    if (event.type === 'run.status') {
      setRunId(event.runId)
      setRunStatus(event.status)
      setRunLabel(event.label || labels[event.status])
      return
    }

    if (event.type === 'assistant.delta') {
      const known = committedStreamIdsRef.current.has(event.messageId)
        || latestRef.current.conversations.some((conversation) => conversation.id === conversationId
          && conversation.messages.some((message) => message.id === event.messageId))
      if (!known) {
        appendStreamDelta(conversationId, event.messageId, event.runId, event.delta)
        return
      }
      // The answer already reached the durable state, so it has to keep growing
      // there rather than reappearing as a second bubble.
      setConversations((current) => updateConversation(current, conversationId, (conversation) => ({
        ...conversation,
        updatedAt: new Date().toISOString(),
        messages: conversation.messages.map((message) => message.id === event.messageId
          ? { ...message, content: message.content + event.delta, status: 'streaming' as const }
          : message),
      })))
      return
    }

    if (event.type === 'assistant.completed') {
      flushStream(conversationId, event.runId, 'complete', event.messageId)
      setConversations((current) => updateConversation(current, conversationId, (conversation) => {
        let changed = false
        const messages = conversation.messages.map((message) => {
          if (message.id !== event.messageId || message.status === 'complete') return message
          changed = true
          return { ...message, status: 'complete' as const }
        })
        return changed ? { ...conversation, messages } : conversation
      }))
      return
    }

    if (event.type === 'tool.started') {
      setRunStatus('tool_running')
      setRunLabel(`Инструмент: ${event.toolCall.name}`)
      return
    }

    if (event.type === 'tool.updated') {
      return
    }

    if (event.type === 'approval.required') {
      setPendingApproval(event.approval)
      setApprovalError(undefined)
      setRunStatus('waiting_approval')
      setRunLabel(labels.waiting_approval)
      return
    }

    if (event.type === 'run.completed') {
      // The backend never emits `assistant.completed` for a cancelled run, so
      // the terminal run event is the only place the partial answer can be
      // finalized.
      finalizeStreamingMessages(
        conversationId,
        event.runId,
        event.status === 'complete' ? 'complete' : event.status === 'cancelled' ? 'cancelled' : 'error',
      )
      unfinalizedRunRef.current = undefined
      setRunId(undefined)
      setPendingApproval(undefined)
      setApprovalError(undefined)
      setRunStatus(event.status === 'complete' ? 'idle' : event.status)
      setRunLabel(event.status === 'complete' ? labels.idle : event.status === 'cancelled' ? labels.cancelled : event.error ?? labels.error)
      if (event.status === 'error') setError(event.error ?? 'Запуск завершился ошибкой.')
      if (event.status === 'complete' && autoSpeakEnabledRef.current) autoSpeakRunRef.current = event.runId
      else autoSpeakRunRef.current = undefined
    }
  }, [appendStreamDelta, finalizeStreamingMessages, flushStream, labels])

  const startRun = useCallback(async (text: string, retryOfMessageId?: string) => {
    const trimmed = text.trim()
    const outgoingAttachments = retryOfMessageId ? [] : attachments
    if ((!trimmed && outgoingAttachments.length === 0) || !selectedId || runId) return
    setError(undefined)
    const userMessage: ChatMessage = {
      id: makeId('user'),
      role: 'user',
      content: trimmed,
      status: 'complete',
      createdAt: new Date().toISOString(),
      attachments: outgoingAttachments.map(({ dataBase64: _dataBase64, ...attachment }) => ({ ...attachment })),
    }
    if (!retryOfMessageId) {
      setConversations((current) => updateConversation(current, selectedId, (conversation) => ({
        ...conversation,
        preview: trimmed || outgoingAttachments.map((attachment) => attachment.name).join(', '),
        updatedAt: new Date().toISOString(),
        messages: [...conversation.messages, userMessage],
      })))
    }
    setDraft('')
    if (!retryOfMessageId) setAttachments([])
    setRunStatus('thinking')
    setRunLabel(labels.thinking)
    try {
      await (retryOfMessageId
        ? client.retryLast({ conversationId: selectedId, text: trimmed, retryOfMessageId }, (event) => handleEvent(selectedId, event))
        : client.sendMessage({
            conversationId: selectedId,
            text: trimmed,
            attachments: outgoingAttachments.map(({ previewDataUrl: _previewDataUrl, ...attachment }) => attachment),
          }, (event) => handleEvent(selectedId, event)))
      // Settling the bridge promise is the last signal we get. If the run ended
      // without a terminal event, treat the partial answer as interrupted
      // rather than leaving it streaming forever.
      const orphan = unfinalizedRunRef.current
      if (orphan) {
        finalizeStreamingMessages(selectedId, orphan, 'cancelled')
        unfinalizedRunRef.current = undefined
        setRunId(undefined)
        setPendingApproval(undefined)
        setApprovalError(undefined)
        setRunStatus('cancelled')
        setRunLabel(labels.cancelled)
      }
    } catch (cause) {
      const orphan = unfinalizedRunRef.current
      finalizeStreamingMessages(selectedId, orphan, 'error')
      unfinalizedRunRef.current = undefined
      setRunId(undefined)
      // The run is gone, so nothing can answer an approval any more: never
      // leave the modal up over a dead run.
      setPendingApproval(undefined)
      setApprovalError(undefined)
      setRunStatus('error')
      setRunLabel(labels.error)
      setError(cause instanceof Error ? cause.message : 'Не удалось отправить сообщение.')
    }
  }, [attachments, client, finalizeStreamingMessages, handleEvent, labels, runId, selectedId])

  const handleRenameConversation = useCallback(async (title: string) => {
    const conversationId = selectedId
    if (!conversationId) return
    const renamed = await client.renameConversation(conversationId, title)
    setConversations((current) => updateConversation(current, conversationId, (conversation) => ({
      ...conversation,
      title: renamed?.title ?? title,
      titleSource: renamed?.titleSource ?? 'user',
      updatedAt: renamed?.updatedAt ?? new Date().toISOString(),
    })))
  }, [client, selectedId])

  useEffect(() => {
    latestRef.current = { conversations, selectedId, startRun }
  })

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    void startRun(draft)
  }

  const handleDraftKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
      event.preventDefault()
      void startRun(draft)
    }
  }

  const handleNewConversation = () => {
    void client.createConversation('Новый диалог').then((conversation) => {
      // Created empty and known to be empty, so opening it must not spend a
      // round-trip asking the backend for a transcript that cannot exist.
      hydratedRef.current.add(conversation.id)
      setConversations((current) => [conversation, ...current])
      setSelectedId(conversation.id)
      setDraft('')
      setAttachments([])
      setError(undefined)
    }).catch(() => setError('Не удалось создать новый диалог.'))
  }

  const handleApproval = async (decision: ApprovalDecision) => {
    if (!pendingApproval || approvalBusy) return
    setApprovalBusy(true)
    setApprovalError(undefined)
    try {
      await client.approve(pendingApproval.id, decision)
      setPendingApproval(undefined)
      setApprovalError(undefined)
    } catch (cause) {
      // The decision never reached the runtime. Report it inside the dialog and
      // keep both buttons live so the user can retry or close.
      setApprovalError(cause instanceof Error ? cause.message : 'Не удалось передать решение агенту.')
    } finally {
      setApprovalBusy(false)
    }
  }

  const handleApprovalDismiss = () => {
    setPendingApproval(undefined)
    setApprovalError(undefined)
    setError('Решение по подтверждению не было передано агенту. Действие осталось неподтверждённым — остановите запуск или попробуйте снова.')
    // That explanation, and the stop button next to it, only exist inside the
    // chat: closing the dialog from another tab must not hide both.
    if (hidden) openChat()
  }

  const runIdRef = useRef(runId)
  useEffect(() => {
    runIdRef.current = runId
  }, [runId])

  const handleCancel = useCallback(async () => {
    const activeRunId = runIdRef.current
    if (!activeRunId) return
    setRunLabel('Останавливаем запуск…')
    try {
      await client.cancelRun(activeRunId)
    } catch (cause) {
      // Never leave the composer stuck on "Останавливаем запуск…": the run is
      // still alive, so restore the label the current status implies.
      setRunLabel(labels[runStatus])
      setError(cause instanceof Error ? cause.message : 'Не удалось остановить запуск.')
    }
  }, [client, labels, runStatus])

  const handleRetry = useCallback((messageId: string) => {
    const { conversations: current, selectedId: currentId, startRun: run } = latestRef.current
    const conversation = current.find((candidate) => candidate.id === currentId)
    if (!conversation || !run) return
    const previousUser = conversation.messages
      .slice(0, conversation.messages.findIndex((candidate) => candidate.id === messageId))
      .reverse()
      .find((candidate) => candidate.role === 'user')
    if (previousUser) void run(previousUser.content, messageId)
  }, [])

  const toggleAutoSpeak = useCallback(() => {
    setAutoSpeak((current) => {
      const next = !current
      autoSpeakEnabledRef.current = next
      if (!next) autoSpeakRunRef.current = undefined
      saveAutoSpeakPreference(next)
      return next
    })
  }, [])

  const handleCancelClick = useCallback(() => { void handleCancel() }, [handleCancel])
  const handleSelectConversation = useCallback((conversationId: string) => setSelectedId(conversationId), [])
  const handleCaptureVoice = useCallback((blob: Blob) => setCapturedVoice(blob), [])
  const handleSelectAttachments = useCallback((files: FileList) => {
    void readChatAttachments(files, attachments).then((selected) => {
      setAttachments((current) => [...current, ...selected])
      setError(undefined)
    }).catch((cause) => setError(cause instanceof Error ? cause.message : 'Не удалось прикрепить файл.'))
  }, [attachments])
  const handleRemoveAttachment = useCallback((attachmentId: string) => {
    setAttachments((current) => current.filter((attachment) => attachment.id !== attachmentId))
  }, [])
  const loadAttachment = useCallback((messageId: string, attachmentId: string) => client.getChatAttachment(messageId, attachmentId), [client])
  const handleOpenExternalURL = useCallback((url: string) => {
    void client.openExternalURL(url).catch((cause) => {
      setError(cause instanceof Error ? cause.message : 'Не удалось открыть ссылку.')
    })
  }, [client])
  const handleOpenLocalPath = useCallback((path: string) => {
    void client.openLocalPath(path).catch((cause) => {
      setError(cause instanceof Error ? cause.message : 'Не удалось открыть локальный путь.')
    })
  }, [client])
  const running = Boolean(runId)

  return (
    <div className="chat-view chat-view--workspace" hidden={hidden} style={hidden ? offscreenStyle : undefined}>
      <div className="ambient-glow ambient-glow--one" />
      <div className="ambient-glow ambient-glow--two" />
      <div className="chat-layout">
        <ConversationPanel
          clientMode={client.mode}
          conversations={visibleConversations}
          filter={conversationFilter}
          hasMoreConversations={!conversationsExhausted}
          loading={loading}
          loadingMore={loadingMoreConversations}
          onFilterChange={setConversationFilter}
          onLoadMore={handleLoadMoreConversations}
          onNewConversation={handleNewConversation}
          onOpenSettings={openSettings}
          onSelect={handleSelectConversation}
          selectedId={selectedId}
        />

        <section aria-label="Текущий диалог" className="chat-main">
          <ChatHeader
            agentName={agentName}
            avatarState={avatarState}
            key={selectedId}
            onRename={handleRenameConversation}
            runLabel={runLabel}
            runStatus={runStatus}
            title={selectedConversation?.title ?? 'Новый диалог'}
          />

          <ToolAvailabilityBar allowedDirectories={allowedDirectories} loading={toolsLoading} onOpenSettings={openSettings} tools={chatTools} />

          {/*
            * Deliberately not a live region (M-44). `role="log"` carries an
            * implicit `aria-live="polite"`, so it has to go along with the
            * attribute: while it was here, every token appended to a streaming
            * answer was a separate announcement that interrupted the previous
            * one, and the `waiting_approval` status change was lost in it. The
            * finished answer is announced once by `chatAnnouncer` below, and
            * the run state by the `role="status"` indicator in the header.
            */}
          <TranscriptFeed
            agentName={agentName}
            entries={visibleEntries}
            fetchingEarlier={fetchingEarlier}
            followingBottom={followingBottom}
            hasMoreMessages={Boolean(selectedConversation?.hasMoreMessages)}
            hiddenCount={hiddenCount}
            loadAttachment={loadAttachment}
            messagesEndRef={messagesEndRef}
            messagesRef={messagesRef}
            onJumpToBottom={handleJumpToBottom}
            onOpenExternalURL={handleOpenExternalURL}
            onOpenLocalPath={handleOpenLocalPath}
            onRetry={handleRetry}
            onScroll={handleMessagesScroll}
            onShowEarlier={handleShowEarlier}
            onSpeak={speakTTS}
            onStopSpeaking={stopTTS}
            speakingId={tts.speakingId}
            speechSupported={ttsSupported}
            timelineLength={timeline.length}
          />

          {/*
            * The only live region over the transcript (M-44). It is outside
            * `.messages` on purpose: nothing that streams can mutate it, so it
            * speaks a whole answer once instead of a run of fragments.
            * `aria-atomic` makes the reader deliver the answer as one utterance.
            */}
          <p aria-atomic="true" aria-live="polite" className="sr-only" data-testid="chat-announcer">
            {announcement}
          </p>

          {error && (
            <div className="chat-error" role="alert">
              <Icon name="warning" width={15} height={15} />
              <span>{error}</span>
              <button aria-label="Закрыть уведомление об ошибке" className="icon-button icon-button--small" onClick={() => setError(undefined)} type="button"><Icon name="x" width={14} height={14} /></button>
            </div>
          )}

          <ChatComposer
            agentName={agentName}
            attachments={attachments}
            autoSpeak={autoSpeak}
            clearVoiceToken={clearVoiceToken}
            connected={backend.status === 'connected'}
            draft={draft}
            onBeforeRecord={stopTTS}
            onCancel={handleCancelClick}
            onCaptureVoice={handleCaptureVoice}
            onDraftChange={setDraft}
            onDraftKeyDown={handleDraftKeyDown}
            onOpenSettings={openSettings}
            onRecordingChange={setRecording}
            onRemoveAttachment={handleRemoveAttachment}
            onSelectAttachments={handleSelectAttachments}
            onSubmit={handleSubmit}
            onToggleAutoSpeak={toggleAutoSpeak}
            running={running}
            speechSupported={ttsSupported}
            transcribing={transcribing}
          />
        </section>
      </div>
      <ChatStarters agentName={agentName} onSelect={setDraft} />
      <ApprovalPortal
        approval={pendingApproval}
        away={hidden}
        busy={approvalBusy}
        error={approvalError}
        onDecision={(decision) => void handleApproval(decision)}
        onDismiss={handleApprovalDismiss}
        onOpenChat={openChat}
      />
    </div>
  )
}
