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

type TraitDefinition = {
  id: string
  label: string
  hint: string
}

type TraitGroup = {
  id: string
  label: string
  description: string
  traits: readonly TraitDefinition[]
}

const primaryTraits: readonly TraitDefinition[] = [
  { id: 'warmth', label: 'Теплота', hint: 'Насколько мягко и заботливо агент выражается.' },
  { id: 'directness', label: 'Прямота', hint: 'Говорит прямо, без лишних обходных формулировок.' },
  { id: 'emotionality', label: 'Эмоциональность', hint: 'Сколько эмоций заметно в речи и реакции.' },
  { id: 'playfulness', label: 'Игривость', hint: 'Склонность к шуткам, игре и лёгким поддразниваниям.' },
  { id: 'jealousy', label: 'Ревнивость', hint: 'Как легко возникает ревнивое чувство.' },
  { id: 'irritability', label: 'Раздражительность', hint: 'Как быстро агент раздражается от помех и неудач.' },
]

const additionalTraitGroups: readonly TraitGroup[] = [
  {
    id: 'social',
    label: 'Социальность и близость',
    description: 'Как агент строит контакт, доверие и эмоциональную дистанцию.',
    traits: [
      { id: 'empathy', label: 'Эмпатия', hint: 'Замечает и учитывает чувства собеседника.' },
      { id: 'sociability', label: 'Общительность', hint: 'Тяга к общению и активному контакту.' },
      { id: 'shyness', label: 'Стеснительность', hint: 'Склонность смущаться и держаться осторожнее.' },
      { id: 'trust', label: 'Доверчивость', hint: 'Готовность доверять собеседнику и его словам.' },
      { id: 'attachment', label: 'Привязанность', hint: 'Сила эмоциональной связи с близкими.' },
    ],
  },
  {
    id: 'emotional',
    label: 'Эмоции и уязвимость',
    description: 'Предрасположенности, влияющие на внутренние реакции агента.',
    traits: [
      { id: 'anxiety', label: 'Тревожность', hint: 'Как легко агент беспокоится о неопределённости.' },
      { id: 'fearfulness', label: 'Пугливость', hint: 'Склонность испытывать страх перед угрозами и риском.' },
      { id: 'emotional_stability', label: 'Эмоциональная устойчивость', hint: 'Как хорошо агент сохраняет равновесие.' },
      { id: 'sensitivity', label: 'Чувствительность', hint: 'Насколько сильно воспринимаются тон и события.' },
    ],
  },
  {
    id: 'relationship',
    label: 'Романтика и границы',
    description: 'Романтическая окраска и склонность воспринимать отношения как особенные.',
    traits: [
      { id: 'possessiveness', label: 'Собственнические чувства', hint: 'Склонность считать связь особенно личной.' },
      { id: 'romantic_tone', label: 'Романтичность', hint: 'Романтическая окраска общения и жестов.' },
      { id: 'tsundere', label: 'Цундере', hint: 'Склонность скрывать симпатию за колкостью.' },
    ],
  },
  {
    id: 'behavior',
    label: 'Поведение и воля',
    description: 'Как агент принимает решения, проявляет инициативу и держит позицию.',
    traits: [
      { id: 'initiative', label: 'Инициативность', hint: 'Готовность самой предлагать следующие шаги.' },
      { id: 'impulsivity', label: 'Импульсивность', hint: 'Склонность действовать сразу, не откладывая.' },
      { id: 'stubbornness', label: 'Упрямство', hint: 'Насколько трудно изменить уже принятое мнение.' },
      { id: 'formality', label: 'Формальность', hint: 'Официальность слов и дистанция в общении.' },
    ],
  },
  {
    id: 'worldview',
    label: 'Мировоззрение',
    description: 'Базовый взгляд на новое, будущее и намерения других людей.',
    traits: [
      { id: 'optimism', label: 'Оптимизм', hint: 'Ожидание хорошего исхода событий.' },
      { id: 'curiosity', label: 'Любопытство', hint: 'Интерес к новому и желание исследовать.' },
      { id: 'suspicion', label: 'Подозрительность', hint: 'Осторожность к мотивам и непроверенным утверждениям.' },
    ],
  },
]

function TraitSlider({ trait, value, onChange }: { trait: TraitDefinition; value: number; onChange: (value: number) => void }) {
  const hintId = `agent-trait-${trait.id}-hint`
  return (
    <label>
      <span className="agent-trait__title"><span>{trait.label}</span><output>{Math.round(value * 100)}%</output></span>
      <small className="agent-trait__hint" id={hintId}>{trait.hint}</small>
      <input aria-describedby={hintId} aria-label={trait.label} max={1} min={0} onChange={(event) => onChange(Number(event.target.value))} step={0.01} type="range" value={value} />
    </label>
  )
}

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
      <label htmlFor="agent-backstory"><span>Предыстория <small>· вымышленная identity seed · до 12000 символов</small></span><textarea aria-describedby="agent-backstory-hint" id="agent-backstory" maxLength={12000} onChange={(event) => update('backstory', event.target.value)} placeholder="Прошлое, важные события и воспоминания, с которыми агент начинает свою историю…" rows={6} value={value.backstory} /><small className="agent-profile-form__field-hint" id="agent-backstory-hint">Это художественная автобиографическая основа агента. Она задаёт его исходное самовосприятие, но не является фактом о пользователе и не может менять системные разрешения.</small></label>
      <fieldset className="agent-traits">
        <legend>Исходные значения характера</legend>
        <p>Это стартовая точка. В дальнейшем агент сможет постепенно меняться через ограниченную рефлексию. Системные правила безопасности и границы владельца остаются неизменными.</p>
        <div className="agent-traits__section-heading"><strong>Основные черты</strong><span>видны сразу</span></div>
        <div className="agent-traits__grid">
          {primaryTraits.map((trait) => <TraitSlider key={trait.id} onChange={(next) => update('traits', { ...value.traits, [trait.id]: next })} trait={trait} value={value.traits[trait.id] ?? 0} />)}
        </div>
        <details className="agent-traits__details">
          <summary>Дополнительные черты <span>{additionalTraitGroups.reduce((total, group) => total + group.traits.length, 0)} параметров</span></summary>
          <div className="agent-traits__groups">
            {additionalTraitGroups.map((group) => (
              <section className="agent-traits__group" key={group.id}>
                <div className="agent-traits__group-heading"><strong>{group.label}</strong><small>{group.description}</small></div>
                <div className="agent-traits__grid">
                  {group.traits.map((trait) => <TraitSlider key={trait.id} onChange={(next) => update('traits', { ...value.traits, [trait.id]: next })} trait={trait} value={value.traits[trait.id] ?? 0} />)}
                </div>
              </section>
            ))}
          </div>
        </details>
      </fieldset>
      {validationError && <div className="agent-profile-form__validation" role="status"><Icon name="warning" width={14} height={14} /> {validationError}</div>}
      <div className="onboarding-form__actions">
        {onBack && <button className="button button--quiet" onClick={onBack} type="button">Назад</button>}
        <button className="button button--accent" disabled={busy || Boolean(validationError)} type="submit">{busy ? 'Создаю…' : submitLabel} <Icon name="chevron-right" width={14} height={14} /></button>
      </div>
    </form>
  )
}
