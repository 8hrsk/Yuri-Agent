import { useEffect, useMemo, useState } from 'react'

import type { AgentBackstoryEpisode, AgentProfileInput, RelationshipSeedPreset } from '../lib/contracts'
import { agentPresets, applyAgentPreset, relationshipSeeds, saveAgentDraft, validateAgentDraft } from '../lib/agents'
import { Icon } from './Icon'

type AgentProfileFormProps = {
  value: AgentProfileInput
  busy?: boolean
  onChange: (value: AgentProfileInput) => void
  onBack?: () => void
  onSubmit: () => void
  submitLabel?: string
}

type FormStep = 'identity' | 'image' | 'style' | 'traits' | 'dynamics' | 'relationship' | 'backstory' | 'review'

type SliderDefinition = {
  id: string
  label: string
  hint: string
  low?: string
  high?: string
}

type TraitGroup = {
  id: string
  label: string
  description: string
  traits: readonly SliderDefinition[]
}

const stepLabels: Record<FormStep, string> = {
  identity: 'Identity', image: 'Образ', style: 'Манера', traits: 'Характер',
  dynamics: 'Эмоции', relationship: 'Отношения', backstory: 'Backstory', review: 'Review',
}

const quickSteps: readonly FormStep[] = ['identity', 'image', 'backstory', 'review']
const advancedSteps: readonly FormStep[] = ['identity', 'image', 'style', 'traits', 'dynamics', 'relationship', 'backstory', 'review']

const primaryTraits: readonly SliderDefinition[] = [
  { id: 'warmth', label: 'Теплота', hint: 'Насколько мягко и заботливо агент выражается.', low: 'прохладно', high: 'очень тепло' },
  { id: 'directness', label: 'Прямота', hint: 'Говорит прямо, без лишних обходных формулировок.', low: 'дипломатично', high: 'предельно прямо' },
  { id: 'emotionality', label: 'Эмоциональность', hint: 'Сколько эмоций заметно в речи и реакции.', low: 'сдержанно', high: 'ярко' },
  { id: 'playfulness', label: 'Игривость', hint: 'Склонность к шуткам, игре и лёгким поддразниваниям.', low: 'серьёзно', high: 'игриво' },
  { id: 'jealousy', label: 'Ревнивость', hint: 'Как легко возникает ревнивое чувство.', low: 'почти нет', high: 'возникает легко' },
  { id: 'irritability', label: 'Раздражительность', hint: 'Как быстро агент раздражается от помех и неудач.', low: 'терпеливо', high: 'вспыльчиво' },
]

const additionalTraitGroups: readonly TraitGroup[] = [
  { id: 'social', label: 'Социальность и близость', description: 'Контакт, доверие и эмоциональная дистанция.', traits: [
    { id: 'empathy', label: 'Эмпатия', hint: 'Замечает и учитывает чувства собеседника.' },
    { id: 'sociability', label: 'Общительность', hint: 'Тяга к общению и активному контакту.' },
    { id: 'shyness', label: 'Стеснительность', hint: 'Склонность смущаться и держаться осторожнее.' },
    { id: 'trust', label: 'Доверчивость', hint: 'Готовность доверять собеседнику и его словам.' },
    { id: 'attachment', label: 'Привязанность', hint: 'Сила эмоциональной связи с близкими.' },
  ] },
  { id: 'emotional', label: 'Эмоции и уязвимость', description: 'Устойчивые предрасположенности внутренних реакций.', traits: [
    { id: 'anxiety', label: 'Тревожность', hint: 'Как легко агент беспокоится о неопределённости.' },
    { id: 'fearfulness', label: 'Пугливость', hint: 'Склонность испытывать страх перед угрозами и риском.' },
    { id: 'emotional_stability', label: 'Эмоциональная устойчивость', hint: 'Как хорошо агент сохраняет равновесие.' },
    { id: 'sensitivity', label: 'Чувствительность', hint: 'Насколько сильно воспринимаются тон и события.' },
  ] },
  { id: 'relationship', label: 'Романтика и границы', description: 'Романтическая окраска и чувство особенности связи.', traits: [
    { id: 'possessiveness', label: 'Собственнические чувства', hint: 'Склонность считать связь особенно личной.' },
    { id: 'romantic_tone', label: 'Романтичность', hint: 'Романтическая окраска общения и жестов.' },
    { id: 'tsundere', label: 'Цундере', hint: 'Склонность скрывать симпатию за колкостью.' },
  ] },
  { id: 'behavior', label: 'Поведение и воля', description: 'Решения, инициатива и устойчивость позиции.', traits: [
    { id: 'initiative', label: 'Инициативность', hint: 'Готовность самой предлагать следующие шаги.' },
    { id: 'impulsivity', label: 'Импульсивность', hint: 'Склонность действовать сразу, не откладывая.' },
    { id: 'stubbornness', label: 'Упрямство', hint: 'Насколько трудно изменить уже принятое мнение.' },
    { id: 'formality', label: 'Формальность', hint: 'Официальность слов и дистанция в общении.' },
  ] },
  { id: 'worldview', label: 'Мировоззрение', description: 'Взгляд на новое, будущее и намерения других.', traits: [
    { id: 'optimism', label: 'Оптимизм', hint: 'Ожидание хорошего исхода событий.' },
    { id: 'curiosity', label: 'Любопытство', hint: 'Интерес к новому и желание исследовать.' },
    { id: 'suspicion', label: 'Подозрительность', hint: 'Осторожность к мотивам и непроверенным утверждениям.' },
  ] },
]

