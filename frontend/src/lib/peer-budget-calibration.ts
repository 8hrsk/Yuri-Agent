import { modelRouteLabel } from './agents'
import type { PeerDialogue } from './contracts'

export const peerBudgetCalibrationMinimumSamples = 5

export type PeerBudgetCalibrationStatus = 'collecting' | 'balanced' | 'roomy' | 'tight'

export type PeerBudgetCalibration = {
  route: string
  samples: number
  requiredSamples: number
  hardStops: number
  turnUtilization: number
  tokenUtilization: number
  durationUtilization: number
  status: PeerBudgetCalibrationStatus
}

export function peerBudgetUtilization(used: number, limit: number): number {
  return limit > 0 ? Math.max(0, Math.min(1, used / limit)) : 0
}

export function peerBudgetRecommendationVerdict(dialogue: PeerDialogue): string {
  if (!dialogue.finishedAt) return 'Фактический расход ещё собирается.'
  if (dialogue.completionReason === 'max_turns' || dialogue.completionReason === 'max_tokens' || dialogue.completionReason === 'max_duration') {
    return 'Рекомендация оказалась тесной: диалог упёрся в жёсткий лимит.'
  }
  const recommendation = dialogue.recommendation
  if (!recommendation) return ''
  const peak = Math.max(
    peerBudgetUtilization(dialogue.turnCount, recommendation.maxTurns),
    peerBudgetUtilization(dialogue.tokensUsed, recommendation.maxTokens),
    peerBudgetUtilization(dialogue.durationUsedSeconds, recommendation.maxDurationSeconds),
  )
  return peak <= 0.5
    ? 'В этом запуске использована не более чем половина рекомендованного запаса.'
    : 'Рекомендация попала в рабочий диапазон этого запуска.'
}

export function peerBudgetCalibrationStatusLabel(group: PeerBudgetCalibration): string {
  switch (group.status) {
    case 'collecting': return `Собираем выборку: ${group.samples} из ${group.requiredSamples}`
    case 'tight': return 'Сигнал: лимит может быть тесным'
    case 'roomy': return 'Сигнал: запас может быть избыточным'
    default: return 'Сигнал: рабочий диапазон'
  }
}

function historicalAgentRoute(dialogue: PeerDialogue, agentId: string, providerId?: string, model?: string): string {
  const historical = [...dialogue.messages].reverse().find((message) => message.senderAgentId === agentId && (message.providerId || message.model))
  return modelRouteLabel(historical?.providerId ?? providerId, historical?.model ?? model)
}

function calibrationStatus(group: Omit<PeerBudgetCalibration, 'status'>): PeerBudgetCalibrationStatus {
  if (group.samples < group.requiredSamples) return 'collecting'
  if (group.hardStops / group.samples >= 0.2) return 'tight'
  const peak = Math.max(group.turnUtilization, group.tokenUtilization, group.durationUtilization)
  if (peak <= 0.5) return 'roomy'
  return 'balanced'
}

/**
 * Produces a read-only dogfood projection. It never feeds values back into
 * the resolver: even a mature signal remains evidence for an owner-reviewed
 * heuristic change, not authority to raise or lower a live hard limit.
 */
export function buildPeerBudgetCalibration(dialogues: PeerDialogue[]): PeerBudgetCalibration[] {
  const groups = new Map<string, Omit<PeerBudgetCalibration, 'status'>>()
  for (const dialogue of dialogues) {
    if (dialogue.status !== 'completed' || dialogue.budgetOrigin !== 'owner_recommendation' || !dialogue.recommendation) continue
    const route = `${historicalAgentRoute(dialogue, dialogue.initiatorAgentId, dialogue.initiatorProviderId, dialogue.initiatorModel)} ↔ ${historicalAgentRoute(dialogue, dialogue.peerAgentId, dialogue.peerProviderId, dialogue.peerModel)}`
    const current = groups.get(route) ?? {
      route, samples: 0, requiredSamples: peerBudgetCalibrationMinimumSamples, hardStops: 0,
      turnUtilization: 0, tokenUtilization: 0, durationUtilization: 0,
    }
    current.samples += 1
    current.turnUtilization += peerBudgetUtilization(dialogue.turnCount, dialogue.recommendation.maxTurns)
    current.tokenUtilization += peerBudgetUtilization(dialogue.tokensUsed, dialogue.recommendation.maxTokens)
    current.durationUtilization += peerBudgetUtilization(dialogue.durationUsedSeconds, dialogue.recommendation.maxDurationSeconds)
    if (dialogue.completionReason === 'max_turns' || dialogue.completionReason === 'max_tokens' || dialogue.completionReason === 'max_duration') current.hardStops += 1
    groups.set(route, current)
  }
  return [...groups.values()].map((group) => {
    const averaged = {
      ...group,
      turnUtilization: group.turnUtilization / group.samples,
      tokenUtilization: group.tokenUtilization / group.samples,
      durationUtilization: group.durationUtilization / group.samples,
    }
    return { ...averaged, status: calibrationStatus(averaged) }
  }).sort((a, b) => b.samples - a.samples || a.route.localeCompare(b.route))
}
