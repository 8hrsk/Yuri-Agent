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
8. Permanent memory создаётся приватной. Владелец может явно опубликовать принадлежащую агенту запись как `owner_shared` или `installation_shared` и отозвать публикацию обратно в `agent_private`. Scope меняется новой append-only revision (`publish`/`revoke`), ownership и provenance сохраняются; highly-sensitive записи не публикуются.
9. Completed peer dialogue порождает по одной private episodic projection для каждого участника. Детерминированный digest фиксирует событие и bounded-фрагмент завершения, но не интерпретирует его как opinion/affect. Projection имеет стабильный ID и provenance на dialogue/message log; bounded startup reconciliation восстанавливает пропущенную запись после crash.
10. После episodic projection отдельный bounded social-reflection pass формирует независимое направленное отношение `observer → peer` и, при наличии evidence, краткоживущий affect event observer. Peer transcript остаётся untrusted data; persona, owner relationship, factual memory и permissions не являются допустимыми targets. State revisions и terminal marker коммитятся атомарно, а пропущенный pass повторяется не более чем для одного completed dialogue за следующий model-backed background cycle.
11. Владелец видит directional relationship только в области активного observer. UI маркирует opinion/inference как субъективные данные и показывает version/evidence history. Reset и rollback не редактируют старые строки: они создают новую relationship revision и owner audit event.
12. Автономное открытие peer dialogue является отдельной opt-in настройкой, выключенной по умолчанию. После интерактивного turn tool-less reviewer выбирает `no_change` либо одного peer и sanitized task abstraction. Dispatch повторно ограничен quiet hours, отдельными daily limit/cooldown и одним dialogue на root run; trigger kind/reason сохраняются как durable provenance.

## Последствия

- Single-user invariant сохраняется: `agent_id` не является `user_id` или tenant boundary.
- Переключение активного агента не должно смешивать private memory, relationship или affect.
- Agent roster и межагентные сообщения считаются недоверенным контекстом относительно immutable policy.
- Реализация Stage 8 идёт последовательными вертикальными срезами; наличие `AgentProfile` или delegation не означает, что background dialogue уже включён.
- Пустой delegation scope сохраняет tool-less режим. Отдельный read-only scope/policy slice разрешает максимум три явно запрошенных tools из `filesystem.read`, `web.search`, `web.fetch`; итоговый registry вычисляется как `request ∩ parent registry ∩ immutable allowlist`, filesystem использует только ранее одобренные roots, а execution-time authorizer повторяет проверку перед вызовом. Это не скрытое или наследуемое право subagent.
- Peer-dialogue запускается явным tool intent named root-agent: после opening `A → B` участники чередуются в отдельных background runs в диапазоне `min_turns..max_turns`. У каждого turn пустой tool registry и структурированный outcome `continue|complete`; ранний semantic completion игнорируется до минимума, legacy plain text завершается после минимума, а hard limits всегда побеждают. Aggregate хранит причину завершения, message log и run provenance; один active exchange на пару, TTL/cooldown/idempotency и no-retry recovery после restart остаются обязательными.
- Следующий autonomous-trigger slice переиспользует тот же aggregate/runtime и не создаёт новый канал общения. Reviewer не может вызвать tools или передать peer private memory/system prompts; secret-like proposal отклоняется до persistence, а отсутствие необходимости фиксируется как bounded redacted audit decision.
- Context retrieval одного агента видит только его `agent_private` records и явно опубликованные shared records. Чужие private records не участвуют ни в core snapshot, ни в lexical/vector candidate set. Shared memory маркируется scope и owning agent в model context; отзыв действует со следующего retrieval/snapshot.
- Peer-dialogue episodes относятся к `episodic`, поэтому не попадают в always-on core. Они доступны participant через релевантный recall и остаются private, пока владелец явно не изменит scope. Failed/cancelled/expired dialogue не создаёт эпизод.
- Directional peer opinion не публикуется в roster и не смешивается с owner relationship. Следующий peer dialogue получает только opinion отвечающего агента о конкретном собеседнике и его собственный affect; противоположное мнение второго агента остаётся приватным.
