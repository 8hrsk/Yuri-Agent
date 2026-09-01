# Inference providers

## Назначение

Provider layer адаптирует внешний model transport к agent.ModelBackend. Выбор
route выполняется desktop до запуска, а run хранит snapshot выбранного
provider/model и usage/failure attribution. Provider не получает полномочия
на local tools или конфигурационные секреты сверх необходимого запроса.

## Route и credential boundary

Agent profile имеет primary route и отдельный opt-in fallback route. Bridge
разрешает route в [chat_backend.go](../../internal/desktop/chat_backend.go),
создаёт backend и фиксирует его в AgentRun до model call. Provider configuration
содержит opaque CredentialRef; ключ извлекается через keyring только на
adapter boundary, а не сохраняется в config, SQLite run или UI event.

Probe в [providers_probe.go](../../internal/desktop/providers_probe.go)
считает onboarding завершённым только после реального успешного provider
проверочного вызова. Изменение конфигурации не переписывает route уже
работающего run.

## OpenAI adapter

[internal/providers/openai/client.go](../../internal/providers/openai/client.go) поддерживает
Responses и Chat Completions stream styles. Config валидирует base URL
(без credentials/query/fragment), модель и request limits. Client:

1. Валидирует ModelRequest и model capability до HTTP call.
2. Строит bounded request body и Authorization header внутри adapter.
3. Читает SSE либо compatible JSON и переводит их в normalized stream.
4. Различает first-byte и stream-idle timeout, ограничивает event/line/body.
5. Повторяет только подходящие network/status failures с bounded backoff.

Provider errors нормализуются в нейтральные failure classes: auth, quota,
rate limit, context/model unavailable, timeout, transient и invalid request.
Реализация: [config.go](../../internal/providers/openai/config.go),
[client.go](../../internal/providers/openai/client.go),
[errors.go](../../internal/providers/openai/errors.go),
[models.go](../../internal/providers/openai/models.go).

## Codex App Server adapter

[internal/providers/codexapp/client.go](../../internal/providers/codexapp/client.go) управляет
official local Codex App Server по JSON-RPC. Client ведёт bounded event loop и
thread subscriptions; backend создаёт отдельный server thread с read-only
sandbox и выключенным network, а dynamic tool calls мостит через
InteractiveToolStream.

Это не обходит Yuri authorization: app server не является local tool
authorizer, поэтому каждый динамический call всё равно проходит Runtime,
PolicyEvaluator и ApprovalHandler. Bridge запускает/переиспользует client
с singleflight и generation invalidation, ограничивает стартовый timeout и
закрывает процесс при Shutdown:
[providers_codex_launch.go](../../internal/desktop/providers_codex_launch.go),
[api.go](../../internal/providers/codexapp/api.go),
[backend.go](../../internal/providers/codexapp/backend.go).

OAuth token Codex App Server не передаётся в SQLite/config Yuri. Account state
проверяется provider probe, а не моделируется приложением.

## Unsupported adapter

[internal/providers/antigravity/disabled.go](../../internal/providers/antigravity/disabled.go)
намеренно возвращает stable unsupported_auth_mode вместо чтения OAuth cache,
cookies или выполнения сетевого login flow. Поддерживаемый путь — явно
настроенный OpenAI-compatible API key.

## Fallback, cancellation и observability

Fallback возможен только если primary provider дал ошибку до первого
видимого model или tool output и profile явно его допускает. Только одна
смена route допускается методом AgentRun.SwitchInferenceRoute; она
записывается в run/audit и сообщается UI. Логика pre-output attempt и switch:
[chat_fallback_runtime.go](../../internal/desktop/chat_fallback_runtime.go).

Provider context отменяется при cancel/deadline run. Ошибка adapter не должна
публиковать token, Authorization header, raw request или raw upstream body;
runtime redaction/normalization обеспечивает user-visible failure boundary.
Если fallback не применим, run terminally fails/cancels с сохранённой
inference failure metadata.

## Tests и operational contract

OpenAI tests покрывают request validation, multimodal shape, SSE/JSON,
timeouts, errors и model catalog; Codex tests — routing, API/client lifecycle
и nested dynamic tools. См.
[internal/providers/openai/client_test.go](../../internal/providers/openai/client_test.go),
[timeout_test.go](../../internal/providers/openai/timeout_test.go),
[internal/providers/codexapp/backend_test.go](../../internal/providers/codexapp/backend_test.go)
и [disabled_test.go](../../internal/providers/antigravity/disabled_test.go).
