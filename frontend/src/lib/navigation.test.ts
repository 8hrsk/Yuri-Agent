import { describe, expect, it } from 'vitest'

import { navGroups, navItems } from './navigation'

describe('navigation shell', () => {
  it('keeps all foundation destinations available', () => {
    expect(navItems.map((item) => item.id)).toEqual([
      'chat',
      'tasks',
      'memory',
      'relationship',
      'personality',
      'plugins',
      'activity',
      'settings',
    ])
  })

  it('groups workspace and system destinations', () => {
    expect(navGroups.map((group) => [group.id, group.items.length])).toEqual([
      ['workspace', 5],
      ['system', 3],
    ])
  })
})
