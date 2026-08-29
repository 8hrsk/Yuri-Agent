import { useState, type Dispatch, type SetStateAction } from 'react'

import { ConnectionBadge } from './ConnectionBadge'
import { Icon, type IconName } from './Icon'
import { navGroups, type NavId } from '../lib/navigation'
import type { BackendStatus } from '../lib/backend'
import type { AgentProfile } from '../lib/contracts'

type SidebarProps = {
  activeId: NavId
  activeAgent?: AgentProfile
  agents: AgentProfile[]
  agentBusy?: boolean
  agentError?: string
  connectionStatus: BackendStatus
  onNavigate: Dispatch<SetStateAction<NavId>>
  onSelectAgent: (agentId: string) => void
  onCreateAgent: () => void
}

const iconName = (name: string): IconName => name as IconName

export function Sidebar({ activeId, activeAgent, agents, agentBusy = false, agentError, connectionStatus, onNavigate, onSelectAgent, onCreateAgent }: SidebarProps) {
  const [agentMenuOpen, setAgentMenuOpen] = useState(false)
  const agentName = activeAgent?.name ?? 'Агент'
  const initial = Array.from(agentName.trim())[0]?.toLocaleUpperCase('ru-RU') ?? 'A'
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

      <div className="sidebar__agent-picker">
        <button aria-expanded={agentMenuOpen} aria-haspopup="listbox" className="sidebar__profile" onClick={() => setAgentMenuOpen((open) => !open)} type="button">
          <div className="profile-orb" aria-hidden="true">
            <span>{initial}</span>
          </div>
          <div className="profile-copy">
            <strong>{agentName}</strong>
            <span>{activeAgent ? 'active · local' : 'profile unavailable'}</span>
          </div>
          <span className="profile-status" aria-label={`${agentName} is idle`} />
          <Icon className="sidebar__profile-chevron" name="chevron-right" width={13} height={13} />
        </button>
        {agentMenuOpen && (
          <div aria-label="Выбор агента" className="sidebar__agent-menu" role="listbox">
            <div className="sidebar__agent-menu-heading"><span>Агенты</span><small>{agents.length}</small></div>
            {agents.length === 0 && <span className="sidebar__agent-empty">Агенты ещё не загружены</span>}
            {agents.map((agent) => (
              <button
                aria-selected={agent.id === activeAgent?.id}
                className={`sidebar__agent-option${agent.id === activeAgent?.id ? ' sidebar__agent-option--active' : ''}`}
                disabled={agentBusy}
                key={agent.id}
                onClick={() => { setAgentMenuOpen(false); onSelectAgent(agent.id) }}
                role="option"
                type="button"
              >
                <span className="sidebar__agent-option-initial">{Array.from(agent.name)[0]?.toLocaleUpperCase('ru-RU') ?? 'A'}</span>
                <span><strong>{agent.name}</strong><small>{agent.gender}{agent.age ? ` · ${agent.age}` : ''}</small></span>
                {agent.id === activeAgent?.id && <Icon name="check" width={13} height={13} />}
              </button>
            ))}
            <button className="sidebar__agent-create" disabled={agentBusy} onClick={() => { setAgentMenuOpen(false); onCreateAgent() }} type="button">
              <Icon name="plus" width={14} height={14} /> Создать агента
            </button>
            {agentError && <span className="sidebar__agent-error" role="alert">{agentError}</span>}
          </div>
        )}
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
        <span className="sidebar__footer-copy">Этап 8 · Agent profiles</span>
      </div>
    </aside>
  )
}
