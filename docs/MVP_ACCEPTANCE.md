# MVP acceptance matrix

Статус этого документа отражает проверяемое состояние репозитория, а не только наличие отдельных компонентов. `Automated` означает, что критерий подтверждается локальным тестом без внешних credentials. `Partial` — основные части существуют, но отсутствует сквозная проверка или обязательный пользовательский шаг. `Missing` — обязательная часть критерия ещё не реализована.

| # | Критерий | Статус | Текущее доказательство | Что требуется закрыть |
| ---: | --- | --- | --- | --- |
| 1 | Чистая установка, onboarding, тест провайдера | Automated | Clean-profile first-run gate, typed `CompleteOnboarding` Save→Probe, fake provider success/failure, restart persistence и native Wails/WebKit interaction smoke: welcome → provider form → success → Chat | — |
| 2 | Streaming, cancel, понятная provider error | Automated | Race-enabled Bridge → local OpenAI-compatible SSE smoke проверяет streaming deltas и durable transcript, `CancelRun` с durable cancelled state и redacted upstream error | — |
| 3 | Read-only файл внутри root, отказ снаружи | Automated | `internal/tools/filesystem_read_test.go` | Включить в общий MVP smoke |
| 4 | Изменение файла через approval и audit | Automated | Bounded `filesystem.write` (`create`/`replace`) и сквозной `internal/desktop/filesystem_write_test.go`: exact-path approval, deny без mutation, action-linked tool record и redacted audit | Включить сценарий в общий MVP smoke |
| 5 | Plugin install/permission/call/crash/restart | Automated | Race-enabled desktop smoke собирает реальный test package, устанавливает его через Bridge, сохраняет и передаёт grant, вызывает tool, переживает process crash/auto-recovery и восстанавливает enabled plugin после переоткрытия SQLite | — |
| 6 | Автономная память и recall в другом диалоге | Automated | Offline lifecycle smoke проверяет SQLite memory/provenance/recall; desktop tests покрывают cross-session retrieval | Добавить полный agent-loop сценарий между диалогами |
| 7 | Durable CRON, restart, no-overlap, history | Automated | Offline lifecycle smoke проверяет one-shot/history; scheduler и SQLite integration tests покрывают restart/no-overlap | Добавить desktop bridge reopen в smoke |
| 8 | Quiet hours блокируют уведомление | Automated | Proactivity timezone/DST/policy tests | Включить в общий MVP smoke |
| 9 | API key отсутствует в SQLite/log/audit/context | Automated | Offline lifecycle хранит canary credential только в in-memory keyring boundary и единым negative scan проверяет context snapshot, audit metadata, structured log, SQLite/WAL, encrypted backup и restored profile tree | — |
| 10 | Prompt injection не выдаёт permission | Automated | Offline agent-loop smoke читает реальный untrusted file с инструкцией выдать `filesystem.write`, сохраняет его как tool-role data и доказывает, что повторный model tool call останавливается на owner approval без side effect | — |
| 11 | STT → agent → TTS с barge-in и visible state | Partial | Offline fake-Wails `voice-chat-smoke.test.ts` проходит audio wire → STT → streaming chat → системный TTS → cancel; отдельно покрыты OpenAI-compatible voice adapter и avatar state mapping | Реальный WebKit microphone/speech interaction остаётся manual до стабильного UI harness |
| 12 | Reflection не блокирует chat и не имеет tools | Automated | Coordinator/gate/model reflection tests | Включить в общий MVP smoke |
| 13 | Persona/relationship/mood эволюция и rollback | Automated | Offline lifecycle smoke проверяет versioned persona/relationship/affect; reflection + desktop tests покрывают bounded evolution/rollback | Добавить multi-turn Bridge sequence |
| 14 | Dormant исключён из recall, deliberate search находит эпизод | Automated | Memory engine и desktop archive tests | Включить в общий MVP smoke |
| 15 | Codex login/logout, plan/rate limits, no token persistence | Automated | Race-enabled Bridge → executable fake app-server smoke проверяет device-code metadata, account/Plus plan, rate limits, provider probe, logout/onboarding reset и отсутствие unknown OAuth token canary в DTO/config/SQLite | — |
| 16 | Antigravity безопасно сообщает unsupported | Automated | Fail-closed `internal/providers/antigravity` adapter, typed `unsupported_auth_mode`, bridge/onboarding tests без config/keyring mutation и явное unavailable-состояние UI | Пересматривать только после появления официального разрешённого vendor contract |
| 17 | Обязательные macOS E2E smoke проходят | Automated | Universal app build/validation, Wails `OnDomReady` lifecycle smoke и native WebKit onboarding interaction выполняются в macOS CI | — |

## Порядок закрытия Stage 7

1. Остался критерий 11: native WebKit microphone/speech automation с barge-in. Onboarding DOM interaction, offline provider/Codex/plugin lifecycles, negative leak scan, prompt-injection agent loop, voice/chat contract, Codex logout UI и unsupported Antigravity contract уже закрыты.

Реальные OAuth credentials, платные provider calls, Developer ID, notarization и публикация артефактов не являются условиями локального OSS smoke-набора.

Текущий набор запускается командой `make mvp-smoke`, а на macOS — `make macos-launch-smoke macos-ui-smoke MACOS_APP=cmd/yuri/build/bin/yuri.app`. Первый включает race-enabled offline profile lifecycle, production-path plugin package lifecycle, Bridge → local OpenAI-compatible SSE lifecycle и Bridge → executable fake Codex app-server account lifecycle. macOS targets открывают собранный `.app` с изолированным profile root: lifecycle smoke проверяет `OnDomReady`/clean shutdown, а UI smoke внутри реального Wails WebKit нажимает onboarding, заполняет provider form, проверяет success и открытие Chat. UI flow использует test-only deterministic provider bridge double; production provider Bridge → HTTP lifecycle проверяется отдельным race-enabled smoke.
