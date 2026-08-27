# Yuri

Yuri — локальный desktop-first AI-агент для одного владельца. Первый публичный релиз ориентирован на macOS; архитектура остаётся переносимой на Windows и Linux.

Engineering foundation, conversational vertical slice, storage/memory и plugin runtime реализованы. Основные решения и границы находятся в следующих документах:

- [`docs/PRODUCT_SPEC.md`](docs/PRODUCT_SPEC.md) — техническое задание и roadmap;
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — компоненты, context assembly, storage и trust boundaries;
- [`docs/THREAT_MODEL.md`](docs/THREAT_MODEL.md) — модель угроз и security invariants;
- [`docs/adr/`](docs/adr/) — принятые архитектурные решения;
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — правила разработки и проверки.

Что уже работает:

- один локальный профиль Yuri без multi-user/server mode;
- SQLite как authoritative store, Pebble и индексы как производные;
- immutable security policy отдельно от mutable persona и affect;
- потоковый текстовый chat через OpenAI-compatible Responses/Chat Completions;
- официальный Codex App Server adapter с ChatGPT OAuth и чтением work/rate limits без доступа Yuri к OAuth-токенам;
- agent loop с cancellation, budgets, structured tool calls и idempotency;
- read-only filesystem tool только внутри явно разрешённых директорий, deny-by-default policy и redacted audit;
- push-to-talk с OpenAI-compatible STT и прерываемое системное TTS;
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

CI проверяет core, Plugin SDK/reference plugin, frontend и отдельный macOS smoke-путь. Scheduler/proactivity и reflection/persona evolution добавляются по следующим отдельным вехам roadmap.

Важно: Этап 3 изолирует сбой плагина отдельным процессом, но ещё не превращает сторонний executable в полностью недоверенный код. До OS sandbox/notarization/trust-store hardening устанавливать следует только проверенные пакеты; unsigned package требует явного dev mode.

## Локальная проверка

```bash
npm --prefix frontend ci
make check
```

Wails-команды запускаются из каталога desktop entrypoint, где находится конфигурация проекта:

```bash
cd cmd/yuri
wails dev
```
