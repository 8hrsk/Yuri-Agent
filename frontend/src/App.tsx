import { useMemo, useState } from 'react'

import { ChatView } from './components/ChatView'
import { Icon } from './components/Icon'
import { MemoryView } from './components/MemoryView'
import { PlaceholderView } from './components/PlaceholderView'
import { ProviderSettingsView } from './components/ProviderSettingsView'
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
          ) : activeId === 'memory' ? (
            <MemoryView />
          ) : activeId === 'settings' ? (
            <ProviderSettingsView onBackToChat={() => setActiveId('chat')} />
          ) : (
            <PlaceholderView item={activeItem} />
          )}
        </div>
        <footer className="statusbar">
          <span className="statusbar__left"><span className="statusbar__pulse" /> Local-first workspace</span>
          <span className="statusbar__right">Yuri foundation <Icon name="spark" width={12} height={12} /></span>
        </footer>
      </main>
    </div>
  )
}

export default App
