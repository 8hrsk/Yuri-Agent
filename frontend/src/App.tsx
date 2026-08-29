import { useEffect, useMemo, useState } from 'react'

import { ChatView } from './components/ChatView'
import { ActivityView } from './components/ActivityView'
import { AgentProfileForm } from './components/AgentProfileForm'
import { CollaborationView } from './components/CollaborationView'
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
import { defaultAgentDraft } from './lib/agents'
import { createYuriClient } from './lib/client'
import type { AgentProfile, AgentProfileInput } from './lib/contracts'
import { isOnboardingComplete } from './lib/onboarding'
import { navItems, type NavId } from './lib/navigation'

function App() {
  const [activeId, setActiveId] = useState<NavId>('chat')
  const client = useMemo(() => createYuriClient(), [])
  const [onboardingStatus, setOnboardingStatus] = useState<'loading' | 'required' | 'ready'>('loading')
  const [activeAgent, setActiveAgent] = useState<AgentProfile>()
  const [agents, setAgents] = useState<AgentProfile[]>([])
  const [agentFormOpen, setAgentFormOpen] = useState(false)
  const [agentDraft, setAgentDraft] = useState<AgentProfileInput>(() => ({ ...defaultAgentDraft, name: '', preferences: '', traits: { ...defaultAgentDraft.traits } }))
  const [agentBusy, setAgentBusy] = useState(false)
  const [agentError, setAgentError] = useState<string>()
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
    void Promise.all([client.listAgents(), client.getActiveAgent()]).then(([listedAgents, current]) => {
      if (!mounted) return
      const nextAgents = current && !listedAgents.some((agent) => agent.id === current.id)
        ? [...listedAgents, current]
        : listedAgents
      setActiveAgent(current)
      setAgents(nextAgents.map((agent) => ({ ...agent, active: agent.id === current?.id })))
    }).catch(() => undefined)
    return () => { mounted = false }
  }, [client, onboardingStatus])

  const handleSelectAgent = async (agentId: string) => {
    if (agentId === activeAgent?.id || agentBusy) return
    setAgentBusy(true)
    setAgentError(undefined)
    try {
      const selected = await client.setActiveAgent(agentId)
      setActiveAgent(selected)
      setAgents((current) => current.map((agent) => ({ ...agent, active: agent.id === selected.id })))
    } catch (cause) {
      setAgentError(cause instanceof Error ? cause.message : 'Не удалось выбрать агента.')
    } finally {
      setAgentBusy(false)
    }
  }

  const openAgentForm = () => {
    setAgentDraft({ ...defaultAgentDraft, name: '', preferences: '', traits: { ...defaultAgentDraft.traits } })
    setAgentError(undefined)
    setAgentFormOpen(true)
  }

  const handleCreateAgent = async () => {
    setAgentBusy(true)
    setAgentError(undefined)
    try {
      const created = await client.createAgent(agentDraft)
      setActiveAgent(created)
      setAgents((current) => [...current.map((agent) => ({ ...agent, active: false })), { ...created, active: true }])
      setAgentFormOpen(false)
    } catch (cause) {
      setAgentError(cause instanceof Error ? cause.message : 'Не удалось создать агента.')
    } finally {
      setAgentBusy(false)
    }
  }

  if (onboardingStatus === 'loading') {
    return <div className="onboarding-shell"><div className="onboarding-loading" role="status"><span className="onboarding-loading__pulse" /> Проверяю состояние первого запуска…</div></div>
  }

  if (onboardingStatus === 'required') {
    return <OnboardingView client={client} onComplete={() => setOnboardingStatus('ready')} />
  }

  return (
    <div className="app-shell">
      <NotificationCenter />
      <Sidebar
        activeAgent={activeAgent}
        activeId={activeId}
        agentError={agentError}
        agents={agents}
        agentBusy={agentBusy}
        connectionStatus={backend.status}
        onCreateAgent={openAgentForm}
        onNavigate={setActiveId}
        onSelectAgent={(agentId) => void handleSelectAgent(agentId)}
      />
      <main className="main-panel">
        <Topbar
          activeItem={activeItem}
          connectionLabel={backend.label}
          connectionStatus={backend.status}
          onReconnect={backend.refresh}
        />
        <div className="main-panel__scroll">
          {activeId === 'chat' ? (
            <ChatView key={activeAgent?.id ?? 'no-active-agent'} agentName={activeAgent?.name ?? 'Агент'} backend={backend} onOpenSettings={() => setActiveId('settings')} />
          ) : activeId === 'tasks' ? (
            <TasksView />
          ) : activeId === 'memory' ? (
            <MemoryView key={activeAgent?.id ?? 'no-active-agent'} />
          ) : activeId === 'activity' ? (
            <ActivityView />
          ) : activeId === 'collaboration' ? (
            <CollaborationView key={activeAgent?.id ?? 'no-active-agent'} activeAgentId={activeAgent?.id} />
          ) : activeId === 'relationship' || activeId === 'personality' ? (
            <PersonaRelationshipView key={`${activeId}:${activeAgent?.id ?? 'no-active-agent'}`} section={activeId} />
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
      {agentFormOpen && (
        <div className="approval-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget && !agentBusy) setAgentFormOpen(false) }}>
          <section aria-labelledby="agent-create-title" aria-modal="true" className="approval-dialog agent-dialog" role="dialog">
            <div className="approval-dialog__mark"><Icon name="personality" width={22} height={22} /></div>
            <span className="section-heading__overline">AGENT ROSTER</span>
            <h2 id="agent-create-title">Создать нового агента</h2>
            <p>У каждого агента будет собственная личность, память и отношения. После создания он станет активным.</p>
            <AgentProfileForm busy={agentBusy} onBack={() => setAgentFormOpen(false)} onChange={setAgentDraft} onSubmit={() => void handleCreateAgent()} submitLabel="Создать и выбрать" value={agentDraft} />
            {agentError && <div className="agent-dialog__error" role="alert">{agentError}</div>}
          </section>
        </div>
      )}
    </div>
  )
}

export default App
