export type YuriNotificationType = 'task.completed' | 'background.completed' | 'plugin.event' | 'rule.triggered' | 'agent.message' | 'unknown'

export interface YuriNotification {
  id: string
  type: YuriNotificationType
  title: string
  body: string
  createdAt: string
  allowNative: boolean
  permission?: NotificationPermission
  conversationId?: string
  deepLink?: string
}
