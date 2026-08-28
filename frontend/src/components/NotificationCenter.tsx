import { useEffect, useState } from 'react'

import { canUseNativeNotification, subscribeNotifications } from '../lib/client'
import type { YuriNotification } from '../lib/contracts'
import { Icon } from './Icon'

const toastDurationMs = 9000

function NotificationToast({ notification, onDismiss }: {
  notification: YuriNotification
  onDismiss: (id: string) => void
}) {
  useEffect(() => {
    const timer = globalThis.setTimeout(() => onDismiss(notification.id), toastDurationMs)
    return () => globalThis.clearTimeout(timer)
  }, [notification.id, onDismiss])

  return <article className="notification-toast" role="status">
    <span className="notification-toast__icon"><Icon name="spark" width={16} height={16} /></span>
    <div className="notification-toast__copy"><strong>{notification.title}</strong><p>{notification.body}</p><small>{notification.type === 'unknown' ? 'Yuri' : notification.type}</small></div>
    <button aria-label="Закрыть уведомление" className="icon-button icon-button--small" onClick={() => onDismiss(notification.id)} type="button"><Icon name="x" width={13} height={13} /></button>
  </article>
}
export function NotificationCenter() {
  const [notifications, setNotifications] = useState<YuriNotification[]>([])

  useEffect(() => subscribeNotifications((notification) => {
    setNotifications((current) => [notification, ...current.filter((item) => item.id !== notification.id)].slice(0, 3))
    if (canUseNativeNotification(notification)) {
      try {
        new Notification(notification.title, { body: notification.body, tag: notification.id })
      } catch {
        // Browser notification can fail after permission is revoked externally.
      }
    }
  }), [])

  const dismiss = (id: string) => setNotifications((current) => current.filter((notification) => notification.id !== id))
  if (notifications.length === 0) return null

  return <div aria-label="Уведомления Yuri" className="notification-center">{notifications.map((notification) => <NotificationToast key={notification.id} notification={notification} onDismiss={dismiss} />)}</div>
}