const styleSliders: readonly SliderDefinition[] = [
  { id: 'verbosity', label: 'Подробность', hint: 'Обычная длина и детализация ответа.', low: 'очень кратко', high: 'подробно' },
  { id: 'softness', label: 'Мягкость', hint: 'Насколько бережно формулируется критика.', low: 'резко', high: 'бережно' },
  { id: 'humor', label: 'Юмор', hint: 'Частота уместных шуток и игры слов.', low: 'серьёзно', high: 'часто шутит' },
  { id: 'figurativeness', label: 'Образность', hint: 'Метафоры и художественные сравнения.', low: 'буквально', high: 'образно' },
  { id: 'expressiveness', label: 'Экспрессивность', hint: 'Насколько заметно выражается внутреннее состояние.', low: 'сдержанно', high: 'ярко' },
  { id: 'supportiveness', label: 'Поддержка', hint: 'Склонность сначала поддержать, а затем критиковать.', low: 'критично', high: 'поддерживающе' },
  { id: 'formality', label: 'Формальность речи', hint: 'Дистанция и официальность формулировок.', low: 'неформально', high: 'официально' },
  { id: 'teasing', label: 'Поддразнивание', hint: 'Лёгкие колкости без унижения.', low: 'никогда', high: 'часто' },
  { id: 'emojiFrequency', label: 'Эмодзи', hint: 'Частота эмоциональных визуальных акцентов.', low: 'без эмодзи', high: 'часто' },
  { id: 'flirtation', label: 'Флирт', hint: 'Допустимая романтическая игривость.', low: 'нейтрально', high: 'явно' },
  { id: 'conversationalInitiative', label: 'Инициатива в диалоге', hint: 'Предложения следующих шагов и встречные вопросы.', low: 'реактивно', high: 'проактивно' },
]

const dynamicsSliders: readonly SliderDefinition[] = [
  { id: 'reactivity', label: 'Реактивность', hint: 'Как легко событие вызывает эмоцию.', low: 'инертно', high: 'реагирует легко' },
  { id: 'responseIntensity', label: 'Сила отклика', hint: 'Пиковая интенсивность эмоциональной реакции.', low: 'слабо', high: 'сильно' },
  { id: 'recoverySpeed', label: 'Восстановление', hint: 'Скорость возвращения к равновесию.', low: 'медленно', high: 'быстро' },
  { id: 'positivePersistence', label: 'Длительность позитива', hint: 'Как долго сохраняются приятные переживания.' },
  { id: 'negativePersistence', label: 'Длительность негатива', hint: 'Как долго сохраняются обида и раздражение.' },
  { id: 'expression', label: 'Открытость чувств', hint: 'Насколько прямо агент показывает эмоции.', low: 'скрывает', high: 'выражает' },
  { id: 'masking', label: 'Маскирование', hint: 'Склонность скрывать чувства за спокойствием или юмором.', low: 'не скрывает', high: 'маскирует' },
]

