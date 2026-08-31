import { useCallback, useEffect, useMemo, useState } from 'react'

import { createStarterPersonalitySnapshot, createYuriClient, normalizePersonalitySnapshot, subscribePersonaUpdates } from '../lib/client'
import { clonePersonalization } from '../lib/agents'
import type {
  AffectiveDimension,
  AgentPersonalizationProfile,
  AgentProfile,
  AgentProfileInput,
  PersonaTrait,
  PersonaVersion,
  PersonalitySnapshot,
  RelationshipDimension,
  RelationshipVersion,
  SubjectiveOpinion,
} from '../lib/contracts'
import { formatDateTime } from '../lib/datetime'
import { dominantAffectMood } from '../lib/personality'
import { Icon } from './Icon'
import { AgentProfileForm } from './AgentProfileForm'
import { YuriAvatar } from './YuriAvatar'

export type PersonaRelationshipSection = 'personality' | 'relationship'

type PersonaRelationshipViewProps = {
  section: PersonaRelationshipSection
  /**
   * The two sections are two shell destinations. The in-view tabs move the
   * whole shell rather than swapping content behind a nav rail that still
   * highlights the other entry.
   */
  onSelectSection?: (section: PersonaRelationshipSection) => void
}

type Feedback = { kind: 'success' | 'error'; text: string }
type BusyAction = 'loading' | 'evolution' | 'pin' | 'rollback' | 'reset' | 'seed' | undefined

function ownerSeedDraft(agent: AgentProfile, seed: AgentPersonalizationProfile): AgentProfileInput {
  return {
    name: agent.name,
    age: agent.age,
    gender: agent.gender,
    preferences: seed.identity.selfDescription,
    backstory: seed.structuredBackstory.narrative,
    traits: { ...seed.temperament },
    personalization: clonePersonalization(seed),
    creationMode: 'advanced',
    presetId: 'custom',
  }
}

function formatDate(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return formatDateTime(date)
}

function percentage(value: number): string {
  return `${Math.round(value * 100)}%`
}

function signedPercentage(value: number): string {
  const percent = Math.round(value * 100)
  return `${percent > 0 ? '+' : ''}${percent}%`
}

function TraitRow({ trait, onPin, busy }: { trait: PersonaTrait; onPin: (trait: PersonaTrait) => void; busy: boolean }) {
  const min = Math.max(0, Math.min(1, trait.min))
  const max = Math.max(min, Math.min(1, trait.max))
  const value = Math.max(min, Math.min(max, trait.value))
  return (
    <article className="persona-trait">
      <div className="persona-trait__heading">
        <div>
          <strong>{trait.label}</strong>
          {trait.description && <small>{trait.description}</small>}
        </div>
        <div className="persona-trait__value">
          <span>{percentage(value)}</span>
          <button aria-pressed={trait.pinned} className={trait.pinned ? 'persona-pin persona-pin--active' : 'persona-pin'} disabled={busy} onClick={() => onPin(trait)} title={trait.pinned ? 'Открепить trait' : 'Закрепить trait'} type="button">
            <Icon name={trait.pinned ? 'check' : 'spark'} width={12} height={12} />
            {trait.pinned ? 'закреплён' : 'закрепить'}
          </button>
        </div>
      </div>
      <div aria-label={`Диапазон ${trait.label}: от ${percentage(min)} до ${percentage(max)}, текущее значение ${percentage(value)}`} className="persona-trait__scale">
        <span className="persona-trait__bound" style={{ left: `${min * 100}%`, width: `${(max - min) * 100}%` }} />
        <i style={{ left: `${value * 100}%` }} />
      </div>
      <div className="persona-trait__bounds"><span>min {percentage(min)}</span><span>bounded range</span><span>max {percentage(max)}</span></div>
    </article>
  )
}

