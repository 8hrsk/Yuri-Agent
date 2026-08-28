import type { Dispatch, SetStateAction } from 'react'

import { ConnectionBadge } from './ConnectionBadge'
import { Icon, type IconName } from './Icon'
import { navGroups, type NavId } from '../lib/navigation'
import type { BackendStatus } from '../lib/backend'

type SidebarProps = {
  activeId: NavId
  connectionStatus: BackendStatus
  onNavigate: Dispatch<SetStateAction<NavId>>
}

const iconName = (name: string): IconName => name as IconName

export function Sidebar({ activeId, connectionStatus, onNavigate }: SidebarProps) {
  return (
    <aside className="sidebar">
      <div className="sidebar__brand">
        <div className="brand-mark" aria-hidden="true">
          <span>Y</span>
          <i />
        </div>
        <div className="brand-copy">
          <span className="brand-copy__name">Yuri</span>
          <span className="brand-copy__meta">Personal AI</span>
        </div>
        <span className="brand-version">0.7</span>
      </div>

      <div className="sidebar__profile">
        <div className="profile-orb" aria-hidden="true">
          <span>Y</span>
        </div>
        <div className="profile-copy">
          <strong>Yuri</strong>
          <span>neutral · local</span>
        </div>
        <span className="profile-status" aria-label="Yuri is idle" />
      </div>

      <nav className="sidebar__nav" aria-label="Основная навигация">
        {navGroups.map((group) => (
          <div className="nav-group" key={group.id}>
            <span className="nav-group__label">{group.label}</span>
            <div className="nav-group__items">
              {group.items.map((item) => {
                const isActive = activeId === item.id

                return (
                  <button
                    aria-current={isActive ? 'page' : undefined}
                    className={`nav-item${isActive ? ' nav-item--active' : ''}`}
                    key={item.id}
                    onClick={() => onNavigate(item.id)}
                    type="button"
                  >
                    <span className="nav-item__icon"><Icon name={iconName(item.icon)} /></span>
                    <span className="nav-item__copy">
                      <span>{item.label}</span>
                      <small>{item.caption}</small>
                    </span>
                    {isActive && <span className="nav-item__active-dot" />}
                  </button>
                )
              })}
            </div>
          </div>
        ))}
      </nav>

      <div className="sidebar__footer">
        <ConnectionBadge compact label={connectionStatus === 'connected' ? 'Connected' : connectionStatus === 'connecting' ? 'Connecting' : 'Shell mode'} status={connectionStatus} />
        <span className="sidebar__footer-copy">Этап 7 · OSS readiness</span>
      </div>
    </aside>
  )
}