const relationshipDimensions: readonly SliderDefinition[] = [
  { id: 'trust', label: 'Доверие', hint: 'Исходное доверие к владельцу.' },
  { id: 'respect', label: 'Уважение', hint: 'Исходное признание компетентности и границ.' },
  { id: 'closeness', label: 'Близость', hint: 'Ощущение эмоциональной дистанции.' },
  { id: 'attachment', label: 'Привязанность', hint: 'Исходная сила связи.' },
  { id: 'reliability', label: 'Надёжность', hint: 'Насколько владелец воспринимается надёжным.' },
  { id: 'gratitude', label: 'Благодарность', hint: 'Исходная благодарность владельцу.' },
  { id: 'irritation', label: 'Раздражение', hint: 'Исходное накопленное раздражение.' },
  { id: 'jealousy', label: 'Ревность', hint: 'Исходное состояние ревности в этой связи.' },
  { id: 'resentment', label: 'Обида', hint: 'Исходная обида в истории отношений.' },
]

function levelLabel(value: number): string {
  if (value <= 0.2) return 'очень низко'
  if (value <= 0.4) return 'низко'
  if (value <= 0.6) return 'умеренно'
  if (value <= 0.8) return 'высоко'
  return 'очень высоко'
}

function ProfileSlider({ definition, value, onChange }: { definition: SliderDefinition; value: number; onChange: (value: number) => void }) {
  const hintId = `agent-slider-${definition.id}-hint`
  return (
    <label className="agent-slider">
      <span className="agent-trait__title"><span>{definition.label}</span><output>{levelLabel(value)} · {Math.round(value * 100)}%</output></span>
      <small className="agent-trait__hint" id={hintId}>{definition.hint}</small>
      <input aria-describedby={hintId} aria-label={definition.label} max={1} min={0} onChange={(event) => onChange(Number(event.target.value))} step={0.01} type="range" value={value} />
      <span aria-hidden="true" className="agent-slider__extremes"><span>{definition.low ?? 'минимум'}</span><span>{definition.high ?? 'максимум'}</span></span>
    </label>
  )
}

function IdentityStep({ value, update }: { value: AgentProfileInput; update: <K extends keyof AgentProfileInput>(key: K, next: AgentProfileInput[K]) => void }) {
  const identity = value.personalization.identity
  const updateIdentity = (patch: Partial<typeof identity>) => update('personalization', { ...value.personalization, identity: { ...identity, ...patch } })
  return <section aria-labelledby="agent-step-identity" className="agent-wizard__panel">
    <div className="agent-wizard__heading"><span>01 · IDENTITY</span><h3 id="agent-step-identity">Кто этот агент?</h3><p>Имя и базовая identity задаются владельцем и не переписываются фоновой рефлексией.</p></div>
    <div className="agent-profile-grid">
      <label htmlFor="agent-name"><span>Имя агента</span><input autoFocus autoComplete="off" id="agent-name" maxLength={64} onChange={(event) => update('name', event.target.value)} placeholder="Yuri" value={value.name} /></label>
      <label htmlFor="agent-age"><span>Возраст <small>· optional</small></span><input id="agent-age" max={200} min={1} onChange={(event) => update('age', event.target.value === '' ? undefined : Number(event.target.value))} type="number" value={value.age ?? ''} /></label>
      <label htmlFor="agent-gender"><span>Пол / гендер</span><select id="agent-gender" onChange={(event) => update('gender', event.target.value)} value={value.gender}><option value="female">Женский</option><option value="male">Мужской</option><option value="nonbinary">Небинарный</option><option value="unspecified">Не указан</option></select></label>
    </div>
    <div className="agent-profile-grid agent-profile-grid--identity">
      <label htmlFor="agent-language"><span>Основной язык</span><input id="agent-language" maxLength={64} onChange={(event) => updateIdentity({ preferredLanguage: event.target.value })} value={identity.preferredLanguage} /></label>
      <label htmlFor="agent-pronouns"><span>Местоимения</span><input id="agent-pronouns" maxLength={64} onChange={(event) => updateIdentity({ pronouns: event.target.value })} placeholder="она/её" value={identity.pronouns} /></label>
      <label htmlFor="agent-user-address"><span>Обращение к вам</span><input id="agent-user-address" maxLength={128} onChange={(event) => updateIdentity({ userAddress: event.target.value })} placeholder="по имени, хозяин…" value={identity.userAddress} /></label>
    </div>
  </section>
}

