# ADR-0006: Общая память Yuri с bounded snapshot и on-demand cross-session search

- Статус: accepted
- Дата: 2026-08-27
- Область: Этап 0 context boundary; storage/retrieval реализуется на Этапе 2

## Контекст

Новая сессия должна продолжать опыт Yuri из предыдущих диалогов, но полный архив нельзя отправлять в каждый prompt. Нужны одновременно стабильный prefix для prompt caching, live memory updates внутри run и возможность восстановить редкий эпизод с provenance.

## Решение

1. Все conversations принадлежат одной Yuri и используют общую memory/relationship/persona state.
2. На границе run создаётся `ContextSnapshot` в фиксированном порядке: immutable policy → identity seed → mutable persona → relationship/affect → bounded core memory → task context → retrieved history → current conversation.
3. Core memory ограничена token budget и хранит curated high-signal entries; неактивные записи переводятся в lifecycle state `dormant` и не попадают в обычный retrieval.
4. Полный transcript остаётся в SQLite. FTS5/session search и semantic/vector search являются отдельными projections; hybrid ranker выдаёт bounded snippets с session/source IDs и evidence.
5. Memory writes через текущий run публикуются в live state, но уже зафиксированный snapshot и immutable policy не мутируют. Следующий run получает обновлённый snapshot.
6. Перед compression выполняется memory flush/handoff compression; оригинальная история не перезаписывается.

## Последствия

Положительные:

- cross-session continuity без необозримого prompt;
- provenance и deliberate search позволяют отличить память от текущего факта;
- потеря индекса не уничтожает transcript.

Ограничения:

- retrieval может не найти слабый или противоречивый эпизод;
- memory lifecycle, sensitivity filter и token budget требуют отдельного тестирования;
- общая память принадлежит одному профилю и не является user-isolation mechanism для multi-user сценариев.

