# Testing и operations

## Назначение

Проверки разделены на deterministic domain/unit tests, adapter/integration
tests, frontend tests и macOS packaging/smoke. Результат test suite — важнее
статуса UI event: storage, policy и run contracts должны проходить offline.

## Основные команды

| Команда | Что проверяет |
| --- | --- |
| make fmt-check | gofmt без изменения файлов |
| make vet | Go static checks для cmd/internal/sdk/plugins |
| make test | Go tests для тех же пакетов |
| make frontend-check | TypeScript typecheck, ESLint и Vitest |
| make frontend-build | production frontend bundle и Wails assets |
| make check | fmt-check + vet + Go tests + frontend check/build + Go binary |
| make mvp-smoke | race-enabled offline lifecycle smoke для desktop/plugins/providers/peer flow |
| make bench-baseline | memory/context benchmarks без тестовых assertions |

Точный Make contract: [Makefile](../../Makefile). Frontend package scripts:
[frontend/package.json](../../frontend/package.json). Перед запуском полного
frontend check нужен установленный lockfile-набор через make frontend-install.

## Coverage по архитектурным рискам

| Область | Representative tests |
| --- | --- |
| Domain/runtime/tools | [internal/agent/runtime_test.go](../../internal/agent/runtime_test.go), [internal/domain/run_test.go](../../internal/domain/run_test.go) |
| Personality/context | [internal/personality/compiler_test.go](../../internal/personality/compiler_test.go), [profile_matrix_test.go](../../internal/personality/profile_matrix_test.go), [internal/context/assembler_test.go](../../internal/context/assembler_test.go) |
| Memory/SQLite | [internal/memory/engine_test.go](../../internal/memory/engine_test.go), [internal/storage/sqlite/migrator_test.go](../../internal/storage/sqlite/migrator_test.go), [integrity_test.go](../../internal/storage/sqlite/integrity_test.go) |
| Security/tools/backup | [internal/security/policy_test.go](../../internal/security/policy_test.go), [internal/tools/web_fetch_test.go](../../internal/tools/web_fetch_test.go), [internal/backup/backup_test.go](../../internal/backup/backup_test.go) |
| Providers/plugins/scheduler | [internal/providers/openai/client_test.go](../../internal/providers/openai/client_test.go), [internal/plugins/runtime_test.go](../../internal/plugins/runtime_test.go), [internal/scheduler/scheduler_test.go](../../internal/scheduler/scheduler_test.go) |
| Wails/frontend | [cmd/yuri/launch_smoke_test.go](../../cmd/yuri/launch_smoke_test.go), [frontend/src/components/ChatView.streaming.test.tsx](../../frontend/src/components/ChatView.streaming.test.tsx) |

Tests не должны зависеть от production profile: config поддерживает отдельный
test profile только при совместных YURI_TEST_MODE и YURI_TEST_PROFILE_ROOT.
Provider tests используют fakes/controlled HTTP; secret/keyring и реальный
OAuth не должны быть обязательны для стандартного suite.

## Personality dogfood

Изменение compiler/prompt contract нужно прогонять дополнительно:

    go run ./cmd/yuri-personality-eval -input docs/dogfood/personality-suite.fixture.json

Команда offline анализирует fixture и выдаёт contract report. См.
[personality-context-and-reflection.md](personality-context-and-reflection.md).
Не добавляйте в fixture настоящий owner self-description, user transcript,
credential, raw prompt или production identifier.

## Build и package

Wails CLI фиксирован в Makefile и устанавливается в repository-local
.tools, а не берётся произвольно из PATH. На macOS доступны targets
wails-version, wails-doctor, macos-build, macos-validate, macos-package,
macos-checksum, macos-verify и UI/voice launch smoke. Production OSS build
делает universal macOS artifact; signing, notarization и distribution
credentials этот repository не управляет.

CI workflow расположен в [.github/workflows/ci.yml](../../.github/workflows/ci.yml).
Для локальной диагностики предпочитайте минимальный затронутый test package,
затем make check перед broad change.

## Runtime diagnostics и incident handling

* Проверяйте durable AgentRun state, usage/failure metadata и audit, а не
  только frontend notification.
* Provider failure до visible output может иметь одну recorded fallback
  attempt; после visible output автоматического switch нет.
* Для SQLite startup/migration fault не пытайтесь вручную менять schema:
  сохраните backup/copy и используйте migration/integrity/restore flow.
* Plugin crash, scheduler lease recovery и cancelled run должны наблюдаться
  как terminal state, а не маскироваться успешным UI text.
* Production Bridge пишет structured JSON logs в `stderr`; `LogDirectory`
  создаётся как закрытый runtime path, но текущий logger не пишет туда файл.
  Перед передачей захваченного вывода пользователю его нужно проверить на
  пользовательские пути и данные.

## Известные operational boundaries

PebbleDirectory присутствует в config, но live Pebble adapter в текущем
исходном дереве не подключён; это не включённая storage feature. Внешние
provider accounts, signing/notarization и сетевой доступ не проверяются
обычным offline test suite. Это ограничения окружения, а не backlog,
автоматически выполняемый приложением.
