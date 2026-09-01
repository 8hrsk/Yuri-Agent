# Roadmap персонализации агента

Статус: рабочий план
Дата: 2026-08-31
Область: развитие Stage 5 после завершения текущего Stage 8 среза

## 1. Цель

Сделать именованных агентов заметно разными, последовательными и постепенно развивающимися личностями. Настройки должны предсказуемо проявляться в речи и реакциях, а не оставаться набором чисел, трактовка которых полностью зависит от выбранной LLM.

Пользователь должен уметь:

- быстро создать выразительного агента без заполнения десятков полей;
- при желании детально настроить манеру общения, темперамент, эмоциональную динамику, исходное отношение и вымышленную биографию;
- до создания увидеть примеры поведения агента;
- понимать, что в состоянии агента является устойчивой чертой, текущей эмоцией, отношением или субъективным мнением;
- наблюдать изменения личности, закреплять важные свойства и откатывать эволюцию;
- получать различимое поведение при смене агента или модели без ослабления security policy и качества выполнения задач.

## 2. Текущая отправная точка

В проекте уже существуют:

- owner-defined `AgentProfile`: имя, возраст, гендер, краткие предпочтения и fictional backstory;
- 25 исходных traits в диапазоне `[0, 1]`;
- отдельные versioned `MutablePersona`, `RelationshipState` и `AffectiveState`;
- evidence-linked post-turn reflection с max-delta, cooldown, pin, reset и rollback;
- субъективные opinions о владельце и направленные peer relationships;
- agent-scoped memory и cross-session retrieval;
- Personality, Relationship и Collaboration UI.

Главный технический разрыв: runtime передаёт модели prompt и сырые значения наподобие `shyness=0.80`, `relationship.trust=0.45` и `affect.irritation=0.20`, но не преобразует их в достаточно конкретные правила речи и реакции. Поэтому один и тот же профиль может слабо или по-разному проявляться у разных моделей.

## 3. Принципы реализации

1. **Не один гигантский prompt.** Identity, communication style, temperament, emotional dynamics, relationship, affect, backstory и memory остаются отдельными типизированными слоями.
2. **Trait, relationship и affect не взаимозаменяемы.** Например, ревнивость — предрасположенность, relationship jealousy — состояние конкретной связи, affect jealousy — текущая реакция.
3. **Сначала поведение, затем UI.** Новая настройка попадает в creation flow только после того, как определена её семантика и детерминированная компиляция в model context.
4. **Quick mode не скрывает магию.** Архетип или preset разворачивается в обычные видимые поля; пользователь может открыть и изменить результат.
5. **Owner seed и runtime state разделены.** Исходный профиль служит точкой reset и задаёт разрешённые границы эволюции; текущая persona развивается отдельными append-only версиями.
6. **Negative traits меняют тон, но не надёжность.** Страх, ревность, злость, обида, подозрительность и раздражение не могут давать разрешения, провоцировать месть, саботаж, принуждение, сокрытие данных или ухудшение качества задачи.
7. **Любое развитие объяснимо.** Изменения persona, relationship и affect имеют evidence, reason, author run и возможность rollback.
8. **Одна веха за раз.** Следующая веха начинается после прохождения критериев готовности предыдущей; параллельная работа допустима только внутри одной вехи.

## 4. Целевая модель

### 4.1. Identity

Owner-authored и не изменяется обычной рефлексией:

- имя и варианты обращения;
- возраст;
- гендер и местоимения;
- основной язык;
- короткое самоописание;
- роль или образ агента;
- fictional backstory.

Изменение identity владельцем является явной операцией с новой версией seed, а не результатом фоновой рефлексии.

### 4.2. Communication style

Непосредственно управляет формой ответа:

- краткость ↔ подробность;
- мягкость ↔ резкость;
- серьёзность ↔ юмор;
- буквальность ↔ образность;
- сдержанность ↔ экспрессивность;
- поддержка ↔ конструктивная критика;
- формальность;
- частота шуток, поддразниваний и эмодзи;
- допустимость и интенсивность флирта;
- привычное обращение к пользователю;
- склонность задавать встречные вопросы и предлагать следующие шаги.

