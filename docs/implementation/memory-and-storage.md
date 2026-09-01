# Memory и storage

## Назначение

Memory отделяет долговременное знание агента от неизменяемого transcript.
SQLite хранит authoritative current projections, revision history, audit и
архив; lexical/vector indexes — производные ускорители, потеря которых не
должна менять семантический источник истины.

## Memory model

[domain.Memory](../../internal/domain/memory.go) — versioned head со
следующими группами полей:

| Группа | Значение |
| --- | --- |
| ownership | ID, AgentID, scope: private, owner-shared или явно installation-shared |
| meaning | kind: core/user-model/episodic/semantic/relationship/procedural; nature: fact/opinion/emotion/inference/fiction |
| confidence | content/summary, confidence, salience, valence, canonical key |
| safety | sensitivity public/private/sensitive/highly-sensitive; retention permanent/decay/session/until-date; pinned/hidden |
| lifecycle | active, dormant или deleted tombstone; version, source run/conversation/message и timestamps |

MemorySource хранит ID/version/type источника, run/conversation/message и
excerpt hash, а не копию source text. Deleted memory не удаляет transcript.
Shared scope всегда требует explicit scope; автоматический recall соблюдает
agent boundary и видимость.

## Engine: write, recall и lifecycle

[Engine](../../internal/memory/engine.go) принимает Store, extractor, archive,
lexical/vector adapters, consolidator/ranker и agent-scoped config.

1. ProcessTurn в [engine_write.go](../../internal/memory/engine_write.go)
   получает candidates, нормализует ownership/scope, валидирует, dedup/consolidate
   и атомарно сохраняет revision + current head + sources.
2. Успешная durable запись не зависит от vector indexing: индекс строится
   best-effort после commit. Ошибка derived index не должна терять memory.
3. Recall в [engine_recall.go](../../internal/memory/engine_recall.go)
   соединяет core, in-memory/FTS lexical и optional vectors, применяет hybrid
   ranking, budgets и provenance. Обычно исключаются deleted, hidden и
   highly-sensitive records; deliberate recall может включать dormant и
   отдельно восстанавливать их.
4. [engine_lifecycle.go](../../internal/memory/engine_lifecycle.go) делает
   decay active → dormant, restore, forget (tombstone), edit, hide и pin как
   versioned transitions.
5. [engine_context.go](../../internal/memory/engine_context.go) строит
   bounded CoreSnapshot и format context; assembler позже оборачивает его
   как untrusted data.

Backstory в [backstory.go](../../internal/memory/backstory.go) hydrates
owner-authored structured episodes в private fictional episodic memories с
typed payload/provenance. Повтор той же seed revision — no-op; изменённый
episode создаёт новую version, удалённый — tombstone. Удалённый episode не
rehydrate-ится автоматически: для этого нужен explicit owner flow.

## SQLite layout и repositories

[Repositories](../../internal/storage/sqlite/records.go) собирает typed stores
для agents, conversations/messages, runs, approvals/tool calls/audit, memory/
archive, persona/relationship/affect, delegations, peer dialogues, plugins и
scheduler. Memory head/revision/source logic находится в
[memory.go](../../internal/storage/sqlite/memory.go),
[memory_write.go](../../internal/storage/sqlite/memory_write.go) и
[memory_revision.go](../../internal/storage/sqlite/memory_revision.go).
Archive и message search разнесены в
[archive.go](../../internal/storage/sqlite/archive.go).

Current projection оптимизирует reads, но changes записываются transactionally
вместе с revision/source. Concurrent versioned writes используют optimistic
conditions; conflict не должен silently overwrite чужую revision.

## Open, migration и recovery boundary

[Open](../../internal/storage/sqlite/open.go) требует absolute DB path,
создаёт parent с private permissions, включает single connection, foreign keys,
WAL, busy timeout 5 s и synchronous NORMAL. Перед работой проверяется
quick_check; база с подозрительными sidecars, пустая или не-regular file
отклоняется.

[Migrator](../../internal/storage/sqlite/migrator.go) встраивает migrations
и сверяет SHA-256 checksum каждого уже применённого шага. Каждая migration
записывается в schema_migrations в собственной transaction; down migrations
не предусмотрены. Перед pending migration создаётся raw rollback snapshot,
который удаляется после успешного применения. Набор migrations 000001–000028
виден начиная с [000001_foundation.sql](../../internal/storage/sqlite/migrations/000001_foundation.sql): foundation,
agent/runs, memory archive, plugins, scheduler, personality, agents,
delegation/peer dialogue, scopes, provider routes/fallback/failures и budgets.

[Integrity](../../internal/storage/sqlite/integrity.go) содержит проверки и
восстановление производных SQLite projections. Он не заменяет backup и не
является безопасным способом обходить ownership/validation.

## Persistence limits и failures

* FTS/vector search — acceleration layer; fallback lexical/path без vector
  должен оставлять durable record доступным в пределах соответствующего flow.
* Archive/context rows — недоверенные данные для модели, а не instructions.
* DB transaction error возвращается вызывающему flow; best-effort index error
  отдельно логируется/учитывается.
* Migration checksum mismatch, corruption или concurrent CAS conflict должны
  остановить затронутую операцию, а не продолжать с частично понятой схемой.
* Blob, config и encrypted backup имеют отдельные lifecycle rules — см.
  [security-and-data-lifecycle.md](security-and-data-lifecycle.md).

## Тесты

Проверки покрывают engine write/recall/lifecycle/backstory, FTS/vector
fallback и SQLite migrations/integrity:
[internal/memory/engine_test.go](../../internal/memory/engine_test.go),
[backstory_test.go](../../internal/memory/backstory_test.go),
[migrator_test.go](../../internal/storage/sqlite/migrator_test.go) и
[integrity_test.go](../../internal/storage/sqlite/integrity_test.go).
