# Agent runtime и tools

## Назначение

Пакет [internal/agent/runtime.go](../../internal/agent/runtime.go) реализует provider-независимый
цикл model → tool → model. Desktop orchestration подготавливает run и backend,
но именно Runtime применяет budget, нормализует stream и проводит каждое
намерение модели через authorization/approval boundary.

## Базовые типы

| Тип | Главные поля/варианты | Инвариант |
| --- | --- | --- |
| Message / ContentPart | роли system, developer, user, assistant, tool; text/image и tool calls | ModelRequest валидирует message shape до provider call. |
| ToolDescriptor | name, JSON schema, risk, required capabilities | Дескриптор описывает возможность, но сам не выдаёт её. |
| ToolCall | ID, name, JSON arguments | Это намерение модели, не разрешённое действие. |
| ModelRequest | model, messages, tools, tool choice, max tokens, temperature, metadata | Имена tools уникальны; schemas и messages валидны. |
| RunRequest / RunResult | run/conversation IDs, request, budget, event sink; final usage/output | Runtime не создаёт durable run сам. |
| Event | started, text delta, tool-call lifecycle, approval, completed/failed | Не предназначен для вывода chain-of-thought. |

Определения находятся в [types.go](../../internal/agent/types.go); реестр
в [tools.go](../../internal/agent/tools.go) concurrency-safe, отвергает
дубликаты и отдаёт descriptors в стабильном алфавитном порядке.

## Выполнение run

[Runtime.Run](../../internal/agent/runtime.go) нормализует model request и
budget, ставит deadline из RunBudget.MaxDuration и эмитит start. По умолчанию
budget ограничивает число model steps, total tokens, tool calls, размер tool
output и длительность (значения по умолчанию: 8, 32K, 32, 256 KiB, 10 min).

На каждом step Runtime получает либо обычный ModelStream, либо
InteractiveToolStream для backend с динамическими tools. Он:

1. Нормализует provider events в `agent.Event`.
2. Накапливает usage и прекращает run при budget/deadline.
3. Добавляет assistant message и tool result в следующую модельную итерацию.
4. При required-tool mode делает одну controlled retry, если инструмент не
   был вызван.
5. Возвращает model-visible tool error как data, если это не terminal
   cancellation/deadline/budget/approval failure.

Desktop связывает result с immutable assistant segments и доменным AgentRun в
[internal/desktop/chat_run.go](../../internal/desktop/chat_run.go).
Пакет internal/app помогает создавать/переходить run и публиковать локальные
events, но не заменяет actual model loop:
[internal/app/service.go](../../internal/app/service.go).

## Tool execution и idempotency

В [runtime.go](../../internal/agent/runtime.go) executeTool выполняет границу
в таком порядке:

1. Проверяет, что arguments — JSON object, и находит зарегистрированный tool.
2. Строит idempotency key из call ID либо name + canonical argument hash.
   Повтор ключа с другими arguments — hard error.
3. Вызывает authorizer непосредственно перед исполнением.
4. При deny возвращает error result; при approval requirement эмитит event и
   ждёт ApprovalHandler; при approved action передаёт approved run context.
5. Запускает Tool/ApprovalAwareTool, ограничивает byte size результата и
   сохраняет model-visible success/error result.

ApprovedRunID в context связывает решение с конкретным run; desktop policy
не даёт reuse approval для другого action. Runtime не подменяет ошибки
неизвестного/невалидного tool успешным текстом.

## Tools, policy и interactive backends

В [internal/desktop/chat_fallback_runtime.go](../../internal/desktop/chat_fallback_runtime.go)
создаётся registry для конкретного top-level run. Delegate и peer tools
регистрируются только вне subagent. Desktop authorizer использует policy и
approval gates; background authorizer уже ограничен набором допустимых tools.

Tool implementations, например [web_fetch.go](../../internal/tools/web_fetch.go),
живут в отдельном adapter layer. Их
input/output — недоверенные данные для последующего model turn. В частности,
внешний tool output не может менять system policy. Ограничения filesystem,
web и secrets описаны в
[security-and-data-lifecycle.md](security-and-data-lifecycle.md).

## Ошибки, cancellation и visible output

Runtime проводит terminal event через context.WithoutCancel, чтобы UI мог
закрыть stream даже после отмены исходного request. При cancellation/deadline
run становится cancelled/failed согласно domain transition, а не completed.
Ошибки проходят secret-like redaction: см.
[errors.go](../../internal/agent/errors.go) и
[inference_failure.go](../../internal/agent/inference_failure.go).

Fallback разрешён только до первого видимого model/tool output и только на
явно enabled route. Bridge сохраняет смену в AgentRun и audit; один run не
может бесконечно перебирать providers. Реализация:
[chat_fallback_runtime.go](../../internal/desktop/chat_fallback_runtime.go).

## Persistence и observability

Runtime имеет in-memory state на время вызова. Durable факты пишут desktop и
SQLite repositories: состояние/usage/failure AgentRun, tool call/audit,
сообщения и approvals. Event bus может потеряться при restart; terminal run
record — нет. Для анализа используйте audit и run failure metadata, а не
сырые model prompts или секреты.

## Тестовая граница

[internal/agent/runtime_test.go](../../internal/agent/runtime_test.go) и
[inference_failure_test.go](../../internal/agent/inference_failure_test.go)
закрепляют budgets, approval, idempotency, tool-result bounding, cancellation
и нормализацию failures.