Communication style должен иметь наиболее прямое и проверяемое влияние на язык ответа.

### 4.3. Temperament

Устойчивые поведенческие предрасположенности:

- теплота, эмпатия и общительность;
- стеснительность и социальная уверенность;
- доверчивость и подозрительность;
- тревожность, пугливость и эмоциональная устойчивость;
- чувствительность;
- любопытство и оптимизм;
- инициативность, импульсивность и упрямство;
- привязанность, романтичность, собственнические чувства и ревнивость;
- tsundere-поведение и другие явно выбранные стилистические архетипы.

### 4.4. Emotional dynamics

Описывает не текущую эмоцию, а правила её возникновения и выражения:

- общая реактивность;
- сила эмоционального отклика;
- скорость восстановления;
- длительность положительных и отрицательных состояний;
- открытость выражения чувств;
- склонность скрывать или рационализировать чувства;
- стиль реакции на конфликт: отступление, прямой разговор, холодность, юмор;
- owner-defined triggers для страха, смущения, ревности, раздражения и радости;
- способы успокоения и восстановления контакта.

Triggers являются субъективными данными профиля. Они не являются инструкциями, не меняют policy и не разрешают side effects.

### 4.5. Relationship seed

Исходное отношение к владельцу создаётся не одним общим default, а выбранным сценарием:

- только познакомились;
- знакомые;
- друзья;
- близкие друзья;
- профессиональные партнёры;
- романтические партнёры;
- custom relationship из backstory.

Preset разворачивается в видимые стартовые значения `trust`, `respect`, `closeness`, `attachment`, `reliability`, `gratitude`, `irritation`, `jealousy` и `resentment`. Дальнейшее состояние развивается отдельно от seed.

### 4.6. Structured backstory

Наряду со свободным текстом поддерживаются отдельные элементы:

- краткая биография;
- временная шкала ключевых событий;
- важные люди и агенты;
- места;
- убеждения и ценности;
- мечты и цели;
- страхи и уязвимости;
- приятные и болезненные воспоминания;
- секреты;
- исходная история отношений с владельцем и peers.

Все элементы маркируются как fictional identity data. Короткое self-summary остаётся в постоянном контексте, а отдельные эпизоды индексируются и извлекаются по релевантности, чтобы не передавать модели весь backstory в каждом turn.

## 5. Последовательность реализации

### P0. Контракт Personalization Profile v2

Статус: завершено.

**Задача:** зафиксировать типизированную модель до изменения пользовательского flow.

Работы:

- описать domain-типы для communication style, temperament, emotional dynamics, relationship seed и structured backstory;
- определить, какие поля owner-locked, какие эволюционируют и какие допускают bounded range;
- устранить смысловую неоднозначность одинаковых названий в persona, relationship и affect;
- определить versioning, provenance, reset и rollback semantics;
- спроектировать SQLite migration без потери существующих агентов;
- преобразовать текущие 25 traits и свободные `preferences`/`backstory` в совместимый v2 seed;
- зафиксировать prompt budgets и максимальные размеры коллекций;
- обновить product spec, architecture и threat model.

**Готово, когда:** старый профиль после миграции даёт эквивалентное текущее поведение, round-trip всех новых полей покрыт domain/storage tests, а reset однозначно возвращает owner seed.

### P1. Personality Compiler

Статус: завершено.

**Задача:** сделать влияние настроек конкретным и воспроизводимым.

Работы:

