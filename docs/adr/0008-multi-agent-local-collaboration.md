# ADR-0008: Несколько именованных агентов и обезличенная делегация

- Статус: accepted
- Дата: 2026-08-29
- Область: Stage 8

## Контекст

Одна локальная установка принадлежит одному владельцу, но владелец хочет создавать несколько самостоятельных персонажей. Именованные агенты должны знать о существовании peers, сохраняя отдельные личности и приватные воспоминания. Для параллельной работы нужны subagents, однако превращать каждый task-run в новую личность нельзя. Также требуется bounded фоновое общение агентов без бесконечных циклов и расширения разрешений.

## Решение

1. `AgentProfile` — persistent owner-created identity. Имя, возраст, гендер и исходные ограничения принадлежат владельцу и не изменяются reflection.
2. Mutable persona, relationship, affect и private memory адресуются `agent_id`. Общие данные существуют только в явных `owner_shared`/`installation_shared` scopes.
3. Все именованные агенты получают bounded peer roster: ID, имя, статус и короткое описание. Приватная память, opinions, affect и credentials не публикуются в roster.
4. Subagent — ephemeral child run без `AgentProfile`, persona, affect, relationship и permanent memory. Он получает bounded redacted context, depth 1 и capabilities `parent ∩ delegation scope ∩ policy`.
5. Side effects subagent выполняются от имени principal named agent через обычные policy/approval/audit boundaries. Subagent не может создавать named agents или собственных subagents в первой реализации.
6. Inter-agent message — низкоприоритетный data envelope с authenticated local sender ID и provenance, но не system instruction. Он не выдаёт permission и не меняет состояние peer напрямую.
7. Background dialogue ограничивается purpose, TTL, max turns, token/time budgets, cooldown, concurrency, idempotency и защитой от циклов A→B→A.

## Последствия

- Single-user invariant сохраняется: `agent_id` не является `user_id` или tenant boundary.
- Переключение активного агента не должно смешивать private memory, relationship или affect.
- Agent roster и межагентные сообщения считаются недоверенным контекстом относительно immutable policy.
- Реализация Stage 8 идёт последовательными вертикальными срезами; наличие `AgentProfile` не означает, что delegation или background dialogue уже включены.