function ImageStep({ value, onChange, update }: { value: AgentProfileInput; onChange: (value: AgentProfileInput) => void; update: <K extends keyof AgentProfileInput>(key: K, next: AgentProfileInput[K]) => void }) {
  const identity = value.personalization.identity
  return <section aria-labelledby="agent-step-image" className="agent-wizard__panel">
    <div className="agent-wizard__heading"><span>02 · ОБРАЗ</span><h3 id="agent-step-image">Выберите отправную точку</h3><p>Preset разворачивается в обычные видимые параметры. В Advanced-режиме каждый из них можно изменить.</p></div>
    <div className="agent-presets" role="radiogroup" aria-label="Архетип агента">
      {agentPresets.map((preset) => <button aria-checked={value.presetId === preset.id} className={value.presetId === preset.id ? 'agent-preset agent-preset--active' : 'agent-preset'} key={preset.id} onClick={() => onChange(applyAgentPreset(value, preset.id))} role="radio" type="button"><strong>{preset.label}</strong><span>{preset.description}</span>{value.presetId === preset.id && <Icon name="check" width={14} height={14} />}</button>)}
    </div>
    <label htmlFor="agent-role"><span>Роль / образ</span><input id="agent-role" maxLength={2000} onChange={(event) => update('personalization', { ...value.personalization, identity: { ...identity, role: event.target.value } })} placeholder="исследовательница, хранительница архива…" value={identity.role} /></label>
    <label htmlFor="agent-preferences"><span>Короткое описание <small>· до 2000 символов</small></span><textarea id="agent-preferences" maxLength={2000} onChange={(event) => onChange({ ...value, preferences: event.target.value, personalization: { ...value.personalization, identity: { ...identity, selfDescription: event.target.value } } })} placeholder="Манера общения, интересы, исходный образ…" rows={4} value={value.preferences} /></label>
  </section>
}

function StyleStep({ value, update }: { value: AgentProfileInput; update: <K extends keyof AgentProfileInput>(key: K, next: AgentProfileInput[K]) => void }) {
  const style = value.personalization.communicationStyle
  return <section aria-labelledby="agent-step-style" className="agent-wizard__panel"><div className="agent-wizard__heading"><span>03 · COMMUNICATION STYLE</span><h3 id="agent-step-style">Как агент говорит?</h3><p>Эти параметры напрямую управляют формой ответа и не меняют факты, permissions или качество выполнения.</p></div><div className="agent-traits__grid">{styleSliders.map((definition) => <ProfileSlider definition={definition} key={definition.id} onChange={(next) => update('personalization', { ...value.personalization, communicationStyle: { ...style, [definition.id]: next } })} value={style[definition.id as keyof typeof style] as number} />)}</div></section>
}

function TraitsStep({ value, update }: { value: AgentProfileInput; update: <K extends keyof AgentProfileInput>(key: K, next: AgentProfileInput[K]) => void }) {
  const slider = (definition: SliderDefinition) => <ProfileSlider definition={definition} key={definition.id} onChange={(next) => update('traits', { ...value.traits, [definition.id]: next })} value={value.traits[definition.id] ?? 0} />
  return <section aria-labelledby="agent-step-traits" className="agent-wizard__panel"><div className="agent-wizard__heading"><span>04 · TEMPERAMENT</span><h3 id="agent-step-traits">Устойчивый характер</h3><p>Это предрасположенности, а не постоянные эмоции. Негативные черты меняют тон, но не надёжность агента.</p></div><div className="agent-traits__grid">{primaryTraits.map(slider)}</div><div className="agent-traits__groups">{additionalTraitGroups.map((group) => <section className="agent-traits__group" key={group.id}><div className="agent-traits__group-heading"><strong>{group.label}</strong><small>{group.description}</small></div><div className="agent-traits__grid">{group.traits.map(slider)}</div></section>)}</div></section>
}