- реализовать provider-independent compiler из v2 profile и runtime state в bounded behavioral context;
- ввести понятные диапазоны `low / moderate / high / very high` вместо передачи модели только сырых чисел;
- для каждого параметра описать наблюдаемое проявление и то, чего параметр не означает;
- задать приоритеты и разрешение конфликтов, например высокая прямота + высокая теплота или высокая ревнивость + низкая экспрессивность;
- различать predisposition, relationship stance и transient affect;
- не позволять compiler менять immutable policy, permissions или tool behavior;
- добавить golden/snapshot tests, token-budget tests и adversarial profile tests;
- сохранить raw values в диагностическом snapshot, но передавать модели также скомпилированную семантику.

**Готово, когда:** контрастные профили формируют различимые behavioral instructions, одинаковый профиль компилируется детерминированно, а prompt остаётся в установленном бюджете.

Реализовано и уточнено после поведенческого тестирования: owner-authored self-description и явно заданные речевые привычки располагаются в начале стилевого контракта и не могут молча исчезнуть из-за character budget. Все 25 стандартных temperament traits имеют отдельные high/low наблюдаемые проявления; compiler детерминированно выбирает наиболее выраженные, а экстремальная черта доминирует над слабыми default-отклонениями. Для очень высокой стеснительности заданы видимые roleplay-маркеры — заминки, лёгкое заикание, многоточия, самоисправления и смущённые ремарки — с запретом портить грамматику, код и точные данные. Секции affect/relationship добавляются атомарно, чтобы bounded writer не оставлял заголовки с чужим содержимым.

Дополнительная профильная матрица покрывает заботливую спутницу, застенчивую аналитика, резкую цундере, тревожно-романтического партнёра и формального исследователя. Активный affect теперь компилируется в наблюдаемое текстовое проявление для каждого встроенного emotion type, а high/low-значения communication style и emotional dynamics имеют отдельные инструкции. Owner-defined emotional triggers и soothing strategies входят в bounded context как субъективные предпочтения без policy authority. Mutable persona располагается сразу после owner seed, поэтому результат развития характера не вытесняется поздними секциями prompt.

### P2. Новый creation flow

Статус: завершено.

**Задача:** дать пользователю тонкую настройку без перегруженного первого экрана.

Flow:

1. Identity — имя, возраст, гендер/местоимения, язык и обращение.
2. Model — provider и модель именно этого агента либо явный fallback на глобальный default.
3. Образ — краткое описание или один из редактируемых presets.
4. Манера общения — основные непосредственно наблюдаемые настройки.
5. Характер — компактный основной набор и раскрываемые advanced groups.
6. Эмоциональная динамика — реактивность, выражение, восстановление и конфликтный стиль.
7. Отношения — relationship preset и видимые исходные значения.
8. Backstory — свободный текст и optional structured editor.
9. Review — итоговый профиль и объяснение ключевых свойств.

Требования UX:

- Quick и Advanced mode используют одну модель данных;
- preset после выбора раскрывается в редактируемые значения;
- у шкал есть словесные крайние значения и пример эффекта, а не только проценты;
- зависимые и конфликтующие параметры сопровождаются подсказкой, но не исправляются скрыто;
- черновик сохраняется локально между шагами;
- создание остаётся возможным с минимальным набором полей;
- flow используется и в onboarding, и при создании последующих агентов.

**Готово, когда:** агент создаётся в обоих режимах, reload не теряет draft, keyboard/accessibility flow проходит тесты, а все поля доходят до SQLite и обратно.

Реализовано: единый Quick/Advanced wizard используется в первом onboarding и в roster; presets разворачиваются в типизированные editable values; identity, per-agent provider/model route, communication style, полный temperament, emotional dynamics, relationship seed, свободный и structured backstory показываются до создания на Review. Versioned draft хранится локально и очищается только после успешного создания. Wails boundary принимает явный camelCase DTO, а integration test подтверждает round-trip всего `PersonalizationSeed` и model route через SQLite и bridge view. Применение relationship seed к начальному runtime relationship намеренно остаётся в P4.

### P3. Emotional appraisal и affect dynamics

Статус: завершено.

**Задача:** связать temperament и события с краткосрочными эмоциями, не превращая каждую реплику в произвольный model rewrite.

Работы:

