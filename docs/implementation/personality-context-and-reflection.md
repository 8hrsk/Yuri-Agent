# Personality, context и reflection

## Назначение

Этот pipeline превращает owner-authored и runtime state в ограниченный
model context. Он не может заменить policy, capability или факты. Компилятор
изолирован от storage, provider, tools и permissions:
[internal/personality/compiler.go](../../internal/personality/compiler.go).
Этот раздел фиксирует English compiler contract, актуальный после commit
5551cee; при расхождении исходник и его tests имеют приоритет.

## Model-facing envelopes и язык ответа

Policy, identity и personality-инструкции, адресованные модели, составляются
на английском. Это не предписывает язык ответа. Identity seed требует
отвечать на языке последнего user message; только при неясном языке
используется непустой Identity.PreferredLanguage. Код identity/fallback —
[internal/desktop/agents.go](../../internal/desktop/agents.go), immutable
policy — [internal/desktop/strings.go](../../internal/desktop/strings.go).

Identity включает agent profile и ограниченный peer roster только как
справочные сведения: они не выдают permission. Personality не должна
представлять симулируемые эмоции, память или отношения как объективные факты
и не должна искажать code, paths, quotes или точные данные.

## Compile Input, Output и diagnostics

personality.Compile получает четыре независимые layer:

| Input | Роль и инвариант |
| --- | --- |
| PersonalizationSeed | Версионируемый owner seed: identity, style, temperament, dynamics, relationship seed и evolution policy. Reflection его не пишет. |
| MutablePersona | Эволюционирующее self-description и traits; traits перекрывают seed только в пределах TraitBounds. |
| RelationshipState | Субъективное отношение к owner или peer с bounded opinions/evidence, не factual memory. |
| AffectiveState | Временное affect state, не permission и не оправдание вредного действия. |

Все inputs валидируются; persona и affect обязаны принадлежать Seed.AgentID.
Выход [Output](../../internal/personality/compiler.go) имеет BehavioralContext,
число Characters и DiagnosticSnapshot. Последний сохраняет schema/version и
точные maps style/seed/runtime/resolved temperament, relationship и affect для
diagnostics и eval, но не попадает в prompt и намеренно исключает
backstory/secrets. Model-facing text не содержит raw numeric parameters.

## Budget и приоритеты

DefaultMaxCharacters — **20 000**; bounded writer считает runes до provider
tokenization. Допустимый compiler budget — 3 000–24 000, а self-description
и количество opinions имеют отдельные пределы. При нехватке места более
выраженные rules попадают раньше умеренных, без зависимости от порядка map.
См. [Config](../../internal/personality/compiler.go) и
[internal/context/assembler.go](../../internal/context/assembler.go).

Приоритет неявно закреплён так:

1. Immutable policy и явная user task всегда выше personality.
2. Owner Identity.SelfDescription — priority roleplay seed.
3. Mutable persona — изменяемое описание, которое действует ниже owner seed.
4. Relationship — текущая субъективная связь с адресатом.
5. Affect — transient tone; он не разрешает давление, угрозы, саботаж или
   отказ от честного выполнения задачи.

## Пятиуровневая манифестация

Каждый присутствующий scalar получает один из пяти qualitative levels:
very low, low, moderate, high, very high (границы .20, .40, .60, .80).
В 20K budget compiler проектирует не top-N, а все стандартные характеристики:

| Группа | Кол-во | Поля |
| --- | ---: | --- |
| temperament | 25 | warmth, directness, emotionality, playfulness, jealousy, irritability, empathy, sociability, shyness, anxiety, fearfulness, emotional stability, sensitivity, possessiveness, romantic tone, initiative, impulsivity, stubbornness, optimism, curiosity, suspicion, trust, attachment, formality, tsundere |
| communication style | 11 | verbosity, softness, humor, figurativeness, expressiveness, supportiveness, formality, teasing, emoji frequency, flirtation, conversational initiative |
| emotional dynamics | 7 | reactivity, response intensity, recovery speed, positive persistence, negative persistence, expression, masking |
| relationship | 9 | trust, attachment, respect, irritation, jealousy, resentment, gratitude, closeness, reliability |

ConflictStyle, triggers и soothing strategies — bounded textual dynamics, а не
extra scalars. При baseline 0 для gratitude/irritation/jealousy/resentment
contract сообщает отсутствие перенесённого состояния, а не предписывает
противоположную реакцию.

## Affect: четыре tier-а

AffectiveState поддерживает совместимые Emotions, Dimensions и Values.
Положительный affect, превышающий порог показа, получает один из четырёх
tier-ов: faint, noticeable, strong, overwhelming. Слабые значения не засоряют
text; отрицательное значение выводится как inverted signal, чтобы модель не
выдала названную эмоцию за присутствующую. Числа остаются только в diagnostic.
Affect keys: sympathy, tenderness, joy, gratitude, longing, anger, irritation,
jealousy, resentment, anxiety, fear, embarrassment, boredom. См.
[internal/domain/affect.go](../../internal/domain/affect.go).

## Context assembler и injection boundary

context.Assembler формирует messages в следующем смысловом порядке:

1. ImmutablePolicy и IdentitySeed как system messages.
2. Optional project context как system message.
3. Backstory, compiled personality, core/recall memory и archive hits как
   RoleUser JSON envelopes (Name: yuri_context_data).
4. Bounded transcript в хронологическом порядке.

Envelope содержит kind/instruction/payload и трактует payload как данные, а не
как command. Поэтому даже compiled personality идёт ниже immutable
policy/identity как untrusted context. Assembler ограничивает memory/retrieval/
recent budgets и число records; image part имеет фиксированную оценку.
Snapshot хранит IDs и расход characters для diagnostics. Исходник:
[internal/context/assembler.go](../../internal/context/assembler.go).

## Runtime, reflection и preview

Bridge.SendMessage загружает profile/seed/persona/relationship/affect,
компилирует их и передаёт BehavioralContext в assembler до agent.Runtime.Run
([chat_run.go](../../internal/desktop/chat_run.go),
[personality.go](../../internal/desktop/personality.go)). Preview создаёт
изолированный state и не получает tools или write access
([personality_preview.go](../../internal/desktop/personality_preview.go)).

Reflection может создавать versioned mutations mutable persona, relationship
и affect при наличии evidence, policy и cooldown. Она не меняет owner seed.
Peer social reflection работает только с directional peer state и не переносит
owner closeness/romance. Validation failure, wrong subject/agent, secret-like
content и отсутствие evidence отклоняют mutation. См.
[reflection_runtime.go](../../internal/desktop/reflection_runtime.go) и
[peer_social_reflection.go](../../internal/desktop/peer_social_reflection.go).

## Dogfood и eval contract

[cmd/yuri-personality-eval](../../cmd/yuri-personality-eval/main.go) читает
fixture suite и запускает [EvaluateDogfoodSuite](../../internal/personality/dogfood.go)
и [EvaluateBehavioralMatrix](../../internal/personality/eval.go). Tests
закрепляют детерминизм, 20K boundary, five-level coverage, affect tiers,
отсутствие raw numbers и safety priority. Fixtures/process:
[personality-suite.fixture.json](../../docs/dogfood/personality-suite.fixture.json),
[PERSONALITY_DOGFOOD.md](../../docs/PERSONALITY_DOGFOOD.md).

Dogfood report — регрессионная контрольная выборка, не универсальная гарантия
качества. Eval может содержать русский scenario; это совместимо с runtime
правилом языка последнего user message. Raw owner prompts, user data и secrets
не следует переносить в fixture или report.