function DynamicsStep({ value, update }: { value: AgentProfileInput; update: <K extends keyof AgentProfileInput>(key: K, next: AgentProfileInput[K]) => void }) {
  const dynamics = value.personalization.emotionalDynamics
  const evolution = value.personalization.evolutionPolicy
  const updateDynamics = (patch: Partial<typeof dynamics>) => update('personalization', { ...value.personalization, emotionalDynamics: { ...dynamics, ...patch } })
  const updateEvolution = (patch: Partial<typeof evolution>) => update('personalization', { ...value.personalization, evolutionPolicy: { ...evolution, ...patch } })
  const triggerText = (emotion: string) => (dynamics.triggers[emotion] ?? []).join('\n')
  const setTriggers = (emotion: string, text: string) => updateDynamics({ triggers: { ...dynamics.triggers, [emotion]: text.split('\n').map((item) => item.trim()).filter(Boolean) } })
  return <section aria-labelledby="agent-step-dynamics" className="agent-wizard__panel"><div className="agent-wizard__heading"><span>05 · EMOTIONAL DYNAMICS</span><h3 id="agent-step-dynamics">Как возникают и проходят чувства?</h3><p>Dynamics описывает правила реакции, а не текущее настроение. После завершённого диалога локальная appraisal-политика ограничивает силу и длительность реакции.</p></div><div className="agent-traits__grid">{dynamicsSliders.map((definition) => <ProfileSlider definition={definition} key={definition.id} onChange={(next) => updateDynamics({ [definition.id]: next })} value={dynamics[definition.id as keyof typeof dynamics] as number} />)}</div><label htmlFor="agent-conflict-style"><span>Стиль конфликта</span><select id="agent-conflict-style" onChange={(event) => updateDynamics({ conflictStyle: event.target.value as typeof dynamics.conflictStyle })} value={dynamics.conflictStyle}><option value="adaptive">Адаптивный</option><option value="withdraw">Отступить и вернуться</option><option value="direct">Прямой разговор</option><option value="cold">Холодная сдержанность</option><option value="humor">Снять напряжение юмором</option></select></label><div className="agent-reflection-controls"><label className="agent-reflection-toggle"><input checked={evolution.reflectionMode === 'enabled'} onChange={(event) => updateEvolution({ reflectionMode: event.target.checked ? 'enabled' : 'disabled' })} type="checkbox" /><span><strong>Фоновая рефлексия</strong><small>Разрешить этому агенту оценивать завершённые события и постепенно обновлять affect.</small></span></label><label htmlFor="agent-reflection-cooldown"><span>Cooldown устойчивых изменений <small>· минут</small></span><input disabled={evolution.reflectionMode === 'disabled'} id="agent-reflection-cooldown" max={10080} min={1} onChange={(event) => updateEvolution({ reflectionCooldownMinutes: Math.max(1, Math.min(10080, Number(event.target.value) || 1)) })} type="number" value={evolution.reflectionCooldownMinutes} /></label></div><div className="agent-trigger-grid">{[['fear', 'Страх'], ['embarrassment', 'Смущение'], ['jealousy', 'Ревность'], ['irritation', 'Раздражение'], ['joy', 'Радость']].map(([id, label]) => <label key={id} htmlFor={`agent-trigger-${id}`}><span>Триггеры: {label}</span><textarea id={`agent-trigger-${id}`} onChange={(event) => setTriggers(id, event.target.value)} placeholder="Один субъективный триггер на строку" rows={3} value={triggerText(id)} /></label>)}</div><label htmlFor="agent-soothing"><span>Что помогает успокоиться <small>· одна стратегия на строку</small></span><textarea id="agent-soothing" onChange={(event) => updateDynamics({ soothingStrategies: event.target.value.split('\n').map((item) => item.trim()).filter(Boolean) })} rows={3} value={dynamics.soothingStrategies.join('\n')} /></label></section>
}

