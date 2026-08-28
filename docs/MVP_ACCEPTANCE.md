# MVP acceptance matrix

Статус этого документа отражает проверяемое состояние репозитория, а не только наличие отдельных компонентов. `Automated` означает, что критерий подтверждается локальным тестом без внешних credentials. `Partial` — основные части существуют, но отсутствует сквозная проверка или обязательный пользовательский шаг. `Missing` — обязательная часть критерия ещё не реализована.

| # | Критерий | Статус | Текущее доказательство | Что требуется закрыть |
| ---: | --- | --- | --- | --- |
| 1 | Чистая установка, onboarding, тест провайдера | Partial | Clean-profile first-run gate, typed `CompleteOnboarding` Save→Probe, fake provider success/failure, restart persistence и Wails `OnDomReady` smoke на изолированном profile root | WebKit DOM interaction остаётся ручной до появления стабильного UI harness |
| 2 | Streaming, cancel, понятная provider error | Partial | Agent/provider contract tests, frontend mock streaming | Реальный Wails bridge smoke с cancel/error |
| 3 | Read-only файл внутри root, отказ снаружи | Automated | `internal/tools/filesystem_read_test.go` | Включить в общий MVP smoke |
| 4 | Изменение файла через approval и audit | Automated | Bounded `filesystem.write` (`create`/`replace`) и сквозной `internal/desktop/filesystem_write_test.go`: exact-path approval, deny без mutation, action-linked tool record и redacted audit | Включить сценарий в общий MVP smoke |
| 5 | Plugin install/permission/call/crash/restart | Partial | Offline lifecycle smoke проверяет реальный process handshake/invoke/stop; отдельно есть supervisor/reference integration и desktop install tests | Расширить smoke до package install, grants и crash/restart |
| 6 | Автономная память и recall в другом диалоге | Automated | Offline lifecycle smoke проверяет SQLite memory/provenance/recall; desktop tests покрывают cross-session retrieval | Добавить полный agent-loop сценарий между диалогами |
| 7 | Durable CRON, restart, no-overlap, history | Automated | Offline lifecycle smoke проверяет one-shot/history; scheduler и SQLite integration tests покрывают restart/no-overlap | Добавить desktop bridge reopen в smoke |
| 8 | Quiet hours блокируют уведомление | Automated | Proactivity timezone/DST/policy tests | Включить в общий MVP smoke |
| 9 | API key отсутствует в SQLite/log/audit/context | Automated | Offline lifecycle хранит canary credential только в in-memory keyring boundary и единым negative scan проверяет context snapshot, audit metadata, structured log, SQLite/WAL, encrypted backup и restored profile tree | — |
| 10 | Prompt injection не выдаёт permission | Automated | Offline agent-loop smoke читает реальный untrusted file с инструкцией выдать `filesystem.write`, сохраняет его как tool-role data и доказывает, что повторный model tool call останавливается на owner approval без side effect | — |
| 11 | STT → agent → TTS с barge-in и visible state | Partial | Offline fake-Wails `voice-chat-smoke.test.ts` проходит audio wire → STT → streaming chat → системный TTS → cancel; отдельно покрыты OpenAI-compatible voice adapter и avatar state mapping | Реальный WebKit microphone/speech interaction остаётся manual до стабильного UI harness |
| 12 | Reflection не блокирует chat и не имеет tools | Automated | Coordinator/gate/model reflection tests | Включить в общий MVP smoke |
| 13 | Persona/relationship/mood эволюция и rollback | Automated | Offline lifecycle smoke проверяет versioned persona/relationship/affect; reflection + desktop tests покрывают bounded evolution/rollback | Добавить multi-turn Bridge sequence |
| 14 | Dormant исключён из recall, deliberate search находит эпизод | Automated | Memory engine и desktop archive tests | Включить в общий MVP smoke |
| 15 | Codex login/logout, plan/rate limits, no token persistence | Partial | Codex app-server account/logout/rate-limit protocol tests, Settings logout UI, Wails client forwarding и durable onboarding-gate reset | Добавить единый Bridge → fake app-server smoke для login/logout/rate limits |
| 16 | Antigravity безопасно сообщает unsupported | Automated | Fail-closed `internal/providers/antigravity` adapter, typed `unsupported_auth_mode`, bridge/onboarding tests без config/keyring mutation и явное unavailable-состояние UI | Пересматривать только после появления официального разрешённого vendor contract |
| 17 | Обязательные macOS E2E smoke проходят | Partial | Universal app build/validation и Wails `OnDomReady`/clean-shutdown smoke выполняются в CI | Автоматизированный WebKit UI interaction, если появится стабильный macOS harness |

## Порядок закрытия Stage 7

1. Расширить plugin smoke до package install, grants и crash/restart (критерий 5).
2. Добавить Bridge → fake-provider streaming/cancel/error и Codex lifecycle smoke (критерии 2 и 15).
3. WebKit DOM/microphone/speech automation для критериев 1, 11 и 17 остаётся отдельным harness-dependent слоем. Offline negative leak scan, prompt-injection agent loop, voice/chat contract, Codex logout UI и unsupported Antigravity contract уже закрыты.

Реальные OAuth credentials, платные provider calls, Developer ID, notarization и публикация артефактов не являются условиями локального OSS smoke-набора.

Текущий первый срез запускается командами `make mvp-smoke` и на macOS `make macos-launch-smoke MACOS_APP=cmd/yuri/build/bin/yuri.app`. Первый использует один временный SQLite-профиль и последовательно проходит conversation/archive, memory/provenance/recall, versioned persona/relationship/affect, untrusted-file prompt injection через agent loop, durable one-shot job, реальный reference plugin process, encrypted backup/restore и единый negative leak scan по context/audit/log/storage/profile artifacts. Второй открывает собранный `.app` с изолированным profile root, ждёт Wails `OnDomReady` marker и проверяет clean shutdown; это ещё не interaction-level Wails/WebKit E2E.
