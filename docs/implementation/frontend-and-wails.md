# Frontend и Wails

## Назначение

Frontend отображает durable/backend state и отправляет intent в Bridge. Он не
является security authority: доступ к файлам, сети, secrets, schedule и tool
side effects окончательно проверяется Go backend.

## Wails composition

[cmd/yuri/main.go](../../cmd/yuri/main.go) создаёт Wails app, встраивает
frontend/dist и bind-ит Bridge. Production binding строится в
[bridge_runtime.go](../../cmd/yuri/bridge_runtime.go); отдельный build-tag
binding path не открывает рабочую БД при генерации bindings:
[bridge_bindings.go](../../cmd/yuri/bridge_bindings.go).

Bridge methods — RPC surface для chat, history, approvals, agents/personality,
memory, collaboration, providers, plugins, backups и schedules. Каждому
вызову desktop задаёт bounded context; UI close не эквивалентен успешному
completion run.

## Client adapter

[frontend/src/lib/client/wails-client.ts](../../frontend/src/lib/client/wails-client.ts)
реализует typed YuriClient. Он ищет binding method, вызывает bridge и
нормализует wire DTO в UI contracts. Это удерживает snake/camel/nullable
различия на одном boundary, вместо распространения их по components.

[bridge.ts](../../frontend/src/lib/client/bridge.ts) и
[events.ts](../../frontend/src/lib/client/events.ts) инкапсулируют Wails RPC и
event subscriptions. Ошибка/unknown payload должны превратиться в UI error,
а не в unsafe DOM/data cast.

## Streaming и жизненный цикл views

App в [frontend/src/App.tsx](../../frontend/src/App.tsx) сначала проверяет
onboarding и agent roster, затем держит ChatView смонтированным между tab
переключениями, чтобы streaming/approval не потерялся. Активный agent
переключает scoped views; несохранённый model route предупреждает перед
navigation/close.

Chat bus в wails-client использует одну долгоживущую yuri:chat subscription.
Каждая подписка run привязывается к своему run ID; early events сопоставляются
oldest unclaimed conversation subscription, а retired IDs bounded-игнорируются.
Это предотвращает cross-stream при параллельных runs и не позволяет late
event попасть в следующий chat.

[ChatView](../../frontend/src/components/ChatView.tsx) хранит paged transcript,
streaming segments, approval state и attachment UI. UI показывает events, но
после reload снова запрашивает backend history/run data: event bus не
авторитетнее SQLite.

## Presentation и input safety

[markdown.ts](../../frontend/src/lib/markdown.ts) допускает только absolute
local paths или http/https URL для markdown links/media; другие schemes и
NUL-encoded local paths отбрасываются. Это presentation filter, не filesystem
authorization: backend всё равно canonicalizes/authorizes путь.

[chat-attachments.ts](../../frontend/src/lib/chat-attachments.ts) нормализует
browser attachment data перед RPC, а
[internal/desktop/chat_attachments.go](../../internal/desktop/chat_attachments.go)
проверяет backend contract. Голосовой UI в
[frontend/src/lib/voice.ts](../../frontend/src/lib/voice.ts) отделён от backend
voice service [internal/desktop/voice.go](../../internal/desktop/voice.go);
настройка auto-speak не означает permission на запись микрофона или сеть.

Frontend не рендерит raw model HTML как trusted application markup. Model,
tool, memory и external web text должны восприниматься как пользовательские
данные; безопасный link policy не превращает их в commands.

## Failure и cancellation boundary

UI может отправить cancel/approval decision, но Bridge владеет активным
context и terminal run state. Subscription cleanup не отменяет чужой run;
backend shutdown отменяет его независимо от видимости tab. При network/RPC
ошибке UI показывает recoverable state и может переполучить paged history,
но не синтезирует успешный tool/model result.

## Tests

Frontend tests покрывают client normalization, chat stream isolation,
pagination, accessibility, voice и attachments:
[ChatView.streaming.test.tsx](../../frontend/src/components/ChatView.streaming.test.tsx),
[ChatView.paging.test.tsx](../../frontend/src/components/ChatView.paging.test.tsx),
[voice-chat-smoke.test.ts](../../frontend/src/lib/voice-chat-smoke.test.ts) и
[chat-attachments.test.ts](../../frontend/src/lib/chat-attachments.test.ts).
Wails smoke paths расположены в [launch_smoke_test.go](../../cmd/yuri/launch_smoke_test.go).