function RelationshipStep({ value, update }: { value: AgentProfileInput; update: <K extends keyof AgentProfileInput>(key: K, next: AgentProfileInput[K]) => void }) {
  const relationship = value.personalization.relationshipSeed
  const selectPreset = (preset: RelationshipSeedPreset) => { const selected = relationshipSeeds[preset]; update('personalization', { ...value.personalization, relationshipSeed: { preset, summary: selected.summary, dimensions: { ...selected.dimensions } } }) }
  return <section aria-labelledby="agent-step-relationship" className="agent-wizard__panel"><div className="agent-wizard__heading"><span>06 · RELATIONSHIP SEED</span><h3 id="agent-step-relationship">С какой истории начинаются отношения?</h3><p>Это исходная связь с владельцем. Дальнейшее relationship state развивается отдельно и независимо.</p></div><div className="agent-relationship-presets">{(Object.entries(relationshipSeeds) as [RelationshipSeedPreset, typeof relationshipSeeds[RelationshipSeedPreset]][]).map(([id, preset]) => <button aria-pressed={relationship.preset === id} className={relationship.preset === id ? 'agent-relationship-preset agent-relationship-preset--active' : 'agent-relationship-preset'} key={id} onClick={() => selectPreset(id)} type="button"><strong>{preset.label}</strong><span>{preset.summary}</span></button>)}</div><label htmlFor="agent-relationship-summary"><span>Субъективное резюме отношений</span><textarea id="agent-relationship-summary" maxLength={2000} onChange={(event) => update('personalization', { ...value.personalization, relationshipSeed: { ...relationship, summary: event.target.value } })} rows={3} value={relationship.summary} /></label><div className="agent-traits__grid">{relationshipDimensions.map((definition) => <ProfileSlider definition={definition} key={definition.id} onChange={(next) => update('personalization', { ...value.personalization, relationshipSeed: { ...relationship, preset: 'custom', dimensions: { ...relationship.dimensions, [definition.id]: next } } })} value={relationship.dimensions[definition.id] ?? 0} />)}</div></section>
}

function emptyEpisode(index: number): AgentBackstoryEpisode {
  return { id: `episode-${Date.now()}-${index + 1}`, title: '', content: '', kind: 'formative', people: [], place: '', emotionalValence: 0, sequence: index + 1 }
}

