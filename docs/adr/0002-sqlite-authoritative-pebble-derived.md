# ADR-0002: SQLite — источник истины, Pebble и индексы — производные

- Статус: accepted
- Дата: 2026-08-27
- Область: Этап 0 / storage foundation

## Контекст

Yuri должна переживать перезапуск и восстанавливать историю диалогов, memory versions, relationship/affect state, persona evolution, jobs, permissions и audit. При этом event checkpoints, leases, cache и vector/FTS indexes требуют быстрых key-value операций и могут быть перестроены.

Два равноправных authoritative store привели бы к неразрешимым конфликтам при crash и migration.

## Решение

1. SQLite — единственный authoritative store для всего восстанавливаемого пользовательского и security state.
2. Все связанные изменения (например, memory version + provenance + audit) выполняются транзакционно.
3. PebbleDB используется только для производных или высокочастотных данных: checkpoints, leases, idempotency keys, resumable worker state и caches.
4. FTS5, embeddings и vector index являются rebuildable projections. Их metadata/version/parameters хранятся в SQLite.
5. Большие blobs хранятся content-addressed, но hash, size, sensitivity и provenance находятся в SQLite.
6. Перед несовместимой migration создаётся backup; при потере Pebble/index приложение восстанавливает их из SQLite и не теряет memory, history, permissions или persona.

## Правило записи

```text
domain change → SQLite transaction → publish event → update derived projection
```

Производная запись не может быть единственным доказательством того, что side effect, approval или memory/persona transition состоялся.

## Последствия

Положительные:

- детерминированное recovery и экспорт;
- упрощённая проверка целостности и rollback;
- повреждение индекса не уничтожает пользовательское состояние.

Ограничения:

- миграции и транзакции требуют явной схемы и тестов;
- rebuild больших индексов может занимать время после обновления;
- Pebble нельзя использовать для shortcuts, которые меняют semantics durable state.
