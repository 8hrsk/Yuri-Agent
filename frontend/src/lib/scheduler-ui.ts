import type { JobRunStatus } from './contracts'

export type SchedulerFeedback = {
  kind: 'success' | 'info' | 'error'
  text: string
}
export function manualRunFeedback(status?: JobRunStatus): SchedulerFeedback {
  switch (status) {
    case 'completed':
      return { kind: 'success', text: 'Задача выполнена.' }
    case 'queued':
      return { kind: 'info', text: 'Задача добавлена в очередь scheduler.' }
    case 'running':
      return { kind: 'info', text: 'Задача запущена и выполняется в фоне.' }
    case 'skipped':
      return { kind: 'info', text: 'Запуск пропущен согласно текущей policy.' }
    case 'failed':
      return { kind: 'error', text: 'Фоновая задача завершилась с ошибкой.' }
    case 'cancelled':
      return { kind: 'error', text: 'Ручной запуск был отменён.' }
    case 'unknown':
    default:
      return { kind: 'info', text: 'Запуск передан scheduler; состояние появится в истории.' }
  }
}