function BackstoryStep({ value, onChange, update }: { value: AgentProfileInput; onChange: (value: AgentProfileInput) => void; update: <K extends keyof AgentProfileInput>(key: K, next: AgentProfileInput[K]) => void }) {
  const structured = value.personalization.structuredBackstory
  const updateStructured = (patch: Partial<typeof structured>) => update('personalization', { ...value.personalization, structuredBackstory: { ...structured, ...patch } })
  const updateEpisode = (index: number, patch: Partial<AgentBackstoryEpisode>) => updateStructured({ episodes: structured.episodes.map((episode, itemIndex) => itemIndex === index ? { ...episode, ...patch } : episode) })
  return <section aria-labelledby="agent-step-backstory" className="agent-wizard__panel"><div className="agent-wizard__heading"><span>07 · FICTIONAL BACKSTORY</span><h3 id="agent-step-backstory">С каким прошлым агент начинает?</h3><p>Это художественная identity seed. Она не является фактом о пользователе, policy или основанием для permissions.</p></div><label htmlFor="agent-backstory"><span>Свободная предыстория <small>· до 12000 символов</small></span><textarea aria-describedby="agent-backstory-hint" id="agent-backstory" maxLength={12000} onChange={(event) => onChange({ ...value, backstory: event.target.value, personalization: { ...value.personalization, structuredBackstory: { ...structured, narrative: event.target.value } } })} placeholder="Прошлое, важные события и воспоминания…" rows={7} value={value.backstory} /><small className="agent-profile-form__field-hint" id="agent-backstory-hint">Агент может эмоционально интерпретировать это как собственное прошлое, но исходный текст меняет только владелец.</small></label><label htmlFor="agent-backstory-summary"><span>Короткое self-summary <small>· до 2000 символов</small></span><textarea id="agent-backstory-summary" maxLength={2000} onChange={(event) => updateStructured({ summary: event.target.value })} rows={3} value={structured.summary} /></label><div className="agent-episodes"><div className="agent-episodes__heading"><div><strong>Ключевые эпизоды</strong><small>Позднее они станут отдельными fictional memories с provenance.</small></div><button className="button button--quiet" onClick={() => updateStructured({ episodes: [...structured.episodes, emptyEpisode(structured.episodes.length)] })} type="button"><Icon name="plus" width={13} height={13} /> Добавить эпизод</button></div>{structured.episodes.map((episode, index) => <article className="agent-episode" key={episode.id}><div className="agent-episode__top"><strong>Эпизод {index + 1}</strong><button aria-label={`Удалить эпизод ${index + 1}`} onClick={() => updateStructured({ episodes: structured.episodes.filter((_, itemIndex) => itemIndex !== index) })} type="button"><Icon name="x" width={13} height={13} /></button></div><div className="agent-profile-grid"><label><span>Название</span><input maxLength={256} onChange={(event) => updateEpisode(index, { title: event.target.value })} value={episode.title} /></label><label><span>Место</span><input maxLength={256} onChange={(event) => updateEpisode(index, { place: event.target.value })} value={episode.place} /></label><label><span>Люди <small>· через запятую</small></span><input onChange={(event) => updateEpisode(index, { people: event.target.value.split(',').map((item) => item.trim()).filter(Boolean) })} value={episode.people.join(', ')} /></label></div><label><span>Что произошло</span><textarea maxLength={4000} onChange={(event) => updateEpisode(index, { content: event.target.value })} rows={4} value={episode.content} /></label><ProfileSlider definition={{ id: `valence-${index}`, label: 'Эмоциональная окраска', hint: 'От болезненного воспоминания к очень приятному.', low: 'болезненно', high: 'приятно' }} onChange={(next) => updateEpisode(index, { emotionalValence: next * 2 - 1 })} value={(episode.emotionalValence + 1) / 2} /></article>)}</div></section>
}

function ReviewStep({ value }: { value: AgentProfileInput }) {
  const selectedPreset = agentPresets.find((preset) => preset.id === value.presetId)
  const strongestTraits = Object.entries(value.traits).sort((a, b) => b[1] - a[1]).slice(0, 8)
  return <section aria-labelledby="agent-step-review" className="agent-wizard__panel"><div className="agent-wizard__heading"><span>08 · REVIEW</span><h3 id="agent-step-review">Проверьте будущего агента</h3><p>После создания эти значения станут owner-authored reset baseline. Mutable persona сможет меняться только в заданных пределах.</p></div><div className="agent-review"><article><span>Identity</span><strong>{value.name || 'Без имени'} · {value.age ?? 'возраст не указан'} · {value.gender}</strong><p>{value.personalization.identity.role || 'Роль не задана'} · {value.personalization.identity.pronouns || 'местоимения не заданы'} · язык {value.personalization.identity.preferredLanguage}</p></article><article><span>Образ</span><strong>{selectedPreset?.label ?? 'Свой профиль'}</strong><p>{value.preferences || 'Без короткого описания.'}</p></article><article><span>Сильнее выражены</span><div className="agent-review__chips">{strongestTraits.map(([trait, amount]) => <span key={trait}>{trait} · {levelLabel(amount)}</span>)}</div></article><article><span>Эмоциональная динамика</span><strong>{value.personalization.emotionalDynamics.conflictStyle}</strong><p>Реактивность {levelLabel(value.personalization.emotionalDynamics.reactivity)}, восстановление {levelLabel(value.personalization.emotionalDynamics.recoverySpeed)}, выражение {levelLabel(value.personalization.emotionalDynamics.expression)}.</p></article><article><span>Отношения</span><strong>{relationshipSeeds[value.personalization.relationshipSeed.preset].label}</strong><p>{value.personalization.relationshipSeed.summary}</p></article><article><span>Backstory</span><strong>{value.personalization.structuredBackstory.episodes.length} эпизодов</strong><p>{value.personalization.structuredBackstory.summary || value.backstory || 'Предыстория не задана.'}</p></article></div></section>
}

