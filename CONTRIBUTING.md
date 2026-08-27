# Участие в разработке Yuri

Yuri находится на Этапе 0 (Engineering foundation). Перед изменением кода прочитайте:

1. [`docs/PRODUCT_SPEC.md`](docs/PRODUCT_SPEC.md) — продуктовые требования и roadmap;
2. [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — границы процессов, контекста и хранения;
3. [`docs/THREAT_MODEL.md`](docs/THREAT_MODEL.md) — угрозы и обязательные меры;
4. [`docs/adr/`](docs/adr/) — принятые архитектурные решения.

## Границы foundation

MVP разрабатывается сначала для macOS, но domain/application слой остаётся переносимым. Приложение локальное и single-user: не добавляйте server mode, multi-tenant abstractions или shared profiles без отдельного изменения ТЗ и ADR.

Этап 0 допускает только каркас, порты, схемы, миграционный/конфигурационный foundation, Wails + React shell, logging/redaction и security seams. Полные реализации памяти, plugin runtime, scheduler, reflection/persona evolution, voice providers и avatar renderer относятся к следующим вехам. Не смешивайте features разных milestones в одном изменении.

Immutable policy, identity invariants, provider credentials и OS permission boundaries не могут изменяться из UI, модели, плагина, памяти или mutable persona.

## Рабочее окружение

Поддерживаемая платформа разработки — macOS. Foundation закрепляет Go 1.25, Wails v2.15, Node.js 22 и npm с обязательным `package-lock.json`; подробности находятся в ADR-0007.

```bash
npm --prefix frontend ci
make check
```

Не добавляйте `node_modules`, build artifacts, SQLite databases, Keychain exports, provider tokens или диагностические dumps в git.

CI проверяет Go и frontend на Linux, а также повторяет foundation tests и frontend build на macOS. Локальную desktop-упаковку выполняйте из `cmd/yuri` командой `wails build`.

## Правила изменений

- Один PR должен иметь одну понятную цель и указывать milestone roadmap.
- Изменение API/данных сопровождайте тестом и, если меняется boundary, ADR.
- Новые durable данные добавляйте в SQLite migration; Pebble/индекс допустимы только как rebuildable projection.
- Любой внешний side effect проверяется policy непосредственно перед execution и оставляет redacted audit event.
- Все данные из web, файлов, писем, tool output и plugin events помечайте provenance; не превращайте их в system instruction.
- Не помещайте credentials в сообщения, логи, audit payload, memory или crash report.
- Для plugin-пакетов фиксируйте source commit, checksum/signature и compatibility; установка должна оставаться выключенной до явного включения.
- Для memory, relationship, affect и persona используйте versioned, атомарные и обратимые изменения. Они не получают authority над immutable policy.

## Проверка перед PR

- `git diff --check` не сообщает об ошибках whitespace.
- Добавлены/обновлены unit, contract или integration tests, соответствующие изменённой границе.
- Для миграций проверены чистая установка, upgrade и восстановление после ошибки.
- Для provider changes проверены cancellation, timeout, redaction и разделение API-key/harness режимов.
- Для security-sensitive changes обновлены threat model и соответствующий ADR.
- В описании PR указано, какие данные durable, какие derived и какие permissions затрагиваются.

## Commit и review

Используйте короткие imperative commit messages, например `docs: define plugin trust boundary` или `ci: add conditional frontend checks`. Не смешивайте форматирование несвязанных файлов с функциональным изменением.

Review обязателен для изменений, которые затрагивают:

- immutable policy, permissions, approvals или secret handling;
- migrations, backup/restore и authoritative storage;
- provider OAuth, credentials или network egress;
- plugin protocol, package verification и process supervision;
- context assembly, provenance, memory/persona persistence.
