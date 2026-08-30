import { starterPrompts } from './chat-view-model'
import { Icon } from './Icon'

type ChatStartersProps = {
  agentName: string
  onSelect: (prompt: string) => void
}

export function ChatStarters({ agentName, onSelect }: ChatStartersProps) {
  return (
    <section aria-label="Быстрые действия" className="chat-starters">
      <span className="chat-starters__label">Быстрый старт</span>
      {starterPrompts(agentName).map((prompt) => <button key={prompt} onClick={() => onSelect(prompt)} type="button">{prompt}<Icon name="arrow-up" width={13} height={13} /></button>)}
    </section>
  )
}
