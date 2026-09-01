# Доменная модель

## Назначение

[`internal/domain/ports.go`](../../internal/domain/ports.go) и соседние
domain files задают переносимые типы,
валидацию и repository ports. Он не открывает SQLite, не вызывает модели и
не принимает approval. Большинство изменяемых объектов — versioned snapshots:
current projection удобна для чтения, но история revision остаётся частью
аудита.

## Идентичность, профиль и персонализация

| Тип | Ключевые поля | Инварианты |
| --- | --- | --- |
| [`AgentProfile`](../../internal/domain/agent_profile.go) | `ID`, name/age/gender, preferences/backstory, primary и opt-in fallback routes, execution budget preset | Долговечный agent owner profile; fallback не включается сам по себе. |
| [`PersonalizationSeed`](../../internal/domain/personalization.go) | schema/revision/version/parent/operation, `AgentID`, `Identity`, `CommunicationStyle`, `Temperament`, `EmotionalDynamics`, `RelationshipSeed`, backstory, `EvolutionPolicy` | owner-authored append-only baseline. Обычная reflection не может его записать; update/reset — явные owner operations. |
| [`MutablePersona`](../../internal/domain/persona.go) | ID/revision/version/parent/operation, traits, pinned traits, diff, self-description, reason/evidence/author run | Принадлежит agent; trait values находятся в допустимом диапазоне; evolution version увеличивается на один и соблюдает max delta, кроме rollback/reset. |
| [`RelationshipState`](../../internal/domain/relationship.go) | ID/revision/version/parent/operation, dimensions, opinions, summary, evidence/reason/author | Субъективен и не является фактической memory. Owner state keyed by agent; peer states имеют отдельный directional ID. |
| [`AffectiveState`](../../internal/domain/affect.go) | ID/revision/version/parent/operation, emotions/dimensions/values, summary/evidence/reason | Transient subjective state; значения нормализуются, revisions append-only. |

`IdentityPersonalization` содержит `PreferredLanguage`, pronouns, user address,
self-description и role. `CommunicationStyle` содержит 11 видимых параметров
в диапазоне `[0,1]`; `Temperament` — 25 стандартных traits плюс безопасный
`Custom`; `EmotionalDynamics` — семь scalar dynamics, `ConflictStyle`,
triggers и soothing strategies. Их точная языковая интерпретация принадлежит
компилятору, а не domain layer. См.
[`internal/domain/personalization.go`](../../internal/domain/personalization.go)
и [personality-context-and-reflection.md](personality-context-and-reflection.md).

### Разделение owner и mutable layers

`NewOwnerRelationshipState` проецирует `RelationshipSeed` в независимую
mutable relationship с provenance owner seed. Это не превращает fictional
history в factual memory. `NewPeerRelationshipState` стартует только от
социальных predispositions observer-а: он намеренно не наследует owner
closeness или романтический narrative. Текущий набор девяти relationship
measurements: `trust`, `attachment`, `respect`, `irritation`, `jealousy`,
`resentment`, `gratitude`, `closeness`, `reliability`.

`RelationshipOpinion` требует subject, claim/text, confidence `[0,1]` и
минимум одну `EvidenceLink`; это opinion/inference, не факт. Соответствующая
валидация находится в
[`internal/domain/relationship.go`](../../internal/domain/relationship.go).

## Conversation, message и run

[`Conversation`](../../internal/storage/sqlite/records.go) принадлежит одному
agent; `Message` после записи не редактируется как transcript record. Title
имеет source, поэтому owner title не должен быть перезаписан автоматическим
именованием. Репозиторий проверяет ownership при обращении к conversation и
message.

[`AgentRun`](../../internal/domain/run.go) отделяет выполнение от сообщения:

| Поле/группа | Значение |
| --- | --- |
| identity | `ID`, `AgentID`, kind, optional conversation и parent run |
| state | `created`, `queued`, `running`, `waiting_approval`, `cancelling`, terminal `completed`/`failed`/`cancelled` |
| control | `RunBudget`, version, timestamps, cancellation/failure info |
| inference | initial route, optional единственный fallback route, usage |

Top-level run владеет conversation; subagent run имеет parent и не получает
conversation. `SwitchInferenceRoute` разрешён один раз, только пока run
работает и до usage/failure, и требует другого route. Все optimistic updates
проверяют предыдущую `Version` через repository port.

## Memory, capability и approval

`Memory` — versioned head с kind/nature/scope/sensitivity/retention,
confidence, provenance и lifecycle; отдельный `MemorySource` хранит ссылку
на исходник и hash excerpt, а не исходный текст. Полная модель — в
[`internal/domain/memory.go`](../../internal/domain/memory.go) и
[memory-and-storage.md](memory-and-storage.md).

[`Capability`](../../internal/domain/capabilities.go) и scope описывают
то, что в принципе может быть выдано инструменту. [`Approval`](../../internal/domain/approval.go)
связывает риск и exact action hash с durable owner decision. Они не являются
свойством persona или model message.

## Delegation, peer и scheduled work

[`Delegation`](../../internal/domain/delegation.go) — один ephemeral
anonymous child run с parent/principal, bounded scope, idempotency и
результатом ограниченного размера; глубина строго 1. [`PeerDialogue`](../../internal/domain/peer_dialogue.go)
фиксирует named participant pair, purpose, bounded turns/budget/cooldown,
trigger, completion/failure provenance. [`Schedule`](../../internal/domain/scheduler.go)
и `JobRun` задают durable расписание, lease и попытки исполнения. Эти
агрегаты подробно рассматриваются в соответствующих разделах документации.

## Ports, persistence и ошибка конкуренции

[`internal/domain/ports.go`](../../internal/domain/ports.go) определяет
интерфейсы repositories, а [`internal/storage/sqlite/records.go`](../../internal/storage/sqlite/records.go)
их реализует. Versioned records хранят revision и current projection; попытка
записать объект на устаревшей version даёт domain conflict, который
orchestration должен обработать, а не перезаписать бесшумно.

Domain validation не заменяет security boundary: валидный `AgentRun` не
выдаёт tool permission, а валидная `RelationshipOpinion` не делает её
фактом. Ошибка валидации должна закончить запрашиваемое изменение до
persistence; audit/approval/authorization выполняются на прикладном слое.
