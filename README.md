# Yuri

Yuri — локальный desktop-first AI-агент для одного владельца. Текущий OSS CI-артефакт ориентирован на macOS; архитектура остаётся переносимой на Windows и Linux.

Yuri core и Plugin SDK распространяются по [Apache License 2.0](LICENSE). Лицензии сторонних зависимостей сохраняются согласно их собственным notices.

Engineering foundation, conversational vertical slice, storage/memory, plugin runtime, scheduler/proactivity и reflection/personality vertical slice реализованы. Основные решения и границы находятся в следующих документах:

- [`docs/PRODUCT_SPEC.md`](docs/PRODUCT_SPEC.md) — техническое задание и roadmap;
- [`docs/PERSONALIZATION_ROADMAP.md`](docs/PERSONALIZATION_ROADMAP.md) — последовательный план развития personality, affect, relationships и creation flow;
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — компоненты, context assembly, storage и trust boundaries;
- [`docs/THREAT_MODEL.md`](docs/THREAT_MODEL.md) — модель угроз и security invariants;
- [`docs/adr/`](docs/adr/) — принятые архитектурные решения;
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — правила разработки и проверки.

Что уже работает:

- один локальный профиль Yuri без multi-user/server mode;
- SQLite как authoritative store, Pebble и индексы как производные;
- immutable security policy отдельно от mutable persona и affect;
- потоковый текстовый chat через OpenAI-compatible Responses/Chat Completions;
- официальный Codex App Server adapter с ChatGPT OAuth, явным login/logout и чтением work/rate limits без доступа Yuri к OAuth-токенам;
- Antigravity явно остаётся недоступным с typed `unsupported_auth_mode`: Yuri не импортирует чужой OAuth/token cache и предлагает API-key fallback до появления официального integration contract;
- agent loop с cancellation, budgets, structured tool calls и idempotency;
- filesystem tools только внутри явно разрешённых директорий: low-risk чтение и bounded `create`/`replace` с точечным approval, атомарным commit, проверкой текущего SHA-256 и redacted audit;
- credential-free `web.fetch` для bounded чтения публичных HTTP(S)-страниц с защитой от SSRF/DNS rebinding, блокировкой LAN/metadata targets и отдельным trace;
- provider-independent `web.search` с первым SearXNG JSON adapter, проверкой endpoint из Settings, 3–10 нормализованными результатами и отдельным `web.fetch` для чтения выбранной страницы;
- push-to-talk с OpenAI-compatible STT и прерываемое системное TTS;
- offline fake-Wails smoke проверяет цепочку STT → streaming chat → системный TTS и barge-in без сети и реальных credentials;
- Wails UI для диалогов, состояний запуска, approvals и provider settings;
- общий неизменяемый архив всех диалогов с SQLite FTS5 и provenance;
- автономное post-turn извлечение памяти без обязательного подтверждения;
- append-only версии памяти, редактирование, pin, soft delete и lifecycle `active → dormant → active`;
- bounded core snapshot, hybrid lexical/vector-ready retrieval и межсессионная сборка контекста;
- Wails UI для просмотра памяти и целенаправленного поиска по прошлым сессиям.
- versioned `yuri.plugin.v1` JSON Lines protocol и out-of-process supervisor с handshake, health, bounded messages, cancellation, restart и process-group termination;
- JSON Schema manifest/RPC contracts, dependency-free Go Plugin SDK и реально исполняемый reference echo plugin;
- локальная проверка и атомарная установка plugin package, checksum/platform/core compatibility, explicit dev mode и установка в выключенном состоянии;
- durable plugin metadata/capability grants в SQLite, deny-by-default tool mediation, runtime status/audit и UI управления lifecycle.
- durable one-shot/interval/5-field CRON scheduler с IANA timezone, misfire policy, leases, bounded retry, no-overlap и recovery после restart;
- фоновые agent runs с отдельным lifecycle и budgets, ручной запуск/пауза/редактирование/остановка, история запусков и Tasks UI;
- scheduled runs автоматически используют только low-risk read tools; изменяющие и внешние действия остаются интерактивными до появления durable idempotency contract у tools;
- explainable proactivity policy с global switch, overnight/DST-aware quiet hours, durable daily/idempotency ledger, per-type cooldown и дедуплицированной отложенной доставкой;
- in-app/macOS notification flow, append-only Activity UI и medium-risk `scheduler.create` tool с обязательным подтверждением.
- отдельные versioned mutable persona, relationship opinions и affect snapshots с evidence, optimistic concurrency, atomic multi-state commit, rollback/reset и закреплением traits;
- bounded post-turn reflection без tools: строгий JSON contract, cooldown, max-delta/range guards, запрет внешнего неподтверждённого evidence и последовательный запуск для единственного локального профиля;
- provider-independent Personality Compiler превращает owner seed + mutable persona + relationship/affect в bounded качественные правила; обычный и peer dialogue получают их отдельным untrusted data layer ниже immutable policy и identity seed;
- Personality/Relationship UI с историей версий и простым 2D-аватаром; push-to-talk поддерживает barge-in, автоозвучка является явным opt-in и не включает микрофон.
- first-run onboarding с OpenAI-compatible и Codex OAuth вариантами; durable completion устанавливается backend только после успешного provider probe.

CI проверяет core, scheduler/proactivity, reflection/persona storage, Plugin SDK/reference plugin, frontend, macOS universal OSS artifact/checksum и отдельный Wails app launch/clean-shutdown smoke. Подробности упаковки находятся в [`docs/MACOS_RELEASE.md`](docs/MACOS_RELEASE.md).

Важно: Этап 3 изолирует сбой плагина отдельным процессом, но ещё не превращает сторонний executable в полностью недоверенный код. До OS sandbox и process hardening устанавливать следует только проверенные пакеты; unsigned package требует явного dev mode.

## Локальная проверка

```bash
npm --prefix frontend ci
make check
make mvp-smoke
```

Wails-команды запускаются из каталога desktop entrypoint, где находится конфигурация проекта:

```bash
cd cmd/yuri
wails dev
```
