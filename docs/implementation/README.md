# Реализация Yuri Agent

Эта директория описывает текущую реализацию приложения, а не целевую
продуктовую архитектуру. Ссылки ведут на исходный код относительно этого
каталога; поведение, не подтверждённое кодом, здесь не считается контрактом.

## Карта исполнения одного интерактивного сообщения

```text
Wails/React UI
  -> desktop.Bridge.SendMessage
  -> SQLite: conversation, message, AgentRun
  -> route snapshot + personality/context assembly
  -> agent.Runtime
       -> provider stream <-> tool authorization/approval/execution
  -> SQLite: assistant message, run state, audit
  -> UI events; затем best-effort memory/reflection/background work
```

Точкой сборки служит [`Bridge`](../../internal/desktop/bridge.go), а не
frontend или конкретный провайдер. Он открывает хранилище, создаёт
репозитории, восстанавливает необходимые фоновые состояния и владеет
отменой активных запусков. Сам вызов модели и цикл tool calling находятся в
провайдер-независимом [`agent.Runtime`](../../internal/agent/runtime.go).

## Навигация

| Раздел | Что отвечает на вопрос |
| --- | --- |
| [architecture-and-boundaries.md](architecture-and-boundaries.md) | Какие слои есть, кто владеет жизненным циклом и где проходят границы доверия. |
| [domain-model.md](domain-model.md) | Базовые агрегаты, версии, ownership и доменные инварианты. |
| [agent-runtime-and-tools.md](agent-runtime-and-tools.md) | Как run обращается к модели, стримит события, вызывает и утверждает инструменты. |
| [personality-context-and-reflection.md](personality-context-and-reflection.md) | Англоязычный prompt pipeline, personality compiler, память в контексте и рефлексия. |
| [memory-and-storage.md](memory-and-storage.md) | Память, архив переписки, SQLite-проекции, миграции и восстановление. |
| [inference-providers.md](inference-providers.md) | Выбор маршрута, OpenAI, Codex App Server, fallback и ошибки inference. |
| [multi-agent-collaboration.md](multi-agent-collaboration.md) | Анонимные subagent delegation и именованные peer dialogue. |
| [scheduler-plugins-and-background.md](scheduler-plugins-and-background.md) | Расписания, proactivity, плагины и контролируемые фоновые задачи. |
| [frontend-and-wails.md](frontend-and-wails.md) | Wails bindings, frontend-клиент, события, streaming и UI boundaries. |
| [security-and-data-lifecycle.md](security-and-data-lifecycle.md) | Политика прав, approval, секреты, пути, сеть, backup и удаление данных. |
| [testing-and-operations.md](testing-and-operations.md) | Проверки, локальный запуск, диагностика и известные operational limits. |

## Что является источником истины

* Доменные типы и их валидация начинаются в
  [`internal/domain/ports.go`](../../internal/domain/ports.go).
* SQLite — долговременное хранилище; набор и порядок миграций закреплён в
  [`internal/storage/sqlite/migrator.go`](../../internal/storage/sqlite/migrator.go).
* Конфигурация содержит только не-секретные параметры. Путь к credential —
  это ссылка, сами секреты запрашиваются на provider boundary через keyring.
  См. [`internal/config/config.go`](../../internal/config/config.go) и
  [`internal/desktop/chat_backend.go`](../../internal/desktop/chat_backend.go).
* Event bus предназначен для локального процесса и обновляет UI, но не
  заменяет запись terminal state в SQLite. См.
  [`internal/app/event_bus.go`](../../internal/app/event_bus.go).

## Термины

* **owner seed** — неизменяемая обычной рефлексией, версионируемая
  owner-authored база `PersonalizationSeed`.
* **mutable persona**, **relationship** и **affect** — отдельные
  versioned snapshots, которые могут изменяться в разрешённых потоках и
  всегда имеют provenance.
* **run** — единица выполнения модели/инструментов с budget, terminal state
  и снимком inference route; не синоним UI-сообщения.
* **untrusted context** — данные, переданные модели как пользовательский
  envelope: они могут помочь ответить, но не вправе менять policy,
  permissions или задачу.

Дальнейшие разделы намеренно не содержат секретов, raw owner data или полных
prompt templates. Для изменения поведения используйте исходники и тесты,
ссылки на которые приведены рядом с соответствующим контрактом.
