# Multi-agent collaboration

## Назначение

Yuri реализует два раздельных collaboration flow: anonymous subagent
delegation для короткой read-only работы и named peer dialogue между
долговечными agent profiles. Они не являются общей памятью и не могут
вложенно бесконтрольно размножать runs.

## Anonymous delegation

[Delegation](../../internal/domain/delegation.go) связывает principal/parent
run с одним child run и содержит scope JSON, depth, budget, idempotency,
request hash, bounded result/failure, version и timestamps. Инварианты:

| Правило | Реализация |
| --- | --- |
| Глубина | Строго 1; subagent не может delegate дальше. |
| Identity | Child anonymous/ephemeral, без profile, persona и memory namespace. |
| Conversation | Child run имеет parent, но не владеет conversation. |
| Output | Result ограничен 16 KiB и сохраняется с status/provenance. |
| Idempotency | Один call ID + request hash не должен создать дублирующий child. |

Tool agent.delegate в
[delegation_tool.go](../../internal/desktop/delegation_tool.go) ограничивает
task и context, число делегаций на parent и запрашиваемые инструменты. Scope
равен пересечению request, parent registry и fixed read-only allowlist:
filesystem.read, web.fetch, web.search. Для filesystem/web дополнительно
проверяется parent policy/allowed directories.

Child получает ограниченный task/context и отдельную system policy без
personality, memory, roster, permissions, writes, side effects или nested
delegation. Создание child run и Delegation атомарно; ошибки validation,
scope/policy, budget/cancellation или child failure фиксируются в delegation
и возвращаются parent как bounded result, а не как скрытый успех.

## Named peer dialogue

[PeerDialogue](../../internal/domain/peer_dialogue.go) представляет durable
обмен между initiator и named peer. Значимые поля: participant IDs,
purpose, turn limit/usage, model budget, cooldown, trigger kind, pair key,
idempotency, recommendation/budget origin, completion reason, failure
provenance и timestamps.

| Bound | Текущий контракт |
| --- | --- |
| Purpose | до 256 characters |
| Opening message | до 4 000 Unicode code points |
| Сохранённое/generated сообщение | до 16 KiB UTF-8 |
| Turns | максимум 8 |
| Model budget | максимум 16K tokens, 300 s duration |
| Cooldown | максимум 24 h |
| Tools | отсутствуют внутри peer dialogue |

[peer_dialogue.go](../../internal/desktop/peer_dialogue.go) создаёт
persistent dialogue/messages, применяет per-pair cooldown и cancellation map.
Каждый peer получает ограниченный identity/policy, а peer transcript
оборачивается как untrusted data. Model обязана вернуть bounded structured
message/outcome; tool calling внутри диалога не разрешён.

## Peer social state

Owner relationship и peer relationship не смешиваются. На создание peer
направленный state инициализируется только social predispositions observer-а,
без owner backstory/closeness/romance:
[relationship.go](../../internal/domain/relationship.go).
[peer_social_reflection.go](../../internal/desktop/peer_social_reflection.go)
может обновлять только корректный directional relationship/affect и
отклоняет persona mutation, foreign peer, недоказанное opinion и secret-like
content.

## Autonomous peer dialogue

[peer_dialogue_autonomous.go](../../internal/desktop/peer_dialogue_autonomous.go)
работает только когда соответствующая настройка включена; по умолчанию flow
выключен. Serialized evaluator видит ограниченный roster/history и не имеет
tools. Он выдаёт schema no-change или start, а desktop применяет daily
limits, cooldown, quiet hours, duplicate suppression и audit. Автоматическая
рекомендация не вправе расширить hard budget.

[internal/executionbudget/resolver.go](../../internal/executionbudget/resolver.go) определяет
эффективный, balanced и extended peer budgets; recommendation сохраняет
origin/provenance, а не отменяет ceiling.

## Failure, security и persistence

Peer/delegation state лежит в SQLite repositories:
[delegations.go](../../internal/storage/sqlite/delegations.go) и
[peer_dialogues.go](../../internal/storage/sqlite/peer_dialogues.go).
Claim/create и terminal transitions проверяют ownership, version или
idempotency conditions. Bridge хранит cancellers peer runs и отменяет их при
Shutdown; terminal reason не теряется после UI disconnect.

Ни subagent, ни peer не получают автоматический доступ к owner files,
credentials, full transcript, hidden/highly-sensitive memory или права
другого agent. Модельный текст participant-ов — untrusted input; реальные
side effect tools остаются за обычной Runtime/policy/approval boundary.

## Tests

Проверки контрактов расположены в
[internal/desktop/delegation_tool_test.go](../../internal/desktop/delegation_tool_test.go),
[internal/desktop/peer_dialogue_test.go](../../internal/desktop/peer_dialogue_test.go),
[internal/storage/sqlite/delegations_test.go](../../internal/storage/sqlite/delegations_test.go)
и [peer_dialogues_test.go](../../internal/storage/sqlite/peer_dialogues_test.go).
