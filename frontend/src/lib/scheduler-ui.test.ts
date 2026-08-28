import { describe, expect, it } from 'vitest'

import { manualRunFeedback } from './scheduler-ui'

describe('scheduler UI feedback', () => {
  it('treats accepted queued and running manual jobs as informational states', () => {
    expect(manualRunFeedback('queued')).toEqual({
      kind: 'info',
      text: 'Задача добавлена в очередь scheduler.',
    })
    expect(manualRunFeedback('running')).toEqual({
      kind: 'info',
      text: 'Задача запущена и выполняется в фоне.',
    })
  })

  it('distinguishes terminal success and failure', () => {
    expect(manualRunFeedback('completed').kind).toBe('success')
    expect(manualRunFeedback('failed').kind).toBe('error')
  })
})
