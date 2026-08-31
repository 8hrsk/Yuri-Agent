import { useCallback, useEffect, useMemo, useState } from 'react'

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
import { useSidebarCollapse } from './hooks/useSidebarCollapse'
import { clearAgentDraft, loadAgentDraft, newAgentDraft } from './lib/agents'
import { createYuriClient } from './lib/client'
import type { AgentProfile, AgentProfileInput } from './lib/contracts'
import { isOnboardingComplete } from './lib/onboarding'
import { navItems, type NavId } from './lib/navigation'

function App() {
  const [activeId, setActiveId] = useState<NavId>('chat')
  const client = useMemo(() => createYuriClient(), [])
  const [onboardingStatus, setOnboardingStatus] = useState<'loading' | 'required' | 'ready'>('loading')
  const [activeAgent, setActiveAgent] = useState<AgentProfile>()
  // The active agent keys the workspace views, so rendering them before it is
  // known mounts every one of them twice: once under a placeholder key and once
  // under the real one (M-37).
  const [agentsStatus, setAgentsStatus] = useState<'loading' | 'ready'>('loading')
  const [agents, setAgents] = useState<AgentProfile[]>([])
  const [agentFormOpen, setAgentFormOpen] = useState(false)
  const [agentDraft, setAgentDraft] = useState<AgentProfileInput>(() => loadAgentDraft(newAgentDraft({ name: '', preferences: '' })))
  const [agentBusy, setAgentBusy] = useState(false)
  const [agentError, setAgentError] = useState<string>()
  const backend = useBackendConnection()
  const sidebar = useSidebarCollapse()
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
    }).catch(() => undefined).finally(() => {
      // A roster that cannot be loaded still resolves the key: the workspace
      // falls back to the placeholder agent instead of never mounting.
      if (mounted) setAgentsStatus('ready')
    })
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

  // Handed to the always-mounted chat surface, so they must not change identity
  // on every App render.
  const openSettingsTab = useCallback(() => setActiveId('settings'), [])
  const openChatTab = useCallback(() => setActiveId('chat'), [])

  const openAgentForm = () => {
    setAgentDraft(loadAgentDraft(newAgentDraft({ name: '', preferences: '' })))
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
      clearAgentDraft()
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
        collapsed={sidebar.collapsed}
        connectionStatus={backend.status}
        onCreateAgent={openAgentForm}
        onNavigate={setActiveId}
        onSelectAgent={(agentId) => void handleSelectAgent(agentId)}
        onToggleCollapsed={sidebar.toggle}
      />
      <main className="main-panel">
        <Topbar
          activeItem={activeItem}
          connectionLabel={backend.label}
          connectionStatus={backend.status}
          onReconnect={backend.refresh}
        />
        <div className="main-panel__scroll">
          {agentsStatus === 'loading' ? (
            <div className="onboarding-loading" role="status"><span className="onboarding-loading__pulse" /> Загружаю профиль агента…</div>
          ) : (
            <>
              {/*
                * The chat surface is mounted for the whole session instead of
                * being one branch of the tab switch. A run is a long-lived,
                * cancellable operation with a pending approval attached to it;
                * unmounting the view dropped its id, its status and its
                * approval dialog, so the answer disappeared and a dangerous
                * action could sit waiting for a decision nobody was ever shown
                * (H-9). Hidden rather than unmounted, the run keeps streaming
                * into the same state and the approval dialog escapes this
                * subtree through a portal.
                */}
              <ChatView
                agentId={activeAgent?.id}
                agentName={activeAgent?.name ?? 'Агент'}
                backend={backend}
                hidden={activeId !== 'chat'}
                key={activeAgent?.id ?? 'no-active-agent'}
                onOpenChat={openChatTab}
                onOpenSettings={openSettingsTab}
              />
              {activeId !== 'chat' && (activeId === 'tasks' ? (
                <TasksView />
              ) : activeId === 'memory' ? (
                <MemoryView key={activeAgent?.id ?? 'no-active-agent'} />
              ) : activeId === 'activity' ? (
                <ActivityView />
              ) : activeId === 'collaboration' ? (
                <CollaborationView key={activeAgent?.id ?? 'no-active-agent'} activeAgentId={activeAgent?.id} />
              ) : activeId === 'relationship' || activeId === 'personality' ? (
                <PersonaRelationshipView key={activeAgent?.id ?? 'no-active-agent'} onSelectSection={setActiveId} section={activeId} />
              ) : activeId === 'plugins' ? (
                <PluginView />
              ) : activeId === 'settings' ? (
                <ProviderSettingsView onBackToChat={openChatTab} />
              ) : (
                <PlaceholderView item={activeItem} />
              ))}
            </>
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
