# Архитектура и границы

## Назначение

Этот документ фиксирует фактическое разделение Yuri Agent на UI,
orchestration, домен, runtime, persistence и внешние адаптеры. Цель —
изменять один слой без неявного переноса полномочий в другой.

## Слои и владение

| Слой | Реализация | Владеет | Не должен делать |
| --- | --- | --- | --- |
| Desktop composition root | [`internal/desktop/bridge.go`](../../internal/desktop/bridge.go) | конфигом, SQLite, cancel-функциями, scheduler/plugin lifecycle, Wails events | сам быть model runtime или policy engine |
| Application orchestration | [`bridge.go`](../../internal/desktop/bridge.go), [`service.go`](../../internal/app/service.go) | UI-командами, созданием run, аудитом, сбором зависимостей | доверять model tool call как разрешению |
| Domain | [`ports.go`](../../internal/domain/ports.go) | типами, валидацией, версиями и port-интерфейсами | хранением, HTTP, keyring, Wails |
| Runtime | [`runtime.go`](../../internal/agent/runtime.go) | bounded model/tool loop, stream normalization, budgets, cancellation | выбором секрета или обходом policy |
| Context/memory/reflection | [`assembler.go`](../../internal/context/assembler.go), [`engine.go`](../../internal/memory/engine.go), [`reflection/engine.go`](../../internal/reflection/engine.go) | bounded context и versioned knowledge/personality changes | выдачей capability |
| Adapters | [`sqlite/open.go`](../../internal/storage/sqlite/open.go), [`openai/client.go`](../../internal/providers/openai/client.go), [`web_fetch.go`](../../internal/tools/web_fetch.go), [`plugins/manifest.go`](../../internal/plugins/manifest.go) | конкретными протоколами и IO | подменой domain ownership |
| UI | [`App.tsx`](../../frontend/src/App.tsx), [`cmd/yuri/main.go`](../../cmd/yuri/main.go) | отображением, вводом и transport к Bridge | хранением секретов или финальным решением о правах |

`cmd/yuri` собирает Wails-приложение и bind-ит `Bridge`; production bridge
создаётся в [`cmd/yuri/bridge_runtime.go`](../../cmd/yuri/bridge_runtime.go).
`Bridge.NewBridge` создаёт закрытые data/blob/log/plugin директории, открывает
SQLite, применяет миграции, восстанавливает peer dialog state и строит
фоновые сервисы. `Startup` запускает plugins/scheduler, а `Shutdown`
отменяет chat и peer runs, останавливает scheduler/plugins, ждёт worker-ы и
закрывает provider client и БД. Следовательно, отдельный UI-tab не владеет
долгоживущей работой.

## Границы доверия и данных

```text
owner / UI input ─┐
external web/file ├─> tool result, archive, memory, backstory
peer/subagent ────┘            │ (untrusted data)
                                v
immutable policy + identity system messages
                                v
                    model request / model tool intent
                                v
                  Runtime + PolicyEvaluator + Approval
                                v
                 allowed adapter / durable audit / UI event
```

У модели нет прямой capability. `ToolCall` — намерение, а не grant;
authorizer запускается непосредственно перед tool execution. Непроверенные
данные (архив, память, backstory, peer transcript, результаты сети) не могут
переписать system policy. Детали формата контекста — в
[personality-context-and-reflection.md](personality-context-and-reflection.md),
права — в [security-and-data-lifecycle.md](security-and-data-lifecycle.md).

## Интерактивный data flow

1. UI передаёт запрос в `Bridge.SendMessage`
   ([`internal/desktop/chat_run.go`](../../internal/desktop/chat_run.go)).
   Текст/attachment проверяются, создаются conversation/message и
   `AgentRun`; route и budget фиксируются до обращения к модели.
2. Bridge читает transcript, agent profile, owner seed, mutable persona,
   owner relationship и affect. `context.Assembler` добавляет policy,
   identity, project context, personality и релевантные memory/archive
   records в ограниченном размере.
3. `agent.Runtime.Run` стримит ответ выбранного `ModelBackend`, исполняет
   разрешённые инструменты либо переводит run в ожидание approval.
4. Terminal outcome сохраняется. При успехе assistant segments становятся
   immutable message, conversation получает touch; фоновые title/memory/
   reflection действия не являются условием выдачи уже готового ответа.

Все durable transition выполняются через репозитории с version/CAS там, где
состояние конкурентно изменяемо. UI event помогает отрисовать прогресс, но
после restart источником истины остаётся SQLite.

## Configuration и process boundaries

[`Config`](../../internal/config/config.go) хранит locale, разрешённые
директории, provider configuration, preference и пути. `CredentialRef`
намеренно opaque: API key не записывается ни в JSON config, ни в run record.
`Config.Save` пишет атомарно в закрытую директорию. Режим тестового профиля
разрешён только при совместно заданных `YURI_TEST_MODE` и
`YURI_TEST_PROFILE_ROOT`.

OpenAI — HTTP-adapter; Codex — отдельный local App Server process; plugins —
внешние supervised processes. Каждый из них получает ровно необходимый
контекст, а не handle к SQLite или desktop config. См.
[inference-providers.md](inference-providers.md) и
[scheduler-plugins-and-background.md](scheduler-plugins-and-background.md).

## Failure и cancellation boundaries

* Bridge сохраняет cancel-функции top-level chat и peer dialogue по ID run;
  `Shutdown` сначала помечает процесс shutting down и отменяет их.
* Runtime создаёт context с `RunBudget.MaxDuration`. Terminal cancellation
  event эмитится с контекстом без cancellation, чтобы UI смог закрыть поток;
  ошибка всё равно становится `cancelled`, а не успешным ответом.
* Provider failure до первого видимого output может переключиться на ровно
  один явно разрешённый fallback route. Это отдельное доменное изменение run,
  не бесшумная замена модели. См.
  [`internal/desktop/chat_fallback_runtime.go`](../../internal/desktop/chat_fallback_runtime.go).
* Ошибки tool/policy/approval и смысловые model failures нормализуются и
  проходят redaction до user-visible event. Состояние `failed`/`cancelled`
  фиксируется до публикации terminal результата.

## Persistence boundary

SQLite открывается одним соединением с WAL, foreign keys и busy timeout;
миграции применяются транзакционно и имеют checksum. До pending migration
создаётся raw snapshot, а down migrations не используются. Реализация и
восстановление описаны в [memory-and-storage.md](memory-and-storage.md).

`Paths.PebbleDirectory` присутствует в конфигурации, но в текущем дереве нет
подключённого Pebble adapter. Его не следует считать рабочим хранилищем:
это зарезервированный путь/возможный backlog, а не persistence contract.
