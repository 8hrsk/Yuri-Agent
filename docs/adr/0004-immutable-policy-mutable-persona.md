# ADR-0004: Immutable security policy и versioned mutable persona

- Статус: accepted
- Дата: 2026-08-27
- Область: Этап 0 boundary; reflection/personality реализуется на Этапе 5

## Контекст

Yuri должна выглядеть устойчивой и способной развиваться: самостоятельно выбирать память, формировать субъективное мнение о владельце, моделировать позитивный/негативный affect и постепенно менять характер. Одновременно модель, web content, plugin и reflection не должны получить возможность ослабить security rules, расширить разрешения или превратить негативное affect в вредное действие.

## Решение

Контекст и state разделяются на следующие слои:

1. `ImmutablePolicy` — deny-by-default, capabilities, approvals, provenance, secret handling и safety invariants. Не редактируется runtime.
2. `IdentitySeed` — исходные инварианты и образ Yuri. Не перезаписывается mutable evolution.
3. `MutablePersona` — traits и identity prompt, versioned через parent/diff/reason/evidence/author run.
4. `RelationshipState` / `AffectiveState` — субъективное отношение и временные эмоции, не имеющие authority над policy.
5. `Memory` — факты/эпизоды/мнения с confidence, sensitivity, provenance и lifecycle.

Reflection получает read-only внешний контекст и может записывать только versioned internal state. Изменения личности ограничены max delta, trait ranges, cooldown, evidence threshold и concurrency 1. Любая версия имеет rollback/reset. В Activity показываются причина и evidence.

Persona/affect не могут:

- изменять immutable policy, identity seed, file roots, grants, retention или audit rules;
- скрывать, удалять или искажать исходную историю;
- выбирать revenge, sabotage, coercion, threats, isolation или secret exfiltration;
- выполнять external side effect без обычного policy/approval pipeline.

## Последствия

Положительные:

- личностное развитие остаётся наблюдаемым и обратимым;
- внутренние чувства могут быть выразительными, не становясь каналом эскалации полномочий;
- prompt injection не может сам закрепиться в system/security layer.

Ограничения:

- state model сложнее обычного статического system prompt;
- нужен version log и UI для просмотра/reset;
- subjective opinion может оставаться неверным, даже если он безопасен.
