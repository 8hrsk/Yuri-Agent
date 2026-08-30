export {
  clonePersonalitySnapshot,
  createStarterPersonalitySnapshot,
  defaultAffectiveState,
  dominantAffectMood,
  mapAvatarState,
  normalizePersonalitySnapshot,
  normalizeAvatarState,
} from './personality'

export { createYuriClient, resetYuriClientForTests } from './client/wails-client'
export {
  canUseNativeNotification,
  requestBrowserNotificationPermission,
  subscribeConversationUpdates,
  subscribeMemoryUpdates,
  subscribeNotifications,
  subscribePersonaUpdates,
  subscribePersonalityUpdates,
} from './client/events'
export type { MemoryUpdateEvent } from './client/events'
export type { ConversationTitleUpdateEvent } from './client/events'
