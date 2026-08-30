import { memo } from 'react'

import type { ChatTool } from '../lib/contracts'
import { Icon } from './Icon'

export const ToolAvailabilityBar = memo(function ToolAvailabilityBar({
  tools,
  allowedDirectories,
  loading,
  onOpenSettings,
}: {
  tools: ChatTool[]
  allowedDirectories: string[]
  loading: boolean
  onOpenSettings: () => void
}) {
  const availableTools = tools.filter((tool) => tool.available)
  return (
    <div aria-label="Доступность инструментов" className="tool-availability">
      <div className="tool-availability__heading">
        <span className="tool-availability__mark"><Icon name="command" width={13} height={13} /></span>
        <span><strong>Инструменты</strong><small>{loading ? 'Проверяю доступность…' : tools.length > 0 ? `${availableTools.length} доступно` : 'Не подключены'}</small></span>
      </div>
      <div className="tool-availability__tools">
        {tools.length === 0 && !loading && <span className="tool-availability__empty">Список tools появится после подключения runtime</span>}
        {tools.map((tool) => (
          <span className={`tool-availability__tool${tool.available ? '' : ' tool-availability__tool--disabled'}`} key={tool.id} title={tool.description}>
            <i /> {tool.label}
            {tool.requiresApproval && <em>approval</em>}
          </span>
        ))}
      </div>
      <div className="tool-availability__scope">
        <span className="tool-availability__scope-label">Разрешённые директории</span>
        {allowedDirectories.length > 0
          ? allowedDirectories.map((directory) => <code key={directory}>{directory}</code>)
          : <button className="tool-availability__settings" onClick={onOpenSettings} type="button">не настроены · выдать доступ</button>}
      </div>
    </div>
  )
})