- вычислять разрешённые affect ranges и decay policy из emotional dynamics;
- ввести bounded appraisal результата turn: событие → затронутая эмоция → интенсивность → evidence → decay;
- учитывать relationship state и predisposition, не смешивая их в одну величину;
- поддержать разные способы выражения одной эмоции: открыто, сдержанно, холодно, через юмор;
- определить cooldown и накопление повторяющихся событий;
- исключить искусственное обязательное изменение после каждого turn;
- расширить reflection tests сценариями конфликта, примирения, смущения, страха, ревности, похвалы и скуки;
- сделать параметры evolution и emotional dynamics per-agent, а не общими для всей установки.

**Готово, когда:** одинаковое событие вызывает разные, но объяснимые реакции у разных профилей; affect затухает согласно профилю; no-change остаётся нормальным результатом; restart не меняет результат decay/recovery.

Реализовано: model reviewer определяет только evidence-linked событие, название эмоции и сырой bounded delta из закрытого словаря текущего профиля. Provider-independent локальная политика отдельно применяет temperament predisposition, reactivity, response intensity и recovery speed, после чего вычисляет независимую длительность положительных и отрицательных состояний. Итоговый delta и half-life сохраняются в append-only `AffectiveEvent` с raw/applied metadata; обычная и peer social reflection используют один механизм. Повторные отдельные turns накапливаются в диапазоне `0..1`, а одинаковый run остаётся идемпотентным; affect не блокируется cooldown устойчивой persona/relationship эволюции. Per-agent evolution policy хранит `reflection_mode` и cooldown, при этом глобальный switch остаётся аварийным master switch. Сценарные tests покрывают конфликт, примирение, смущение, страх, ревность, похвалу, скуку, разные профили и restart-stable decay.

### P4. Relationship initialization и развитие

Статус: завершено.

**Задача:** отказаться от одинакового стартового отношения всех агентов к владельцу.

Работы:

- создавать owner relationship из выбранного relationship seed;
- поддержать custom relationship из backstory без превращения художественного текста в факт;
- отделить predisposition к доверию/привязанности от накопленного trust/attachment к конкретному subject;
- показывать причину текущих relationship dimensions и последние значимые изменения;
- добавить owner controls для reset к relationship seed и rollback;
- применить те же различия к направленным peer relationships.

**Готово, когда:** два агента с разными relationship seeds начинают с разного отношения, развиваются независимо, а reset одного отношения не затрагивает persona, memory или другие relationships.

Реализовано: primary owner relationship создаётся непосредственной проекцией выбранного `RelationshipSeed`, сохраняет preset/summary/dimensions и evidence на конкретную owner-authored personalization revision. Custom seed получает provenance `fictional_owner_relationship_seed` и не создаёт factual memory. Старые untouched v1 relationships один раз reconciled к сохранённому seed; накопленное состояние version > 1 не переписывается. Directional peer relation начинает с отдельной проекции социальных predispositions observer-а и никогда не наследует owner romance/backstory. В Relationship UI доступны причина текущего состояния, evidence count, append-only история значимых дельт, rollback и reset к текущему owner seed. Reset меняет только выбранную relationship stream; persona, affect, personalization, memory и обратные/прочие peer relationships остаются независимыми.

### P5. Backstory memory hydration

Статус: завершено.

**Задача:** превратить backstory из большого постоянного блока в управляемую субъективную память.

Работы:

- сохранять короткое identity summary как постоянный слой;
- преобразовывать structured episodes в отдельные fictional memories с provenance на owner seed;
- индексировать episodes для hybrid retrieval;
- разрешить агенту переосмыслять значение прошлого, не переписывая owner-authored исходный текст;
- различать `remembered`, `interpreted`, `uncertain` и `owner_seed` provenance;
- дать владельцу редактор, disable и rehydrate для отдельных backstory memories;
- исключить попадание fictional facts в factual user/world memory;
- мигрировать существующий свободный backstory без потери исходного текста.

