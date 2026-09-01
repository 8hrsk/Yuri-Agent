import type { ActivityEvent, Conversation, JobRun, PeerDialogue, Schedule } from '../contracts'
import { nowIso } from './primitives'

function starterConversation(): Conversation {
  const createdAt = nowIso()
  return {
    id: 'conversation-welcome',
    title: 'Знакомство с Yuri',
    titleSource: 'user',
    preview: 'Текстовый vertical slice уже готов к работе.',
    updatedAt: createdAt,
    messages: [
      {
        id: 'message-welcome',
        role: 'assistant',
        content: 'Привет. Я Yuri — твой локальный AI-компаньон. Могу отвечать потоково, объяснять действия и ждать разрешения перед изменением файлов или отправкой данных. С чего начнём?',
        status: 'complete',
        createdAt,
      },
    ],
  }
}

function starterSchedule(): Schedule {
  const now = Date.now()
  return {
    id: 'schedule-daily-briefing',
    title: 'Утренняя сводка',
    prompt: 'Собери краткую сводку важных задач и событий на сегодня.',
    type: 'cron',
    expression: '0 9 * * 1-5',
    timezone: 'Europe/Moscow',
    misfirePolicy: 'run_once',
    enabled: true,
    status: 'active',
    nextRunAt: new Date(now + 1000 * 60 * 90).toISOString(),
    lastRunAt: new Date(now - 1000 * 60 * 60 * 18).toISOString(),
    deliveryChannel: 'notification',
    budget: { maxDurationSeconds: 180, maxTokens: 1800, maxToolCalls: 8 },
    createdAt: new Date(now - 1000 * 60 * 60 * 24 * 6).toISOString(),
    updatedAt: new Date(now - 1000 * 60 * 30).toISOString(),
  }
}

function starterJobRuns(): JobRun[] {
  const now = Date.now()
  return [
    {
      id: 'job-run-briefing-1',
      scheduleId: 'schedule-daily-briefing',
      scheduleTitle: 'Утренняя сводка',
      status: 'completed',
      attempt: 1,
      startedAt: new Date(now - 1000 * 60 * 60 * 18 - 1000 * 34).toISOString(),
      finishedAt: new Date(now - 1000 * 60 * 60 * 18).toISOString(),
      durationMs: 34000,
      summary: 'Сводка подготовлена и показана в приложении.',
      triggeredBy: 'schedule',
    },
    {
      id: 'job-run-briefing-2',
      scheduleId: 'schedule-daily-briefing',
      scheduleTitle: 'Утренняя сводка',
      status: 'skipped',
      attempt: 1,
      startedAt: new Date(now - 1000 * 60 * 60 * 42).toISOString(),
      finishedAt: new Date(now - 1000 * 60 * 60 * 42).toISOString(),
      durationMs: 0,
      summary: 'Пропущено: приложение было закрыто в quiet hours.',
      triggeredBy: 'recovery',
    },
  ]
}

function starterActivity(): ActivityEvent[] {
  const now = Date.now()
  return [
    {
      id: 'activity-job-briefing-1',
      type: 'job',
      status: 'completed',
      title: 'Утренняя сводка завершена',
      detail: 'Сводка подготовлена и показана в приложении.',
      source: 'scheduler',
      scheduleId: 'schedule-daily-briefing',
      runId: 'job-run-briefing-1',
      createdAt: new Date(now - 1000 * 60 * 60 * 18).toISOString(),
      durationMs: 34000,
      provenance: 'schedule-daily-briefing',
    },
    {
      id: 'activity-system-ready',
      type: 'system',
      status: 'info',
      title: 'Фоновый worker готов',
      detail: 'Durable scheduler восстановил расписания после запуска Yuri.',
      source: 'application',
      createdAt: new Date(now - 1000 * 60 * 42).toISOString(),
      provenance: 'startup recovery',
    },
    {
      id: 'activity-proactive-preview',
      type: 'proactive',
      status: 'skipped',
      title: 'Уведомление отложено',
      detail: 'Триггер попал в quiet hours и будет пересмотрен позднее.',
      source: 'proactivity policy',
      createdAt: new Date(now - 1000 * 60 * 12).toISOString(),
      reason: 'quiet hours',
      provenance: 'local policy gate',
    },
  ]
}

function starterPeerDialogues(): PeerDialogue[] {
  const now = Date.now()
  return [
    {
      id: 'peer-dialogue-briefing',
      initiatorAgentId: 'agent-yuri',
      initiatorName: 'Юри',
      peerAgentId: 'agent-mira',
      peerName: 'Мира',
      triggerKind: 'agent_tool',
      triggerReason: 'Агент явно запросил консультацию peer через tool.',
      purpose: 'Проверить, как лучше структурировать утреннюю сводку.',
      status: 'completed',
      turnCount: 2,
      minTurns: 2,
      maxTurns: 4,
      tokensUsed: 486,
      maxTokens: 8000,
      maxDurationSeconds: 90,
      durationUsedSeconds: 60,
      cooldownSeconds: 300,
      budgetOrigin: 'agent_default',
      completionReason: 'semantic',
      createdAt: new Date(now - 1000 * 60 * 18).toISOString(),
      finishedAt: new Date(now - 1000 * 60 * 17).toISOString(),
      messages: [
        {
          id: 'peer-dialogue-briefing-message-0',
          sequence: 0,
          senderAgentId: 'agent-yuri',
          senderName: 'Юри',
          recipientAgentId: 'agent-mira',
          recipientName: 'Мира',
          content: 'Как лучше подать пользователю короткую утреннюю сводку?',
          createdAt: new Date(now - 1000 * 60 * 18).toISOString(),
        },
        {
          id: 'peer-dialogue-briefing-message-1',
          sequence: 1,
          senderAgentId: 'agent-mira',
          senderName: 'Мира',
          recipientAgentId: 'agent-yuri',
          recipientName: 'Юри',
          content: 'Сначала три самых важных пункта, затем задачи с дедлайнами и отдельный блок для того, что требует решения пользователя.',
          createdAt: new Date(now - 1000 * 60 * 17).toISOString(),
        },
      ],
    },
    {
      id: 'peer-dialogue-research',
      initiatorAgentId: 'agent-yuri',
      initiatorName: 'Юри',
      peerAgentId: 'agent-mira',
      peerName: 'Мира',
      triggerKind: 'autonomous',
      triggerReason: 'Нужен независимый взгляд на границы исследования.',
      purpose: 'Сверить план небольшого исследования.',
      status: 'running',
      turnCount: 0,
      minTurns: 2,
      maxTurns: 4,
      tokensUsed: 0,
      maxTokens: 8000,
      maxDurationSeconds: 90,
      durationUsedSeconds: 0,
      cooldownSeconds: 300,
      budgetOrigin: 'agent_default',
      createdAt: new Date(now - 1000 * 32).toISOString(),
      messages: [
        {
          id: 'peer-dialogue-research-message-0',
          sequence: 0,
          senderAgentId: 'agent-yuri',
          senderName: 'Юри',
          recipientAgentId: 'agent-mira',
          recipientName: 'Мира',
          content: 'Проверь, не слишком ли широкая цель исследования и какие источники лучше взять первыми.',
          createdAt: new Date(now - 1000 * 32).toISOString(),
        },
      ],
    },
  ]
}

export { starterActivity, starterConversation, starterJobRuns, starterPeerDialogues, starterSchedule }
