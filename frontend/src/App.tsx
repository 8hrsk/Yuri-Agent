import { useEffect, useMemo, useState } from 'react'

import { ChatView } from './components/ChatView'
import { ActivityView } from './components/ActivityView'
import { Icon } from './components/Icon'
import { MemoryView } from './components/MemoryView'
import { NotificationCenter } from './components/NotificationCenter'
import { OnboardingView } from './components/OnboardingView'
import { PluginView } from './components/PluginView'
import { PlaceholderView } from './components/PlaceholderView'
import { PersonaRelationshipView } from './components/PersonaRelationshipView'
import { ProviderSettingsView } from './components/ProviderSettingsView'
import { TasksView } from './components/TasksView'
import { Sidebar } from './components/Sidebar'
import { Topbar } from './components/Topbar'
import { useBackendConnection } from './hooks/useBackendConnection'
import { createYuriClient } from './lib/client'
import type { AgentProfile } from './lib/contracts'
import { isOnboardingComplete } from './lib/onboarding'
import { navItems, type NavId } from './lib/navigation'

function App() {
  const [activeId, setActiveId] = useState<NavId>('chat')
  const client = useMemo(() => createYuriClient(), [])
  const [onboardingStatus, setOnboardingStatus] = useState<'loading' | 'required' | 'ready'>('loading')
  const [activeAgent, setActiveAgent] = useState<AgentProfile>()
  const backend = useBackendConnection()
  const activeItem = useMemo(() => navItems.find((item) => item.id === activeId) ?? navItems[0], [activeId])

  useEffect(() => {
    let mounted = true
    void client.getOnboardingState().then((state) => {
      if (mounted) setOnboardingStatus(isOnboardingComplete(state) ? 'ready' : 'required')
    }).catch(() => {
      if (mounted) setOnboardingStatus('required')
    })
    return () => { mounted = false }
  }, [client])

  useEffect(() => {
    if (onboardingStatus !== 'ready') return
    let mounted = true
    void client.getActiveAgent().then((agent) => {
      if (mounted) setActiveAgent(agent)
    }).catch(() => undefined)
    return () => { mounted = false }
  }, [client, onboardingStatus])

  if (onboardingStatus === 'loading') {
    return <div className="onboarding-shell"><div className="onboarding-loading" role="status"><span className="onboarding-loading__pulse" /> Проверяю состояние первого запуска…</div></div>
  }

  if (onboardingStatus === 'required') {
    return <OnboardingView client={client} onComplete={() => setOnboardingStatus('ready')} />
  }

  return (
    <div className="app-shell">
      <NotificationCenter />
      <Sidebar activeId={activeId} agentName={activeAgent?.name ?? 'Агент'} connectionStatus={backend.status} onNavigate={setActiveId} />
      <main className="main-panel">
        <Topbar
          activeItem={activeItem}
          connectionLabel={backend.label}
          connectionStatus={backend.status}
          onReconnect={backend.refresh}
        />
        <div className="main-panel__scroll">
          {activeId === 'chat' ? (
            <ChatView agentName={activeAgent?.name ?? 'Агент'} backend={backend} onOpenSettings={() => setActiveId('settings')} />
          ) : activeId === 'tasks' ? (
            <TasksView />
          ) : activeId === 'memory' ? (
            <MemoryView />
          ) : activeId === 'activity' ? (
            <ActivityView />
          ) : activeId === 'relationship' || activeId === 'personality' ? (
            <PersonaRelationshipView key={activeId} section={activeId} />
          ) : activeId === 'plugins' ? (
            <PluginView />
          ) : activeId === 'settings' ? (
            <ProviderSettingsView onBackToChat={() => setActiveId('chat')} />
          ) : (
            <PlaceholderView item={activeItem} />
          )}
        </div>
        <footer className="statusbar">
          <span className="statusbar__left"><span className="statusbar__pulse" /> Local-first workspace</span>
          <span className="statusbar__right">Yuri stage 8 · agent profiles <Icon name="spark" width={12} height={12} /></span>
        </footer>
      </main>
    </div>
  )
}

export default App
