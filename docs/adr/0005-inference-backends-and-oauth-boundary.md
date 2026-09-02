# ADR-0005: Разделение ModelBackend и AgentHarnessBackend

- Статус: accepted
- Дата: 2026-08-27
- Область: Этап 0 provider boundary; adapters реализуются на Этапе 1

## Контекст

Yuri должна поддерживать обычный OpenAI-compatible endpoint и официальные subscription/harness flows. Эти режимы различаются по тому, кто управляет agent loop, tool intents, approvals и credentials. Нельзя выдавать официальный harness за raw API или импортировать токены другого клиента.

## Решение

Ввести единый порт `InferenceBackend` с двумя явно различными режимами.

### ModelBackend

Yuri сама управляет agent loop, отправляет messages/tools в Chat Completions или Responses-style API, обрабатывает streaming/structured tool calls, budgets, cancellation и локальную policy.

### AgentHarnessBackend

Официальный внешний harness возвращает поток событий, tool intents и approval requests. Yuri нормализует события, показывает их в run log и перед выполнением side effect повторно применяет свою policy. Harness sandbox/approval не расширяет local grants.

OpenAI subscription mode подключается только через официальный Codex App Server с managed login/logout, token refresh, plan/rate-limit metadata. Запрещены cookies, browser scraping и импорт token cache Codex CLI/ChatGPT. Antigravity остаётся disabled/unsupported до официального разрешённого integration contract; OAuth piggyback не реализуется.

Каждый provider adapter объявляет auth mode/capabilities, ограничивает timeout/size, поддерживает cancellation, redacts errors и не возвращает credential material за пределы adapter boundary.

## Последствия

Положительные:

- разные полномочия и lifecycle остаются явными;
- можно тестировать agent runtime без provider-specific login;
- security review может отдельно проверять API-key и official-harness paths.

Ограничения:

- два семейства contract tests и UI states;
- rate limits/plan metadata harness могут быть недоступны или измениться vendor-ом;
- без разрешённого Antigravity contract пользователь получает понятную недоступность, а не скрытую несовместимую интеграцию.
