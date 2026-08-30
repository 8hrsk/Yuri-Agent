import type { ConversationTitleSource, PersonalitySnapshot, YuriNotification, YuriNotificationType } from '../contracts'
import { normalizePersonalitySnapshot } from '../personality'
import { subscribeRuntimeEvent } from './bridge'
import { normalizeBoolean, nowIso, optionalString } from './primitives'
import type { UnknownRecord } from './primitives'

/** Payload of `yuri:memory`; Go reports how many records the turn rewrote. */
export interface MemoryUpdateEvent {
  type: string
  /** Number of memory records created, updated or decayed by the last turn. */
  writes: number
}

function normalizeMemoryUpdateEvent(value: unknown): MemoryUpdateEvent {
  const source = value && typeof value === 'object' ? value as UnknownRecord : {}
  const nested = source.data && typeof source.data === 'object' ? source.data as UnknownRecord : source
  const writes = Number(nested.writes ?? nested.count ?? 0)
  return {
    type: optionalString(nested, 'type', 'eventType', 'event_type') ?? 'memory.updated',
    writes: Number.isFinite(writes) && writes > 0 ? writes : 0,
  }
}

/**
 * The payload is forwarded instead of discarded so a subscriber can decide
 * whether a change is worth a full `ListMemories` round trip.
 */
export function subscribeMemoryUpdates(callback: (update: MemoryUpdateEvent) => void): () => void {
  return subscribeRuntimeEvent('yuri:memory', (value) => {
    callback(normalizeMemoryUpdateEvent(value))
  }) ?? (() => undefined)
}

/** Reflection emits a fresh, already-versioned snapshot; the renderer never mutates it locally. */
export function subscribePersonaUpdates(callback: (snapshot: PersonalitySnapshot) => void): () => void {
  const cleanups = ['yuri:persona', 'yuri:personality', 'yuri:relationship'].map((eventName) => subscribeRuntimeEvent(eventName, (value) => {
    callback(normalizePersonalitySnapshot(value))
  })).filter((cleanup): cleanup is () => void => Boolean(cleanup))
  return () => cleanups.forEach((cleanup) => cleanup())
}

export const subscribePersonalityUpdates = subscribePersonaUpdates

/**
 * A title is generated after the first turn in a background request, so it
 * uses its own event channel instead of the short-lived chat-run stream.
 * Keeping this payload small also lets the sidebar update without reloading a
 * transcript or any memory data.
 */
export interface ConversationTitleUpdateEvent {
  type: string
  conversationId: string
  title: string
  titleSource: ConversationTitleSource
  updatedAt?: string
}

function normalizeConversationTitleSource(value: unknown): ConversationTitleSource {
  const source = String(value ?? '').trim().toLowerCase()
  return source === 'generated' || source === 'user' ? source : 'default'
}

export function normalizeConversationTitleUpdate(value: unknown): ConversationTitleUpdateEvent | undefined {
  if (!value || typeof value !== 'object') return undefined
  const rawValue = value as UnknownRecord
  const envelope = rawValue.data && typeof rawValue.data === 'object' ? rawValue.data as UnknownRecord : rawValue
  const source = envelope.conversation && typeof envelope.conversation === 'object'
    ? envelope.conversation as UnknownRecord
    : envelope
  const conversationId = optionalString(source, 'conversationId', 'conversation_id', 'id')
  const title = optionalString(source, 'title', 'name')
  if (!conversationId || !title) return undefined
  return {
    type: optionalString(rawValue, 'type', 'eventType', 'event_type')
      ?? optionalString(envelope, 'type', 'eventType', 'event_type')
      ?? 'conversation.title.updated',
    conversationId,
    title,
    titleSource: normalizeConversationTitleSource(source.titleSource ?? source.title_source),
    updatedAt: optionalString(source, 'updatedAt', 'updated_at', 'timestamp'),
  }
}

export function subscribeConversationUpdates(callback: (update: ConversationTitleUpdateEvent) => void): () => void {
  return subscribeRuntimeEvent('yuri:conversation', (value) => {
    const update = normalizeConversationTitleUpdate(value)
    if (update) callback(update)
  }) ?? (() => undefined)
}

function normalizeNotificationType(value: unknown): YuriNotificationType {
  const type = String(value ?? '').toLowerCase().replace(/[-\s]/g, '_')
  if (type === 'task.completed' || type === 'task_complete') return 'task.completed'
  if (type === 'background.completed' || type === 'background_complete') return 'background.completed'
  if (type === 'plugin.event' || type === 'plugin') return 'plugin.event'
  if (type === 'rule.triggered' || type === 'rule') return 'rule.triggered'
  if (type === 'agent.message' || type === 'message') return 'agent.message'
  return 'unknown'
}

function normalizeNotification(value: unknown): YuriNotification | undefined {
  if (!value || typeof value !== 'object') return undefined
  const rawValue = value as UnknownRecord
  const envelope = rawValue.data && typeof rawValue.data === 'object' ? rawValue.data as UnknownRecord : rawValue
  const source = envelope.notification && typeof envelope.notification === 'object' ? envelope.notification as UnknownRecord : envelope
  const id = optionalString(source, 'id', 'notificationId', 'notification_id', 'eventId', 'event_id')
  const title = optionalString(source, 'title', 'subject')
  const body = optionalString(source, 'body', 'message', 'detail', 'text')
  if (!id || !title || !body) return undefined
  const permissionValue = optionalString(source, 'permission', 'nativePermission', 'native_permission')
  const permission = permissionValue === 'default' || permissionValue === 'granted' || permissionValue === 'denied'
    ? permissionValue
    : undefined
  return {
    id,
    type: normalizeNotificationType(source.type ?? source.notificationType ?? source.notification_type),
    title,
    body,
    createdAt: optionalString(source, 'createdAt', 'created_at', 'timestamp') ?? nowIso(),
    allowNative: normalizeBoolean(source.allowNative ?? source.allow_native, false),
    permission,
    conversationId: optionalString(source, 'conversationId', 'conversation_id'),
    deepLink: optionalString(source, 'deepLink', 'deep_link'),
  }
}

export function subscribeNotifications(callback: (notification: YuriNotification) => void): () => void {
  return subscribeRuntimeEvent('yuri:notification', (value) => {
    const notification = normalizeNotification(value)
    if (notification) callback(notification)
  }) ?? (() => undefined)
}

export function canUseNativeNotification(notification: YuriNotification): boolean {
  return notification.allowNative
    && typeof Notification !== 'undefined'
    && Notification.permission === 'granted'
    && (notification.permission === undefined || notification.permission === 'granted')
}

/**
 * This helper never requests permission on its own. Call it only from an
 * explicit user gesture, such as enabling local notifications in Activity.
 */
export async function requestBrowserNotificationPermission(): Promise<NotificationPermission | undefined> {
  if (typeof Notification === 'undefined') return undefined
  if (Notification.permission !== 'default') return Notification.permission
  return Notification.requestPermission()
}
