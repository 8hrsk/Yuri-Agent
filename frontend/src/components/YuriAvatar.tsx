import type { CSSProperties } from 'react'

import type { AffectiveState, AvatarState } from '../lib/contracts'
import { defaultAffectiveState, dominantAffectMood } from '../lib/personality'

export type YuriAvatarSize = 'sm' | 'md' | 'lg'

export interface YuriAvatarProps {
  state?: AvatarState
  affect?: AffectiveState
  size?: YuriAvatarSize
  label?: string
}

const stateLabels: Record<AvatarState, string> = {
  idle: 'готова к диалогу',
  listening: 'слушает',
  thinking: 'думает',
  speaking: 'говорит',
  tool_running: 'выполняет действие',
  error: 'нужна проверка',
}

/**
 * Small, dependency-free SVG renderer. The avatar is intentionally a visual
 * status surface: affect changes its colour/energy, while run state controls
 * motion. It never performs an action and has no connection to permissions.
 */
export function YuriAvatar({ state = 'idle', affect = defaultAffectiveState(), size = 'md', label }: YuriAvatarProps) {
  const mood = dominantAffectMood(affect)
  const energy = Math.max(0.2, Math.min(1, affect.intensity || 0.2))
  const style = { '--avatar-energy': energy.toFixed(2) } as CSSProperties
  const ariaLabel = label ?? `Yuri · ${stateLabels[state]}`

  return (
    <div aria-label={ariaLabel} className={`yuri-avatar yuri-avatar--${size} yuri-avatar--${state} yuri-avatar--${mood}`} role="img" style={style}>
      <svg aria-hidden="true" className="yuri-avatar__svg" viewBox="0 0 120 120">
        <circle className="yuri-avatar__halo" cx="60" cy="60" r="48" />
        <circle className="yuri-avatar__halo-ring" cx="60" cy="60" r="43" />
        <path className="yuri-avatar__orbit" d="M22 48c7-23 29-35 53-28 14 4 23 13 27 26" />
        <path className="yuri-avatar__orbit yuri-avatar__orbit--secondary" d="M18 73c12 20 36 29 59 21 11-4 19-11 25-21" />
        <g className="yuri-avatar__face">
          <path className="yuri-avatar__hair" d="M36 57c0-24 10-34 25-34 18 0 27 12 25 34l-4 16H39Z" />
          <path className="yuri-avatar__neck" d="M51 86v11h18V86" />
          <path className="yuri-avatar__skin" d="M39 55c0-15 8-25 21-25 15 0 22 10 21 25l-2 18c-2 11-9 17-19 17-11 0-18-6-20-17Z" />
          <path className="yuri-avatar__fringe" d="M38 51c2-17 9-27 23-27 13 0 22 8 24 24-8-2-14-8-19-15-6 9-15 15-28 18Z" />
          <ellipse className="yuri-avatar__eye yuri-avatar__eye--left" cx="51" cy="61" rx="3" ry="4" />
          <ellipse className="yuri-avatar__eye yuri-avatar__eye--right" cx="69" cy="61" rx="3" ry="4" />
          <path className="yuri-avatar__brow yuri-avatar__brow--left" d="M47 53c3-2 6-2 9 0" />
          <path className="yuri-avatar__brow yuri-avatar__brow--right" d="M65 53c3-2 6-2 9 0" />
          <path className="yuri-avatar__mouth" d="M55 76c3 2 7 2 10 0" />
          <circle className="yuri-avatar__cheek yuri-avatar__cheek--left" cx="45" cy="70" r="3" />
          <circle className="yuri-avatar__cheek yuri-avatar__cheek--right" cx="75" cy="70" r="3" />
        </g>
        <g className="yuri-avatar__listening-waves">
          <path d="M25 55c-4 4-4 8 0 12" />
          <path d="M20 50c-7 7-7 15 0 22" />
          <path d="M95 55c4 4 4 8 0 12" />
          <path d="M100 50c7 7 7 15 0 22" />
        </g>
        <g className="yuri-avatar__thinking-dots">
          <circle cx="85" cy="35" r="2" />
          <circle cx="92" cy="31" r="2.5" />
          <circle cx="101" cy="30" r="3" />
        </g>
        <circle className="yuri-avatar__tool-orbit" cx="60" cy="12" r="3" />
        <path className="yuri-avatar__error-mark" d="m92 87 8 8m0-8-8 8" />
      </svg>
    </div>
  )
}