function OpinionCard({ opinion }: { opinion: SubjectiveOpinion }) {
  const label = opinion.label === 'inference' ? 'INFERENCE · вывод' : 'OPINION · мнение'
  return (
    <article className={`persona-opinion persona-opinion--${opinion.label}`}>
      <div className="persona-opinion__heading">
        <span className={`persona-opinion__label persona-opinion__label--${opinion.label}`}><Icon name="personality" width={13} height={13} /> {label}</span>
        <span className="persona-opinion__confidence">confidence {percentage(opinion.confidence)}</span>
      </div>
      <p><strong>{opinion.subject}:</strong> {opinion.content}</p>
      {opinion.reason && <div className="persona-opinion__reason"><span>Почему</span><span>{opinion.reason}</span></div>}
      <details className="persona-evidence">
        <summary><Icon name="search" width={12} height={12} /> Evidence links · {opinion.evidence.length}</summary>
        {opinion.evidence.length > 0 ? <div className="persona-evidence__list">{opinion.evidence.map((evidence, index) => <div className="persona-evidence__item" key={`${evidence.id ?? evidence.sourceId ?? evidence.sourceType}-${index}`}><span>{evidence.sourceType}{evidence.sourceId ? ` · ${evidence.sourceId}` : ''}</span>{evidence.excerpt && <p>{evidence.excerpt}</p>}</div>)}</div> : <p className="persona-evidence__empty">Ссылки на evidence пока не добавлены.</p>}
      </details>
    </article>
  )
}

function AffectBar({ dimension }: { dimension: AffectiveDimension }) {
  const negative = (dimension.valence ?? (['anger', 'irritation', 'jealousy', 'resentment', 'anxiety', 'boredom'].includes(dimension.id) ? -1 : 1)) < 0
  return <div className={`persona-affect__row${negative ? ' persona-affect__row--negative' : ''}`}><div><span>{dimension.label}</span><strong>{percentage(dimension.value)}</strong></div><div className="persona-affect__scale"><i style={{ width: `${dimension.value * 100}%` }} /></div></div>
}

function RelationshipBar({ dimension }: { dimension: RelationshipDimension }) {
  return <div className="relationship-signal"><div><span>{dimension.label}</span><strong>{percentage(dimension.value)}</strong></div><div className="relationship-signal__scale"><i style={{ width: `${dimension.value * 100}%` }} /></div></div>
}

function VersionItem({ version, currentVersion, labels, onRollback, busy }: { version: PersonaVersion; currentVersion: number; labels: Record<string, string>; onRollback: (version: PersonaVersion) => void; busy: boolean }) {
  const current = version.version === currentVersion
  const changes = Object.entries(version.diff ?? {}).filter((entry): entry is [string, number] => typeof entry[1] === 'number' && Math.abs(entry[1]) >= .001)
  return <article className={`persona-version${current ? ' persona-version--current' : ''}`}>
    <div className="persona-version__marker"><span>v{version.version}</span><i /></div>
    <div className="persona-version__body">
      <div className="persona-version__heading"><strong>{current ? 'Текущая версия' : `Версия ${version.version}`}</strong><time dateTime={version.createdAt}>{formatDate(version.createdAt)}</time></div>
      <p>{version.reason}</p>
      {changes.length > 0 && <div className="relationship-version__diff">{changes.map(([id, value]) => <span className={value < 0 ? 'relationship-version__change relationship-version__change--negative' : 'relationship-version__change'} key={id}>{labels[id] ?? id.replaceAll('_', ' ')} {signedPercentage(value)}</span>)}</div>}
      <div className="persona-version__meta"><span>evidence · {version.evidence.length}</span>{version.authorRunId && <span>run · {version.authorRunId}</span>}{version.parentId && <span>parent · {version.parentId}</span>}</div>
      {!current && <button className="button button--quiet persona-version__rollback" disabled={busy} onClick={() => onRollback(version)} type="button"><Icon name="refresh" width={13} height={13} /> Откатить на v{version.version}</button>}
    </div>
  </article>
}

