import type { NavItem } from '../lib/navigation'

import { Icon, type IconName } from './Icon'

type PlaceholderViewProps = {
  item: NavItem
}

type ViewCopy = {
  overline: string
  title: string
  description: string
  detail: string
  metric: string
  metricLabel: string
}

const viewCopy: Record<NavItem['id'], ViewCopy> = {
  chat: {
    overline: 'Workspace',
    title: 'Chat',
    description: 'Ваши локальные диалоги появятся здесь после подключения application core.',
    detail: 'Conversation store не подключён',
    metric: '00',
    metricLabel: 'диалогов',
  },
  tasks: {
    overline: 'Workspace',
    title: 'Tasks',
    description: 'Планировщик задач и фоновые jobs будут доступны после реализации worker layer.',
    detail: 'Scheduler ожидает backend',
    metric: '—',
    metricLabel: 'активных задач',
  },
  memory: {
    overline: 'Workspace',
    title: 'Memory',
    description: 'Единая память Yuri будет синхронизировать контекст между всеми диалогами.',
    detail: 'Memory service ожидает backend',
    metric: '—',
    metricLabel: 'сохранённых записей',
  },
  relationship: {
    overline: 'Workspace',
    title: 'Relationship',
    description: 'История отношений, сигналы доверия и динамика связи появятся в следующих вехах.',
    detail: 'Relationship model · planned',
    metric: '—',
    metricLabel: 'сигналов',
  },
  personality: {
    overline: 'Workspace',
    title: 'Personality',
    description: 'Профиль характера и его эволюция будут отделены от неизменяемых security policies.',
    detail: 'Persona engine · planned',
    metric: '—',
    metricLabel: 'версий характера',
  },
  plugins: {
    overline: 'System',
    title: 'Plugins',
    description: 'Каталог и управление GitHub-плагинами подключатся после стабилизации plugin runtime.',
    detail: 'Plugin host · planned',
    metric: '—',
    metricLabel: 'установленных',
  },
  activity: {
    overline: 'System',
    title: 'Activity',
    description: 'Безопасный append-only журнал действий будет показывать состояние и решения агента.',
    detail: 'Audit stream · planned',
    metric: '—',
    metricLabel: 'событий',
  },
  settings: {
    overline: 'System',
    title: 'Settings',
    description: 'Здесь появятся настройки провайдера, разрешённых директорий и локального поведения Yuri.',
    detail: 'Configuration service · planned',
    metric: '01',
    metricLabel: 'workspace',
  },
}

const iconName = (name: string): IconName => name as IconName

export function PlaceholderView({ item }: PlaceholderViewProps) {
  const copy = viewCopy[item.id]

  return (
    <div className="placeholder-view">
      <div className="placeholder-view__topline">
        <span className="welcome-card__eyebrow"><span className="eyebrow-dot" /> {copy.overline.toUpperCase()}</span>
        <span className="stage-pill">STAGE 0 · SHELL</span>
      </div>
      <div className="placeholder-view__hero">
        <div className="placeholder-view__icon"><Icon name={iconName(item.icon)} width={23} height={23} /></div>
        <h1>{copy.title}<span className="title-dot">.</span></h1>
        <p>{copy.description}</p>
      </div>
      <div className="placeholder-grid">
        <div className="placeholder-panel placeholder-panel--wide">
          <div className="placeholder-panel__heading">
            <span className="section-heading__overline">Состояние модуля</span>
            <span className="status-label"><span /> Not connected</span>
          </div>
          <div className="placeholder-line" />
          <p>{copy.detail}</p>
        </div>
        <div className="placeholder-panel placeholder-panel--metric">
          <span className="section-heading__overline">Workspace</span>
          <strong>{copy.metric}</strong>
          <span>{copy.metricLabel}</span>
        </div>
      </div>
      <div className="placeholder-note">
        <span className="placeholder-note__mark"><Icon name="spark" width={16} height={16} /></span>
        <span>Это навигационная заглушка. Реализация появится последовательно по roadmap Yuri.</span>
      </div>
    </div>
  )
}
