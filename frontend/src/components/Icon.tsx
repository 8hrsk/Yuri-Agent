import type { SVGProps } from 'react'

export type IconName =
  | 'activity'
  | 'arrow-up'
  | 'chat'
  | 'check'
  | 'chevron-right'
  | 'clock'
  | 'command'
  | 'lock'
  | 'mic'
  | 'memory'
  | 'personality'
  | 'plugins'
  | 'plus'
  | 'refresh'
  | 'relationship'
  | 'search'
  | 'settings'
  | 'shield'
  | 'spark'
  | 'tasks'
  | 'volume'
  | 'warning'
  | 'x'

type IconProps = SVGProps<SVGSVGElement> & { name: IconName }

const paths: Record<IconName, JSX.Element> = {
  activity: <path d="M3 12h3l2-7 4 14 2-7h7" />,
  'arrow-up': <path d="m5 12 7-7 7 7M12 5v14" />,
  chat: <path d="M20 11.5a7.5 7.5 0 0 1-8 7.5 8.8 8.8 0 0 1-3.2-.6L4 20l1.5-4A7.4 7.4 0 0 1 4.5 12 7.5 7.5 0 0 1 12 4.5a7.5 7.5 0 0 1 8 7Z" />,
  check: <path d="m5 12 4.2 4.2L19 6.5" />,
  'chevron-right': <path d="m9 5 7 7-7 7" />,
  clock: <><circle cx="12" cy="12" r="8.5" /><path d="M12 7v5l3.5 2" /></>,
  command: <><path d="M7 3a3 3 0 1 0 0 6h10a3 3 0 1 0 0-6M7 15a3 3 0 1 0 0 6 3 3 0 0 0 3-3V7a3 3 0 1 0-3-3M17 9a3 3 0 1 0 0-6M17 15a3 3 0 1 1 0 6 3 3 0 0 1-3-3V7a3 3 0 1 1 3 3" /></>,
  lock: <><rect x="5" y="10" width="14" height="11" rx="2" /><path d="M8 10V7a4 4 0 0 1 8 0v3M12 14v3" /></>,
  mic: <><rect x="8" y="3" width="8" height="12" rx="4" /><path d="M5 11a7 7 0 0 0 14 0M12 18v3M8 21h8" /></>,
  memory: <><path d="M5 5.5A2.5 2.5 0 0 1 7.5 3H19v15.5a2.5 2.5 0 0 0-2.5-2.5h-9A2.5 2.5 0 0 0 5 18.5Z" /><path d="M5 18.5A2.5 2.5 0 0 0 7.5 21H19M9 7h6M9 10h6" /></>,
  personality: <><path d="M12 20a8 8 0 1 0-8-8c0 1.6.5 3.1 1.3 4.4L4 20l3.7-1.3A8 8 0 0 0 12 20Z" /><path d="M8.5 11h.01M15.5 11h.01M8.7 14.5c1.9 1.2 3.7 1.2 5.6 0" /></>,
  plugins: <><path d="M8 3v4M16 3v4M4 7h16v13H4zM8 12h.01M12 12h.01M16 12h.01M8 16h.01M12 16h.01" /></>,
  plus: <path d="M12 5v14M5 12h14" />,
  refresh: <><path d="M20 11a8 8 0 0 0-14.9-3L4 10" /><path d="M4 5v5h5M4 13a8 8 0 0 0 14.9 3L20 14" /><path d="M20 19v-5h-5" /></>,
  relationship: <><path d="M12 20s-7-4.3-7-10.2A3.8 3.8 0 0 1 12 7a3.8 3.8 0 0 1 7 2.8C19 15.7 12 20 12 20Z" /><path d="M8.5 12.5h1.8l1-2 1.7 4 1-2H16" /></>,
  search: <><circle cx="10.8" cy="10.8" r="6.3" /><path d="m16 16 4.5 4.5" /></>,
  settings: <><path d="M12 8.5a3.5 3.5 0 1 0 0 7 3.5 3.5 0 0 0 0-7Z" /><path d="m19 13.5 1.3 1-.1 2-1.7 1-1.7-1a8 8 0 0 1-1.7 1l-.2 2-1.8.8-1.6-1.3a8 8 0 0 1-2 0l-1.6 1.3-1.8-.8-.2-2a8 8 0 0 1-1.7-1l-1.7 1-1.7-1 .1-2 1.3-1a8 8 0 0 1 0-2l-1.3-1 .1-2 1.7-1 1.7 1a8 8 0 0 1 1.7-1l.2-2 1.8-.8 1.6 1.3a8 8 0 0 1 2 0l1.6-1.3 1.8.8.2 2a8 8 0 0 1 1.7 1l1.7-1 1.7 1-.1 2-1.3 1a8 8 0 0 1 0 2Z" /></>,
  shield: <><path d="M12 3 19 6v5c0 4.6-2.9 8.3-7 10-4.1-1.7-7-5.4-7-10V6l7-3Z" /><path d="m9 12 2 2 4-4" /></>,
  spark: <path d="m12 3 1.7 5.3L19 10l-5.3 1.7L12 17l-1.7-5.3L5 10l5.3-1.7L12 3ZM19 16l.7 2.3L22 19l-2.3.7L19 22l-.7-2.3L16 19l2.3-.7L19 16Z" />,
  tasks: <><rect x="4" y="4" width="16" height="16" rx="3" /><path d="m8 12 2.2 2.2L16 8.5M8 8h.01M8 16h.01" /></>,
  volume: <><path d="M4 10v4h4l5 4V6l-5 4H4Z" /><path d="M17 9a4 4 0 0 1 0 6M19.5 6.5a7.5 7.5 0 0 1 0 11" /></>,
  warning: <><path d="m12 4 9 16H3L12 4Z" /><path d="M12 9v5M12 17h.01" /></>,
  x: <path d="m6 6 12 12M18 6 6 18" />,
}

export function Icon({ name, width = 18, height = 18, strokeWidth = 1.8, ...props }: IconProps) {
  return (
    <svg
      aria-hidden="true"
      fill="none"
      height={height}
      viewBox="0 0 24 24"
      width={width}
      stroke="currentColor"
      strokeLinecap="round"
      strokeLinejoin="round"
      strokeWidth={strokeWidth}
      {...props}
    >
      {paths[name]}
    </svg>
  )
}