function RelationshipVersionItem({ version, currentVersion, labels, onRollback, busy }: { version: RelationshipVersion; currentVersion: number; labels: Record<string, string>; onRollback: (version: RelationshipVersion) => void; busy: boolean }) {
  const current = version.version === currentVersion
  const changes = Object.entries(version.diff ?? {}).filter(([, value]) => Math.abs(value) >= 0.001)
  return <article className={`persona-version${current ? ' persona-version--current' : ''}`}>
    <div className="persona-version__marker"><span>v{version.version}</span><i /></div>
    <div className="persona-version__body">
      <div className="persona-version__heading"><strong>{current ? 'Текущая связь' : `Версия ${version.version}`} · {version.operation}</strong><time dateTime={version.createdAt}>{formatDate(version.createdAt)}</time></div>
      <p>{version.reason}</p>
      {changes.length > 0 && <div className="relationship-version__diff">{changes.map(([id, value]) => <span className={value < 0 ? 'relationship-version__change relationship-version__change--negative' : 'relationship-version__change'} key={id}>{labels[id] ?? id} {signedPercentage(value)}</span>)}</div>}
      <div className="persona-version__meta"><span>evidence · {version.evidence.length}</span>{version.authorRunId && <span>run · {version.authorRunId}</span>}{version.parentId && <span>parent · {version.parentId}</span>}</div>
      {!current && <button className="button button--quiet persona-version__rollback" disabled={busy} onClick={() => onRollback(version)} type="button"><Icon name="refresh" width={13} height={13} /> Вернуть состояние v{version.version}</button>}
    </div>
  </article>
}

function SnapshotHeader({ snapshot, busy, onEvolution }: { snapshot: PersonalitySnapshot; busy: boolean; onEvolution: () => void }) {
  const mood = dominantAffectMood(snapshot.affect)
  const moodClass = `persona-mood persona-mood--${mood}`
  return <section className="persona-overview">
    <div className="persona-profile-card">
      <div className="persona-profile-card__heading"><div><span className="section-heading__overline">MUTABLE PERSONA</span><h2>Профиль Yuri</h2></div><span className="persona-version-chip">v{snapshot.currentVersion}</span></div>
      <div className="persona-profile-card__body"><YuriAvatar affect={snapshot.affect} label={`Yuri · ${snapshot.affect.mood}`} size="lg" state="idle" /><div className="persona-profile-card__copy"><strong>Версия {snapshot.currentVersion}</strong><p>Traits ограничены диапазонами и меняются только через versioned reflection. Identity seed и policy остаются неизменными.</p><span className="persona-profile-card__updated">Последняя рефлексия · {formatDate(snapshot.lastReflectionAt)}</span></div></div>
      <label className="persona-evolution-toggle"><button aria-checked={snapshot.autoEvolution} className={snapshot.autoEvolution ? 'toggle toggle--on' : 'toggle'} disabled={busy} onClick={onEvolution} role="switch" type="button"><i /></button><span><strong>Автоэволюция личности</strong><small>{snapshot.autoEvolution ? 'Bounded reflection может предложить новые версии.' : 'Новые версии создаются только вручную.'}</small></span><span className={snapshot.autoEvolution ? 'persona-evolution-toggle__status persona-evolution-toggle__status--on' : 'persona-evolution-toggle__status'}>{snapshot.autoEvolution ? 'ON' : 'OFF'}</span></label>
    </div>
    <div className="persona-mood-card">
      <div className="persona-mood-card__heading"><div><span className="section-heading__overline">AFFECTIVE STATE</span><h2>Настроение</h2></div><YuriAvatar affect={snapshot.affect} label={`Аффект: ${snapshot.affect.mood}`} size="sm" state="idle" /></div>
      <div className={moodClass}><span className="persona-mood__dot" /><strong>{snapshot.affect.mood}</strong><span>{signedPercentage(snapshot.affect.valence)} valence</span></div>
      <div className="persona-mood__scale"><i style={{ left: `${((snapshot.affect.valence + 1) / 2) * 100}%` }} /></div>
      <div className="persona-mood__bounds"><span>negative</span><span>positive</span></div>
      <p>Это моделируемое внутреннее состояние персонажа, а не утверждение о сознании модели. Affect не влияет на разрешения и security policy.</p>
    </div>
  </section>
}

