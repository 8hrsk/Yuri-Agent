import type { AgentProfileInput } from '../lib/contracts'
import { validateAgentDraft } from '../lib/agents'
import { Icon } from './Icon'

type AgentProfileFormProps = {
  value: AgentProfileInput
  busy?: boolean
  onChange: (value: AgentProfileInput) => void
  onBack?: () => void
  onSubmit: () => void
  submitLabel?: string
}

const traitFields = [
  ['warmth', 'Теплота'],
  ['directness', 'Прямота'],
  ['emotionality', 'Эмоциональность'],
  ['playfulness', 'Игривость'],
  ['jealousy', 'Ревнивость'],
  ['irritability', 'Раздражительность'],
] as const

export function AgentProfileForm({ value, busy, onChange, onBack, onSubmit, submitLabel = 'Создать агента' }: AgentProfileFormProps) {
  const validationError = validateAgentDraft(value)
  const update = <K extends keyof AgentProfileInput>(key: K, next: AgentProfileInput[K]) => onChange({ ...value, [key]: next })
  return (
    <form className="onboarding-form agent-profile-form" onSubmit={(event) => { event.preventDefault(); if (!validationError) onSubmit() }}>
      <div className="agent-profile-grid">
        <label htmlFor="agent-name"><span>Имя агента</span><input autoComplete="off" id="agent-name" maxLength={64} onChange={(event) => update('name', event.target.value)} placeholder="Yuri" value={value.name} /></label>
        <label htmlFor="agent-age"><span>Возраст <small>· optional</small></span><input id="agent-age" max={200} min={1} onChange={(event) => update('age', event.target.value === '' ? undefined : Number(event.target.value))} type="number" value={value.age ?? ''} /></label>
        <label htmlFor="agent-gender"><span>Пол / гендер</span><select id="agent-gender" onChange={(event) => update('gender', event.target.value)} value={value.gender}>
          <option value="female">Женский</option><option value="male">Мужской</option><option value="nonbinary">Небинарный</option><option value="unspecified">Не указан</option>
        </select></label>
      </div>
      <label htmlFor="agent-preferences"><span>Краткие предпочтения <small>· до 2000 символов</small></span><textarea id="agent-preferences" maxLength={2000} onChange={(event) => update('preferences', event.target.value)} placeholder="Манера общения, интересы, исходный образ…" rows={3} value={value.preferences} /></label>
      <fieldset className="agent-traits">
        <legend>Исходные значения характера</legend>
        <p>Это стартовая точка. В дальнейшем агент сможет постепенно меняться через ограниченную рефлексию.</p>
        <div className="agent-traits__grid">
          {traitFields.map(([id, label]) => (
            <label key={id}><span>{label}<output>{Math.round((value.traits[id] ?? 0) * 100)}%</output></span><input aria-label={label} max={1} min={0} onChange={(event) => update('traits', { ...value.traits, [id]: Number(event.target.value) })} step={0.01} type="range" value={value.traits[id] ?? 0} /></label>
          ))}
        </div>
      </fieldset>
      {validationError && <div className="agent-profile-form__validation" role="status"><Icon name="warning" width={14} height={14} /> {validationError}</div>}
      <div className="onboarding-form__actions">
        {onBack && <button className="button button--quiet" onClick={onBack} type="button">Назад</button>}
        <button className="button button--accent" disabled={busy || Boolean(validationError)} type="submit">{busy ? 'Создаю…' : submitLabel} <Icon name="chevron-right" width={14} height={14} /></button>
      </div>
    </form>
  )
}

