export const navItems = [
  { id: 'chat', label: 'Chat', caption: 'Диалоги', icon: 'chat', group: 'workspace' },
  { id: 'tasks', label: 'Tasks', caption: 'Задачи', icon: 'tasks', group: 'workspace' },
  { id: 'memory', label: 'Memory', caption: 'Память', icon: 'memory', group: 'workspace' },
  { id: 'collaboration', label: 'Collaboration', caption: 'Диалоги агентов', icon: 'collaboration', group: 'workspace' },
  { id: 'relationship', label: 'Relationship', caption: 'Связь', icon: 'relationship', group: 'workspace' },
  { id: 'personality', label: 'Personality', caption: 'Характер', icon: 'personality', group: 'workspace' },
  { id: 'plugins', label: 'Plugins', caption: 'Расширения', icon: 'plugins', group: 'system' },
  { id: 'activity', label: 'Activity', caption: 'Активность', icon: 'activity', group: 'system' },
  { id: 'settings', label: 'Settings', caption: 'Настройки', icon: 'settings', group: 'system' },
] as const

export type NavItem = (typeof navItems)[number]
export type NavId = NavItem['id']

export const navGroups = [
  { id: 'workspace', label: 'Workspace', items: navItems.filter((item) => item.group === 'workspace') },
  { id: 'system', label: 'System', items: navItems.filter((item) => item.group === 'system') },
] as const