function PersonalityLayerMap({ snapshot, ownerSeed, section }: { snapshot: PersonalitySnapshot; ownerSeed?: AgentPersonalizationProfile; section: PersonaRelationshipSection }) {
  const cards = [
    { id: 'owner_seed', label: 'OWNER SEED', value: ownerSeed ? `v${ownerSeed.version}` : '—', detail: 'Владелец · reset baseline' },
    { id: 'mutable_persona', label: 'MUTABLE PERSONA', value: `v${snapshot.currentVersion}`, detail: 'Bounded развитие характера' },
    { id: 'relationship', label: 'RELATIONSHIP', value: `v${snapshot.relationship.version}`, detail: 'Связь с текущим subject' },
    { id: 'opinion', label: 'OPINION / INFERENCE', value: String(snapshot.opinions.length), detail: 'Субъективно · может ошибаться' },
    { id: 'affect', label: 'CURRENT AFFECT', value: snapshot.affect.mood, detail: 'Временное состояние' },
  ]
  return <section aria-label="Слои личности агента" className="personality-layer-map">{cards.map((card) => <article className={`personality-layer personality-layer--${card.id}${section === 'relationship' && (card.id === 'relationship' || card.id === 'opinion') ? ' personality-layer--active' : section === 'personality' && (card.id === 'owner_seed' || card.id === 'mutable_persona' || card.id === 'affect') ? ' personality-layer--active' : ''}`} key={card.id}><span>{card.label}</span><strong>{card.value}</strong><small>{card.detail}</small></article>)}</section>
}

