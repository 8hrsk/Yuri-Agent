# Scheduler, plugins и background work

## Назначение

Эти компоненты выполняют долгоживущую работу без превращения фонового запуска
в обход interactive approval. Все job/plugin lifecycle состояния имеют
отдельные durable records и cancellation boundary.

## Scheduler model

[domain.Schedule](../../internal/domain/scheduler.go) описывает once, interval
или five-field cron schedule. Основные поля: ID/name, expression/timezone,
start/interval, opaque payload, enabled/status, misfire policy, next/last run,
retry policy, job budget, no-overlap, history и version. Active schedule
должен иметь next run; статусами служат active, paused, completed/deleted.

JobRun хранит stable execution key, claim/lease, queued/running/succeeded/
failed/cancelled/skipped state, attempt и result/failure metadata. Stable key
позволяет отличать retry/recovery от нового logical invocation.

## Scheduler execution

[Scheduler](../../internal/scheduler/scheduler.go) по умолчанию использует
polling, lease, bounded claims/concurrency, max run duration и graceful stop.
Он валидирует cron, восстанавливает expired leases, вычисляет due/misfire/
retry tasks, atomically claim-ит их в SQLite и renew-ит lease во время работы.

No-overlap, lease и CAS не дают двум workers выполнить один и тот же job
одновременно. Stop отменяет execution contexts и ждёт bounded shutdown.
Repository logic: [scheduler_claim.go](../../internal/storage/sqlite/scheduler_claim.go),
[scheduler_runs.go](../../internal/storage/sqlite/scheduler_runs.go) и
[scheduler_tx.go](../../internal/storage/sqlite/scheduler_tx.go).

Tool scheduler.create в
[internal/desktop/scheduler_tool.go](../../internal/desktop/scheduler_tool.go)
создаёт предложение schedule как medium-risk action: model не может
самостоятельно включить неизменяемую user obligation без требуемого approval.
Scheduled execution получает отдельный bounded job context и не наследует
любые случайные interactive permissions.

## Proactivity

[internal/proactivity/policy.go](../../internal/proactivity/policy.go) задаёт
daily limit, cooldown, quiet hours/DST и reservation semantics. Policy
намеренно не зависит от persistent store; Bridge восстанавливает ограниченный
недавний ledger из audit и выпускает notification/audit через
[internal/desktop/proactivity.go](../../internal/desktop/proactivity.go).

Отсутствие восстановленного ledger после соответствующего retention window
означает, что прежняя запись больше не используется для dedup. Это operational
limit, а не разрешение игнорировать текущие quiet-hour/cooldown checks.

## Plugin trust и lifecycle

Plugins запускаются как отдельные processes с JSON-lines protocol, не как
код внутри desktop process. [Manifest](../../internal/plugins/manifest.go)
описывает identity/version, executable, schemas и запрошенные permissions.
Installation в [plugins_install.go](../../internal/desktop/plugins_install.go)
копирует package в managed plugin directory, проверяет contents/checksum/
compatibility/trust и persist-ит plugin disabled до owner enable flow.

| Boundary | Реализация |
| --- | --- |
| Trust | Ed25519 trust store в [truststore.go](../../internal/plugins/truststore.go); unsigned/unverified package допустим только с global development mode. |
| Consent | Enable выдаёт только manifest-requested grants, может их сузить; broad/wildcard grant требует явного режима и имеет expiry. См. [plugins_consent.go](../../internal/desktop/plugins_consent.go). |
| Process | Supervisor выполняет handshake, bounded RPC/frame IO, health/restart и process-group termination. См. [supervisor.go](../../internal/plugins/supervisor.go). |
| Tool call | Plugin tool всё равно проходит Yuri runtime/policy boundary; plugin output — untrusted model data. |
| Persistence | Plugin/grant metadata находится в SQLite migration 000004 и repositories [plugins.go](../../internal/storage/sqlite/plugins.go). |

Plugin cancellation и crash не должны оставлять worker hostage: supervisor
останавливает process tree, нормализует error и не считает interrupted action
успешным. Bridge запускает enabled plugins на Startup и bounded-останавливает
их на Shutdown.

## Failure и security boundaries

* Scheduler/job payload — opaque data, но не tool grant.
* Misfire/retry policy не отменяет no-overlap, budget или approval rule.
* Plugin manifest не самодостаточное consent: owner grant и scope обязательны.
* External plugin data, event и tool output не могут изменить system policy.
* Background execution лишается interactive UI approval path; если действие
  требует approval, оно не должно silently выполнить side effect.

## Tests

[internal/scheduler/scheduler_test.go](../../internal/scheduler/scheduler_test.go),
[concurrency_test.go](../../internal/scheduler/concurrency_test.go),
[internal/plugins/runtime_test.go](../../internal/plugins/runtime_test.go),
[manifest_test.go](../../internal/plugins/manifest_test.go) и
[internal/desktop/scheduler_tool_test.go](../../internal/desktop/scheduler_tool_test.go)
покрывают claim/lease, cron/misfire, concurrency, protocol/trust/cancellation
и scheduler approval flow.