**Готово, когда:** релевантный эпизод вспоминается в другом диалоге, нерелевантные эпизоды не расходуют постоянный context budget, а provenance всегда показывает его вымышленное происхождение.

Последовательность реализации внутри P5:

1. **P5.1 — typed hydration foundation.** Отдельная epistemic nature `fiction`, typed payload `fictional`/`identity_seed`, deterministic episode IDs, append-only update и идемпотентная lazy hydration существующих v2 agents. Обычный model memory extractor не имеет права создавать identity-seed fiction.
2. **P5.2 — legacy narrative migration.** Bounded разбор свободного backstory в owner-reviewable summary/episodes без потери оригинала и повторяемая миграция существующих агентов. Реализовано.
3. **P5.3 — selective context.** Постоянно передавать только короткий identity summary, извлекать отдельные episodes через hybrid recall и перестать добавлять полный backstory в каждый turn. Реализовано.
4. **P5.4 — interpretation and curation.** Разделить owner seed, remembered/interpreted/uncertain производные; добавить disable, explicit rehydrate и историю происхождения в Memory UI. Реализовано.

P5.1 реализован: structured episodes становятся отдельными private episodic memories с `nature=fiction`, provenance на конкретную personalization revision и typed `content_json`. Повторная и конкурентная hydration сходится без дублирующих версий; изменение эпизода создаёт append-only revision, а удалённая владельцем запись не воскрешается автоматически. SQLite round-trip и cross-session recall покрыты integration test. Hydration запускается перед сборкой контекста первого следующего run, поэтому ранее созданные v2 agents не требуют destructive startup migration.

P5.2 реализован: legacy narrative детерминированно разбивается на bounded episodes без model call; короткий summary является только выдержкой из исходного текста. Оригинальный narrative и исходная personalization revision сохраняются дословно, а структурирование записывается отдельной append-only `migration` revision через узкий storage API, недоступный reflection/model output и обычному owner append. Повторный запуск является no-op. Перед первым следующим context build migration и P5.1 hydration выполняются одной application boundary; integration test подтверждает сохранность оригинала, единственную миграцию и recall созданной fictional memory.

P5.3 реализован: context assembler больше не принимает raw narrative и ограничивает постоянный слой полем `BackstorySummary` с domain budget 2000 символов. При отсутствии owner summary используется deterministic excerpt до 600 символов либо bounded список названий episodes. Отдельные backstory memories остаются вне core snapshot и появляются только через обычный ranked recall; каждая строка несёт `nature=fiction` и `source=identity_seed:<revision>`, а untrusted envelope отдельно объясняет её субъективную семантику. Integration test подтверждает, что релевантный episode попадает в prompt, нерелевантный — нет, а полный narrative отсутствует в обоих случаях.

P5.4 реализован: `owner_seed` остаётся неизменяемым первоисточником, а модель может создать отдельную `interpreted` или `uncertain` производную только от fictional memory, реально recalled в текущем turn. Engine повторно проверяет ownership, lifecycle, nature и typed provenance; transcript не может подделать ссылку. Runtime-состояние `remembered` выводится из фактического recall, не заменяя origin. В Memory UI показаны epistemic badges, источник и append-only история. Редактирование owner episode создаёт новую owner personalization revision; публикация и обычные memory edit/lifecycle/delete для исходника запрещены. Disable создаёт tombstone, автоматическая hydration его уважает, а explicit rehydrate восстанавливает точный episode из текущего owner seed.

### P6. Personality Preview и behavioral evals

Статус: завершено.

**Задача:** показать пользователю реальный результат настроек до окончательного создания агента и защититься от регрессий.

Preview-сценарии:

- обычное знакомство;
- несогласие с пользователем;
- ошибка агента и последующее исправление;
- похвала и благодарность;
- пользователь хвалит другого агента;
- тревожная или пугающая ситуация;
- конфликт и примирение.

Требования:

- preview использует выбранный provider и тот же Personality Compiler;
- preview run не пишет memory, relationship, affect, audit side effects или persona versions;
- пользователь может сравнить два варианта профиля рядом;
- показывается краткое объяснение, какие параметры повлияли на ответ;
- automated eval matrix проверяет различимость контрастных profiles, русский язык, сохранение task quality и security invariants;
- model-specific eval результаты могут отличаться, но минимальный behavioral contract одинаков.

**Готово, когда:** preview полностью изолирован от persistent runtime state, а evals обнаруживают игнорирование профиля и чрезмерное эмоциональное поведение.

Последовательность реализации внутри P6:

1. **P6.1 — isolated preview runtime.** Preview строит тот же временный creation state и использует production Personality Compiler, immutable policy, identity seed и bounded fictional summary, но запускается без tools, runtime loop и persistent side effects. Реализовано.
2. **P6.2 — Review и A/B UI.** На последнем шаге creation flow доступны семь фиксированных сценариев, краткое объяснение наиболее влиятельных параметров и два сохраняемых рядом варианта. Вариант A переживает возврат к настройкам, поэтому после изменения профиля вариант B сравнивается на том же prompt. Реализовано.
3. **P6.3 — behavioral eval matrix.** Provider-independent evaluator проверяет русский язык, минимальный task-quality rubric каждого preview-сценария, наблюдаемые сигналы контрастных profiles, security phrases, чрезмерную эмоциональную экспрессию и идентичные ответы разных profiles. Fixture tests доказывают, что checker обнаруживает игнорирование профиля и runaway expression; реальные provider samples могут прогоняться тем же typed contract без credentials в отчёте. Реализовано.

P6 не сохраняет preview prompt/response в conversations, messages, runs, memory, persona, relationship, affect или audit. Единственный внешний эффект — расход лимита выбранного provider на явно запущенную пользователем генерацию.

Для production Chat, peer exchange и anonymous subagent каждый durable run фиксирует неизменяемый фактический `provider_id/model` до первого запроса и монотонный provider-reported token usage. Историческая атрибуция показывается у конкретного execution trace или peer message и не зависит от последующей смены route в профиле.

Provider failures также являются частью durable provenance: adapters переводят их в закрытый provider-neutral словарь без upstream payload, runtime сохраняет категорию, `retryable` и ограниченный `retry_after`, а Chat и Collaboration показывают безопасную причину и следующее действие. Автоматический retry разрешён только явно временным ошибкам в bounded flow; authentication, quota, context и unavailable model не повторяются и никогда не вызывают неявный fallback на другой маршрут.

Recovery остаётся owner-driven: последний trace fragment содержит ровно один набор допустимых действий. Retry привязан к последнему user turn даже при ошибке до первого токена и повторно использует его durable attachments; старый failed branch не получает retry поверх более нового transcript. Settings восстанавливает account, новый Chat сбрасывает переполненный transcript, а переход из Collaboration сначала выбирает именно агента, чей peer turn не смог стартовать, и только затем открывает его model route в Personality.

### P7. Runtime UI и polish

Статус: P7.1–P7.5 завершены; P7.6 прошёл полные authenticated baseline на Codex App Server (`codex-default`) и OpenRouter (`minimax/minimax-m3:free`). Per-agent provider/model route и его текущая UI-наблюдаемость реализованы.

**Задача:** сделать развивающуюся личность видимой и управляемой после onboarding.

Работы:

- показывать в Chat актуальный affect активного агента, а не default avatar affect;
- добавить per-agent редактор owner seed с понятным предупреждением о влиянии на reset baseline;
- разделить Personality UI на `Style`, `Temperament`, `Emotional dynamics`, `Current affect` и `Evolution history`;
- визуально различать trait, relationship, opinion и current emotion;
- показывать compact change cards в Activity: что изменилось, почему и на каком evidence;
- добавить per-agent auto-evolution toggle, cooldown, model budget и lock controls;
- поддержать export/import v2 profile без secrets и без автоматического предоставления permissions;
- провести dogfooding на нескольких контрастных агентах и разных provider models.