export function PersonaRelationshipView({ section, onSelectSection }: PersonaRelationshipViewProps) {
  const client = useMemo(() => createYuriClient(), [])
  const [snapshot, setSnapshot] = useState<PersonalitySnapshot>(() => createStarterPersonalitySnapshot())
  const [busy, setBusy] = useState<BusyAction>('loading')
  const [error, setError] = useState<string>()
  const [feedback, setFeedback] = useState<Feedback>()
  const [activeAgent, setActiveAgent] = useState<AgentProfile>()
  const [ownerSeed, setOwnerSeed] = useState<AgentPersonalizationProfile>()
  const [seedDraft, setSeedDraft] = useState<AgentProfileInput>()
  const [seedEditorOpen, setSeedEditorOpen] = useState(false)
  const [seedReason, setSeedReason] = useState('')

  const load = useCallback(async () => {
    setBusy('loading')
    setError(undefined)
    try {
      const [nextSnapshot, agent, seed] = await Promise.all([
        client.getPersonaSnapshot(),
        client.getActiveAgent(),
        client.getActiveAgentPersonalization(),
      ])
      setSnapshot(normalizePersonalitySnapshot(nextSnapshot))
      setActiveAgent(agent)
      setOwnerSeed(seed)
      if (agent && seed) setSeedDraft((current) => current ?? ownerSeedDraft(agent, seed))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Не удалось загрузить состояние личности.')
    } finally {
      setBusy(undefined)
    }
  }, [client])

  useEffect(() => { void load() }, [load])

  useEffect(() => subscribePersonaUpdates((next) => setSnapshot(next)), [])

  const updateSnapshot = (next: PersonalitySnapshot | undefined) => {
    if (next) setSnapshot(normalizePersonalitySnapshot(next))
  }

  const toggleAutoEvolution = async () => {
    setBusy('evolution')
    setFeedback(undefined)
    try {
      updateSnapshot(await client.setPersonaAutoEvolution(!snapshot.autoEvolution))
      setFeedback({ kind: 'success', text: `Автоэволюция ${snapshot.autoEvolution ? 'выключена' : 'включена'}.` })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось изменить режим автоэволюции.' })
    } finally {
      setBusy(undefined)
    }
  }

  const togglePinned = async (trait: PersonaTrait) => {
    setBusy('pin')
    setFeedback(undefined)
    try {
      updateSnapshot(await client.setPersonaTraitPinned(trait.id, !trait.pinned))
      setFeedback({ kind: 'success', text: trait.pinned ? `Trait «${trait.label}» откреплён.` : `Trait «${trait.label}» закреплён.` })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось изменить закрепление trait.' })
    } finally {
      setBusy(undefined)
    }
  }

  const rollback = async (version: PersonaVersion) => {
    setBusy('rollback')
    setFeedback(undefined)
    try {
      updateSnapshot(await client.rollbackPersona(version.id))
      setFeedback({ kind: 'success', text: `Persona откатили на версию ${version.version}. История сохранена.` })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось откатить persona.' })
    } finally {
      setBusy(undefined)
    }
  }

  const reset = async () => {
    setBusy('reset')
    setFeedback(undefined)
    try {
      updateSnapshot(await client.resetPersona())
      setFeedback({ kind: 'success', text: 'Persona сброшена к identity seed. История изменений сохранена.' })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось сбросить persona.' })
    } finally {
      setBusy(undefined)
    }
  }

  const rollbackRelationship = async (version: RelationshipVersion) => {
    setBusy('rollback')
    setFeedback(undefined)
    try {
      updateSnapshot(await client.rollbackRelationship(version.id))
      setFeedback({ kind: 'success', text: `Связь возвращена к состоянию версии ${version.version}. История сохранена.` })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось вернуть состояние связи.' })
    } finally {
      setBusy(undefined)
    }
  }

  const resetRelationship = async () => {
    setBusy('reset')
    setFeedback(undefined)
    try {
      updateSnapshot(await client.resetRelationship())
      setFeedback({ kind: 'success', text: 'Связь сброшена к текущему relationship seed. Persona и память не изменены.' })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось сбросить состояние связи.' })
    } finally {
      setBusy(undefined)
    }
  }

  const saveOwnerSeed = async () => {
    if (!ownerSeed || !seedDraft) return
    const reason = seedReason.trim()
    if (!reason) {
      setFeedback({ kind: 'error', text: 'Укажите причину изменения owner baseline.' })
      return
    }
    setBusy('seed')
    setFeedback(undefined)
    try {
      const next = await client.updateActiveAgentPersonalization({
        expectedVersion: ownerSeed.version,
        traits: { ...seedDraft.traits },
        personalization: clonePersonalization(seedDraft.personalization),
        reason,
      })
      setOwnerSeed(next)
      if (activeAgent) setSeedDraft(ownerSeedDraft(activeAgent, next))
      setSeedReason('')
      setSeedEditorOpen(false)
      setFeedback({ kind: 'success', text: `Owner baseline сохранён как revision v${next.version}. Текущее состояние агента не сброшено.` })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Не удалось сохранить owner baseline.' })
    } finally {
      setBusy(undefined)
    }
  }

  const versions = useMemo(() => [...snapshot.versions].sort((a, b) => b.version - a.version), [snapshot.versions])
  const relationshipVersions = useMemo(() => [...snapshot.relationship.versions].sort((a, b) => b.version - a.version), [snapshot.relationship.versions])
  const relationshipLabels = useMemo(() => Object.fromEntries(snapshot.relationship.dimensions.map((dimension) => [dimension.id, dimension.label])), [snapshot.relationship.dimensions])
  const traitLabels = useMemo(() => Object.fromEntries(snapshot.traits.map((trait) => [trait.id, trait.label])), [snapshot.traits])
  const opinions = snapshot.opinions.length > 0 ? snapshot.opinions : snapshot.relationship.opinions
  const loading = busy === 'loading'

  const relationship = section === 'relationship'

  return <div className="persona-view">
    <div className="ambient-glow ambient-glow--one" />
    <div className="ambient-glow ambient-glow--two" />
    <header className="persona-view__hero">
      <div>
        <span className="welcome-card__eyebrow"><span className="eyebrow-dot" /> {relationship ? 'YURI RELATIONSHIP MODEL' : 'YURI PERSONALITY SYSTEM'}</span>
        <h1>{relationship ? 'Связь' : 'Персона'}<span className="title-dot">.</span></h1>
        <p>{relationship
          ? 'Как Yuri моделирует отношения с вами: сигналы связи и субъективные выводы. Любое мнение помечено как opinion/inference и связано с evidence — оно может быть неверным.'
          : 'Версионируемая личность в границах identity seed: bounded traits, affective state и полная история изменений с возможностью отката.'}</p>
      </div>
      <div className="persona-view__metric">
        <strong>v{relationship ? snapshot.relationship.version : snapshot.currentVersion}</strong>
        <span>{relationship ? 'версия связи' : 'текущая persona'}</span>
      </div>
    </header>

    {/*
      * Two shell destinations, not two panes of one page: the tabs move the
      * nav rail with them so the highlighted entry always matches what is on
      * screen. Without `onSelectSection` (a standalone render) they are inert
      * labels rather than a control that lies about where the user is.
      */}
    <div className="persona-tabs" role="tablist" aria-label="Персона и связь">
      <button aria-selected={!relationship} className={relationship ? 'persona-tab' : 'persona-tab persona-tab--active'} disabled={!onSelectSection} onClick={() => onSelectSection?.('personality')} role="tab" type="button"><Icon name="personality" width={15} height={15} /> Personality</button>
      <button aria-selected={relationship} className={relationship ? 'persona-tab persona-tab--active' : 'persona-tab'} disabled={!onSelectSection} onClick={() => onSelectSection?.('relationship')} role="tab" type="button"><Icon name="relationship" width={15} height={15} /> Relationship</button>
    </div>

    <PersonalityLayerMap ownerSeed={ownerSeed} section={section} snapshot={snapshot} />

    {!relationship && ownerSeed && <section className="owner-seed-summary" aria-labelledby="owner-seed-title">
      <div><span className="section-heading__overline">OWNER RESET BASELINE · v{ownerSeed.version}</span><h2 id="owner-seed-title">Исходная персонализация</h2><p>Это append-only baseline владельца. Новая revision задаёт будущие reset-значения и границы рефлексии, но не переписывает текущие persona, affect или relationship.</p></div>
      <button className="button button--quiet" disabled={busy !== undefined || !seedDraft} onClick={() => setSeedEditorOpen((open) => !open)} type="button"><Icon name={seedEditorOpen ? 'x' : 'personality'} width={14} height={14} /> {seedEditorOpen ? 'Закрыть редактор' : 'Редактировать baseline'}</button>
    </section>}

    {!relationship && seedEditorOpen && seedDraft && <section className="owner-seed-editor" aria-labelledby="owner-seed-editor-title">
      <header><div><span className="section-heading__overline">APPEND-ONLY OWNER REVISION</span><h2 id="owner-seed-editor-title">Новая версия baseline</h2></div><span className="owner-seed-editor__version">v{ownerSeed?.version ?? 0} → v{(ownerSeed?.version ?? 0) + 1}</span></header>
      <label className="owner-seed-editor__reason"><span>Причина изменения <strong>обязательно</strong></span><textarea maxLength={500} onChange={(event) => setSeedReason(event.target.value)} placeholder="Например: хочу сделать стиль общения мягче и добавить важный эпизод предыстории" rows={2} value={seedReason} /></label>
      <div className="owner-seed-editor__warning"><Icon name="warning" width={14} height={14} /><span>Имя, возраст и гендер остаются неизменными. Сохранение не выполняет reset; это отдельное явное действие ниже на странице.</span></div>
      <AgentProfileForm baselineEditing busy={busy === 'seed'} onBack={() => setSeedEditorOpen(false)} onChange={setSeedDraft} onSubmit={() => void saveOwnerSeed()} submitLabel="Сохранить revision" value={seedDraft} />
    </section>}

    {loading && <div className="persona-loading" role="status"><span className="memory-spinner" /> {relationship ? 'Загружаю состояние связи…' : 'Загружаю состояние личности…'}</div>}
    {error && <div className="tasks-feedback tasks-feedback--error" role="alert"><Icon name="warning" width={14} height={14} /> {error}<button aria-label="Закрыть ошибку" className="icon-button icon-button--small" onClick={() => setError(undefined)} type="button"><Icon name="x" width={13} height={13} /></button></div>}

    {!relationship && <SnapshotHeader busy={busy !== undefined} onEvolution={() => void toggleAutoEvolution()} snapshot={snapshot} />}

    <div className="persona-grid">
      <main className="persona-main">
        {relationship ? (
          <>
            <section aria-labelledby="relationship-state-title" className="persona-panel persona-relationship-panel persona-panel--selected">
              <div className="persona-panel__heading"><div><span className="section-heading__overline">RELATIONSHIP STATE · v{snapshot.relationship.version}</span><h2 id="relationship-state-title">Сигналы связи</h2></div><Icon name="relationship" width={18} height={18} /></div>
              <p className="persona-panel__lead">{snapshot.relationship.summary}</p>
              {snapshot.relationship.reason && <div className="relationship-reason"><span>Почему состояние изменилось</span><p>{snapshot.relationship.reason}</p><small>evidence · {snapshot.relationship.evidence?.length ?? 0}</small></div>}
              <div className="relationship-signals">{snapshot.relationship.dimensions.map((dimension) => <RelationshipBar dimension={dimension} key={dimension.id} />)}</div>
              {snapshot.relationship.dimensions.length === 0 && <div className="persona-empty">Сигналы связи ещё не рассчитаны.</div>}
              <div className="persona-relationship-panel__meta"><span>Обновлено</span><time dateTime={snapshot.relationship.updatedAt}>{formatDate(snapshot.relationship.updatedAt)}</time></div>
            </section>

            <section aria-labelledby="relationship-history-title" className="persona-panel persona-history-panel">
              <div className="persona-panel__heading"><div><span className="section-heading__overline">RELATIONSHIP HISTORY</span><h2 id="relationship-history-title">История связи</h2></div><button aria-label="Обновить историю связи" className="icon-button" disabled={busy !== undefined} onClick={() => void load()} type="button"><Icon name="refresh" width={15} height={15} /></button></div>
              <p className="persona-panel__lead">Каждое значимое изменение хранит причину, evidence и дельты сигналов. Rollback создаёт новую версию и не стирает историю.</p>
              <div className="persona-history">{relationshipVersions.map((version) => <RelationshipVersionItem busy={busy !== undefined} currentVersion={snapshot.relationship.version} key={`${version.id}-${version.version}`} labels={relationshipLabels} onRollback={(next) => void rollbackRelationship(next)} version={version} />)}</div>
              {relationshipVersions.length === 0 && <div className="persona-empty">История связи пока не сформирована.</div>}
            </section>

            <section aria-labelledby="persona-opinions-title" className="persona-panel">
              <div className="persona-panel__heading"><div><span className="section-heading__overline">SUBJECTIVE MODEL</span><h2 id="persona-opinions-title">Мнение о пользователе</h2></div><span className="persona-panel__count">opinion ≠ fact</span></div>
              <p className="persona-panel__lead">Эти выводы могут быть неверными или противоречить фактам. Они не переписывают memory и не получают authority над policy.</p>
              <div className="persona-opinions">{opinions.map((opinion) => <OpinionCard key={opinion.id} opinion={opinion} />)}</div>
              {opinions.length === 0 && <div className="persona-empty">Субъективных выводов пока нет.</div>}
            </section>
          </>
        ) : (
          <>
            <section aria-labelledby="persona-traits-title" className="persona-panel persona-panel--selected">
              <div className="persona-panel__heading"><div><span className="section-heading__overline">BOUNDED TRAITS</span><h2 id="persona-traits-title">Черты характера</h2></div><span className="persona-panel__count">{snapshot.traits.length} traits · max delta protected</span></div>
              <p className="persona-panel__lead">Диапазон каждой черты задан seed/policy и не может быть расширен моделью. Закреплённые traits не исчезают при обычной рефлексии.</p>
              <div className="persona-traits">{snapshot.traits.map((trait) => <TraitRow busy={busy !== undefined} key={trait.id} onPin={(next) => void togglePinned(next)} trait={trait} />)}</div>
              {snapshot.traits.length === 0 && <div className="persona-empty">Traits ещё не загружены из reflection service.</div>}
            </section>

            <section aria-labelledby="persona-history-title" className="persona-panel persona-history-panel">
              <div className="persona-panel__heading"><div><span className="section-heading__overline">VERSION HISTORY</span><h2 id="persona-history-title">История и причины</h2></div><button aria-label="Обновить историю persona" className="icon-button" disabled={busy !== undefined} onClick={() => void load()} type="button"><Icon name="refresh" width={15} height={15} /></button></div>
              <p className="persona-panel__lead">Rollback создаёт наблюдаемое изменение и не удаляет исходные версии. Выберите версию, если нужно вернуть прежний bounded prompt stack.</p>
              <div className="persona-history">{versions.map((version) => <VersionItem busy={busy !== undefined} currentVersion={snapshot.currentVersion} key={`${version.id}-${version.version}`} labels={traitLabels} onRollback={(next) => void rollback(next)} version={version} />)}</div>
            </section>
          </>
        )}
      </main>

      <aside className="persona-side">
        <section aria-labelledby="affect-dimensions-title" className="persona-panel"><div className="persona-panel__heading"><div><span className="section-heading__overline">AFFECT DIMENSIONS</span><h2 id="affect-dimensions-title">Сигналы affect</h2></div><span className="persona-panel__count">intensity</span></div><div className="persona-affect">{snapshot.affect.dimensions.map((dimension) => <AffectBar dimension={dimension} key={dimension.id} />)}</div></section>

        <section className="persona-safety-note"><span className="persona-safety-note__icon"><Icon name="shield" width={16} height={16} /></span><div><strong>Security boundary</strong><p>Негативный affect, ревность и tsundere-поведение не могут выполнять месть, саботаж, угрозы, шантаж или скрывать данные. Все внешние действия проходят обычный policy/approval flow.</p></div></section>

        {!relationship && <section aria-labelledby="persona-controls-title" className="persona-panel persona-controls"><div className="persona-panel__heading"><div><span className="section-heading__overline">RECOVERY</span><h2 id="persona-controls-title">Управление состоянием</h2></div><Icon name="refresh" width={17} height={17} /></div><p>Сброс возвращает исходный identity seed. Запись истории и evidence остаётся доступной для проверки.</p><button className="button button--quiet" disabled={busy !== undefined} onClick={() => void reset()} type="button"><Icon name="refresh" width={13} height={13} /> Сбросить к identity seed</button><span className="persona-controls__mode"><i /> {client.mode === 'wails' ? 'Wails backend' : 'Локальный preview'}</span></section>}
        {relationship && <section aria-labelledby="relationship-controls-title" className="persona-panel persona-controls"><div className="persona-panel__heading"><div><span className="section-heading__overline">RELATIONSHIP RECOVERY</span><h2 id="relationship-controls-title">Управление связью</h2></div><Icon name="refresh" width={17} height={17} /></div><p>Сброс возвращает только отношение к владельцу к текущему relationship seed. Persona, память, affect и отношения с другими агентами остаются неизменными.</p><button className="button button--quiet" disabled={busy !== undefined} onClick={() => void resetRelationship()} type="button"><Icon name="refresh" width={13} height={13} /> Сбросить связь к seed</button><span className="persona-controls__mode"><i /> {client.mode === 'wails' ? 'Wails backend' : 'Локальный preview'}</span></section>}
      </aside>
    </div>

    {feedback && <div className={`tasks-feedback tasks-feedback--${feedback.kind}`} role={feedback.kind === 'error' ? 'alert' : 'status'}><Icon name={feedback.kind === 'success' ? 'check' : 'warning'} width={14} height={14} /> {feedback.text}<button aria-label="Закрыть уведомление" className="icon-button icon-button--small" onClick={() => setFeedback(undefined)} type="button"><Icon name="x" width={13} height={13} /></button></div>}
    <div className="persona-note"><span className="persona-note__icon"><Icon name="lock" width={15} height={15} /></span><span>Mutable persona и affect — данные профиля. Они не изменяют immutable policy, permissions, allowed directories или approval semantics.</span></div>
  </div>
}
