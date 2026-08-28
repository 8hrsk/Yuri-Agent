import { useMemo, useState } from 'react'

import { ChatView } from './components/ChatView'
import { ActivityView } from './components/ActivityView'
import { Icon } from './components/Icon'
import { MemoryView } from './components/MemoryView'
import { NotificationCenter } from './components/NotificationCenter'
import { PluginView } from './components/PluginView'
import { PlaceholderView } from './components/PlaceholderView'
import { ProviderSettingsView } from './components/ProviderSettingsView'
import { TasksView } from './components/TasksView'
import { Sidebar } from './components/Sidebar'
import { Topbar } from './components/Topbar'
import { useBackendConnection } from './hooks/useBackendConnection'
import { navItems, type NavId } from './lib/navigation'

function App() {
  const [activeId, setActiveId] = useState<NavId>('chat')
  const backend = useBackendConnection()
  const activeItem = useMemo(() => navItems.find((item) => item.id === activeId) ?? navItems[0], [activeId])

  return (
    <div className="app-shell">
      <NotificationCenter />
      <Sidebar activeId={activeId} connectionStatus={backend.status} onNavigate={setActiveId} />
      <main className="main-panel">
        <Topbar
          activeItem={activeItem}
          connectionLabel={backend.label}
          connectionStatus={backend.status}
          onReconnect={backend.refresh}
        />
        <div className="main-panel__scroll">
          {activeId === 'chat' ? (
            <ChatView backend={backend} onOpenSettings={() => setActiveId('settings')} />
          ) : activeId === 'tasks' ? (
            <TasksView />
          ) : activeId === 'memory' ? (
            <MemoryView />
          ) : activeId === 'activity' ? (
            <ActivityView />
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
          <span className="statusbar__right">Yuri stage 4 <Icon name="spark" width={12} height={12} /></span>
        </footer>
      </main>
    </div>
  )
}

export default App