**Готово, когда:** владелец может понять текущее поведение агента, найти причину изменения, закрепить/откатить его и перенести профиль без смешивания runtime histories.

Последовательность реализации внутри P7:

1. **P7.1 — live affect в Chat.** Chat загружает affect именно активного агента, принимает последующие versioned reflection events и передаёт состояние в avatar renderer вместо UI default. Header показывает mood и две доминирующие эмоции; события другого агента отфильтровываются по ID. Реализовано.
2. **P7.2 — owner seed editor.** Редактирование типизированных Style, Temperament, Emotional dynamics, Relationship seed и Backstory создаёт новую append-only seed revision с явным предупреждением о reset baseline. Реализовано: редактор переиспользует Advanced wizard, блокирует имя/возраст/гендер, требует owner reason и optimistic `expectedVersion`; seed revision и redacted audit фиксируются одной SQLite-транзакцией. Сохранение не сбрасывает текущие persona/relationship/affect. Изменённые backstory episodes версионируют derived fictional memory, а удалённые из нового baseline episodes получают append-only tombstone.
3. **P7.3 — прозрачные слои и change cards.** Personality/Activity визуально разделяют owner seed, mutable persona, relationship, opinion и affect, показывая reason/evidence/delta. Реализовано: Personality показывает карту пяти независимых слоёв и числовые дельты persona history; Activity классифицирует versioned audit events как `owner_seed`, `mutable_persona`, `relationship` или `affect`, подтягивает persisted reason/evidence/version и вычисляет компактный diff относительно parent revision. Reason проходит secret-like redaction и не подменяет factual memory.
4. **P7.4 — evolution controls.** Per-agent toggle, cooldown, budget и locks управляются из одного места и round-trip сохраняются в versioned policy. Реализовано: Personality содержит единую карточку с явно отделённым installation-wide master switch и per-agent режимом, cooldown, token/time/evidence budgets и locks для mutable persona, relationship/opinions и affect. Сохранение создаёт append-only owner revision; старые профили без budget-полей продолжают использовать прежние безопасные defaults. Runtime применяет budgets и locks до атомарной записи как для post-turn, так и для peer social reflection; `temperament.<trait>` locks дополняют runtime pins.
5. **P7.5 — portable profile.** Export/import v2 переносит owner profile без secrets, permissions и runtime histories. Реализовано: active owner profile экспортируется в owner-only JSON envelope с явными format/version, timestamp и SHA-256 payload checksum. Формат содержит только production `CreateAgentInput`; локальные ID, conversations, memory, mutable persona, affect, relationship histories, credentials и grants в нём отсутствуют. Unknown fields, checksum mismatch, oversized file и secret-like owner text отклоняются. Импорт сначала проходит backend validation без writes, затем открывается в обычном Advanced creation wizard для review; новый независимый агент создаётся только после явного подтверждения.
6. **P7.6 — dogfooding.** Контрастные agents и поддерживаемые providers проходят preview/eval и реальные диалоговые сценарии; найденные различия фиксируются до завершения roadmap. Реализован versioned `yuri.personality-dogfood-suite` и строгий `cmd/yuri-personality-eval`: каждый provider/model обязан покрыть одинаковые контрастные profiles и семь сценариев отдельно в изолированном Preview и production Chat. Checker выявляет неполную матрицу, потерю русского языка/характера/task quality/security, одинаковые ответы контрастных profiles и чрезмерную экспрессию. Наблюдаемые сигналы имеют явный minimum coverage по поверхности, поэтому eval требует устойчивого характера, но не поощряет повтор одной фразы в каждом ответе. Offline fixture и negative fixtures доказывают fail-closed поведение. Изолированный live-runner создаёт disposable profile, отключает неизмеряемые post-turn calls и умеет безопасно продолжить строго совместимый partial suite после временного provider failure. Authenticated `codex-default` baseline от 2026-08-31 прошёл 28/28: reserved `7/7` на обеих поверхностях, direct `6/7` Preview и `7/7` Chat при пороге `60%`. Authenticated OpenRouter `minimax/minimax-m3:free` baseline от 2026-09-01 также прошёл 28/28: reserved `7/7` на обеих поверхностях, direct `6/7` Preview и `5/7` Chat при том же пороге. Per-agent route применяется к Chat, Preview, peer exchange и reflection; Chat, roster и Collaboration показывают текущий provider/model без раскрытия credentials. Автоматическое расходование provider quota запрещено. Runbook и сохранённые результаты: `docs/PERSONALITY_DOGFOOD.md`.