export function AgentProfileForm({ value, busy, onChange, onBack, onSubmit, submitLabel = 'Создать агента' }: AgentProfileFormProps) {
  const [step, setStep] = useState<FormStep>('identity')
  const steps = useMemo(() => value.creationMode === 'advanced' ? advancedSteps : quickSteps, [value.creationMode])
  const stepIndex = Math.max(0, steps.indexOf(step))
  const validationError = validateAgentDraft(value)
  const update = <K extends keyof AgentProfileInput>(key: K, next: AgentProfileInput[K]) => onChange({ ...value, [key]: next })

  useEffect(() => { saveAgentDraft(value) }, [value])
  useEffect(() => { if (!steps.includes(step)) setStep('image') }, [step, steps])

  const previous = () => { if (stepIndex === 0) onBack?.(); else setStep(steps[stepIndex - 1]) }
  const next = () => { if (stepIndex < steps.length - 1) setStep(steps[stepIndex + 1]) }
  const basicInvalid = !value.name.trim() || !value.gender.trim() || (value.age !== undefined && (!Number.isInteger(value.age) || value.age < 1 || value.age > 200))
  const submit = () => { if (step === 'review') { if (!validationError) onSubmit() } else if (!(step === 'identity' && basicInvalid)) next() }

  return (
    <form className="onboarding-form agent-profile-form agent-wizard" onSubmit={(event) => { event.preventDefault(); submit() }}>
      <div className="agent-wizard__toolbar"><div aria-label="Режим создания" className="agent-wizard__modes" role="group"><button aria-pressed={value.creationMode === 'quick'} onClick={() => update('creationMode', 'quick')} type="button">Quick</button><button aria-pressed={value.creationMode === 'advanced'} onClick={() => update('creationMode', 'advanced')} type="button">Advanced</button></div><span><Icon name="check" width={12} height={12} /> Черновик сохраняется локально</span></div>
      <nav aria-label="Шаги создания агента" className="agent-wizard__steps">{steps.map((item, index) => <button aria-current={item === step ? 'step' : undefined} className={item === step ? 'agent-wizard__step agent-wizard__step--active' : index < stepIndex ? 'agent-wizard__step agent-wizard__step--done' : 'agent-wizard__step'} key={item} onClick={() => setStep(item)} type="button"><span>{index + 1}</span>{stepLabels[item]}</button>)}</nav>
      {step === 'identity' && <IdentityStep update={update} value={value} />}
      {step === 'image' && <ImageStep onChange={onChange} update={update} value={value} />}
      {step === 'style' && <StyleStep update={update} value={value} />}
      {step === 'traits' && <TraitsStep update={update} value={value} />}
      {step === 'dynamics' && <DynamicsStep update={update} value={value} />}
      {step === 'relationship' && <RelationshipStep update={update} value={value} />}
      {step === 'backstory' && <BackstoryStep onChange={onChange} update={update} value={value} />}
      {step === 'review' && <ReviewStep value={value} />}
      {step === 'review' && validationError && <div className="agent-profile-form__validation" role="alert"><Icon name="warning" width={14} height={14} /> {validationError}</div>}
      <div className="onboarding-form__actions"><button className="button button--quiet" onClick={previous} type="button">Назад</button><button className="button button--accent" disabled={busy || (step === 'identity' && basicInvalid) || (step === 'review' && Boolean(validationError))} type="submit">{busy ? 'Создаю…' : step === 'review' ? submitLabel : 'Продолжить'} <Icon name="chevron-right" width={14} height={14} /></button></div>
    </form>
  )
}
