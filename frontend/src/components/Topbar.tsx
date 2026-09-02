import type { NavItem } from '../lib/navigation'

import { ConnectionBadge } from './ConnectionBadge'
import { Icon } from './Icon'
import type { BackendStatus } from '../lib/backend'

type TopbarProps = {
  activeItem: NavItem
  connectionStatus: BackendStatus
  connectionLabel: string
  onReconnect: () => void
}

export function Topbar({ activeItem, connectionStatus, connectionLabel, onReconnect }: TopbarProps) {
  return (
    <header className="topbar">
      <div className="topbar__crumbs">
        <span className="topbar__workspace">Workspace</span>
        <Icon name="chevron-right" width={14} height={14} />
        <span className="topbar__current">{activeItem.label}</span>
      </div>

      <div className="topbar__actions">
        <button className="topbar__connection" onClick={onReconnect} title="Проверить подключение заново" type="button">
          <ConnectionBadge compact label={connectionLabel} status={connectionStatus} />
        </button>
      </div>
    </header>
  )
}