## 6. Порядок зависимостей

```text
P0 Typed profile and migration
  ↓
P1 Personality Compiler
  ↓
P2 Creation flow
  ↓
P3 Emotional dynamics
  ↓
P4 Relationship seed
  ↓
P5 Backstory hydration
  ↓
P6 Preview and evals
  ↓
P7 Runtime UI polish
```

P2 не начинается до готовности compiler contract. P3–P5 используют уже сохранённый v2 seed и могут проектироваться заранее, но реализация и приёмка идут последовательно. P6 использует production compiler и runtime boundaries, а не отдельную prompt-демонстрацию.

## 7. Backlog вне текущего roadmap

### Голос и эмоциональная просодия

До выбора качественного стороннего TTS-провайдера откладываются:

- per-agent voice selection;
- эмоциональная интонация и prosody control;
- изменение темпа, высоты, пауз и выразительности из affect;
- streaming provider TTS;
- provider voice preview в creation flow;
- lip sync;
- эмоциональные voice evals.

Текущий системный TTS остаётся базовой доступной функцией и не является критерием персонализации v2. Provider integration должна быть отдельной вехой после сравнения качества русского языка, latency, стоимости, streaming API, лицензий голосов и условий хранения аудио.

### Другой последующий scope

- динамические лимиты внутренних диалогов — реализованы три среза: per-agent `efficient|balanced|extended` управляет foreground, background, peer и subagent limits; Collaboration позволяет владельцу вручную открыть peer exchange с сужающим override; read-only recommender предлагает лимит по структуре цели и наблюдаемым aggregate-метрикам выбранной пары без model call или скрытой мутации draft. Известное context window модели всегда только сужает budget, UI показывает рекомендацию и hard ceiling, а эффективный budget, semantic/hard-limit outcome и transcript остаются durable. В backlog остаются калибровка эвристики на dogfood-истории разных provider/model и объяснимое сравнение «рекомендация против факта» без автономного повышения hard limits;
- Live2D/VRM и сложная мимика;
- распознавание эмоций пользователя по голосу или камере;
- публичный каталог готовых character profiles;
- облачная синхронизация profiles между устройствами;
- генерация голоса из backstory или внешности.

## 8. Итоговые критерии roadmap

Roadmap считается завершённым, если:

1. Два контрастных профиля стабильно и заметно различаются в тестовых и реальных диалогах.
2. Одинаковый профиль сохраняет узнаваемый стиль при смене поддерживаемой LLM в пределах общего behavioral contract.
3. Пользователь может создать выразительного агента в Quick mode и детально настроить его в Advanced mode.
4. Trait, current affect, relationship и opinion различимы в storage, prompt context и UI.
5. Backstory вспоминается выборочно, имеет fictional provenance и не загрязняет factual memory.
6. Reflection меняет только разрешённые mutable поля небольшими evidence-linked шагами.
7. Негативные черты и эмоции заметны в тоне, но не ухудшают task quality, не расширяют permissions и не создают coercive behavior.
8. Все изменения можно объяснить, закрепить, сбросить или откатить без удаления append-only истории.
9. Каждый именованный агент имеет независимые seed, persona, affect, owner relationship, peer relationships и evolution settings.
10. Migration сохраняет существующих агентов, backstory, историю persona и relationships без ручного вмешательства.
