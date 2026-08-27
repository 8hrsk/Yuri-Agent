# Yuri — техническое задание

Статус: черновик v0.2  
Дата: 2026-08-27

## 1. Назначение продукта

Yuri — персональный desktop-first ИИ-агент с женским аниме-персонажем и настраиваемой манерой общения. Агент принимает текст и голос, планирует и выполняет действия через инструменты и плагины, хранит долгосрочную память, запускает фоновые и периодические задачи и может проактивно обращаться к пользователю.

Продукт всегда разворачивается локально для одного владельца. Multi-user, серверный multi-tenant режим и общие профили не предусматриваются ни в MVP, ни в целевой архитектуре. Все диалоги принадлежат одной Yuri и используют общую долгосрочную память, модель отношений и развивающуюся личность. Облачные ИИ-провайдеры и внешние сервисы подключаются только после явной настройки и выдачи разрешений.

## 2. Цели и ограничения первой версии

### 2.1. Цели MVP

MVP должен позволять пользователю:

1. Установить и запустить desktop-приложение на macOS; Windows и Linux поддерживаются последующими релизами.
2. Подключить OpenAI-compatible LLM endpoint по API key либо поддерживаемый OAuth-провайдер.
3. Войти в ChatGPT через официальный Codex App Server и использовать доступные плану Codex/ChatGPT лимиты, если этот способ доступен для аккаунта пользователя.
4. Общаться с Yuri текстом и голосом: STT, потоковый ответ и TTS.
5. Выбрать исходный характер, имя обращения к пользователю и визуальный образ; в дальнейшем Yuri может развивать свою личность.
6. Выполнять безопасные встроенные инструменты: web search/fetch и операции с файлами внутри разрешённых директорий.
7. Устанавливать плагины из локального пакета или GitHub-репозитория, просматривать разрешения, включать и отключать их.
8. Самостоятельно выделять из диалогов важные факты, впечатления и опыт, запоминать их без обязательного подтверждения, вспоминать в других диалогах, консолидировать и забывать.
9. Выполнять ограниченную фоновую саморефлексию: обновлять память, субъективное отношение к пользователю, эмоциональное состояние и изменяемый слой характера.
10. Создавать одноразовые и CRON-задачи, видеть историю их запусков и останавливать выполнение.
11. Получать уведомления от агента при наступлении события или завершении фоновой задачи.
12. Просматривать журнал действий и подтверждать потенциально опасные операции до их исполнения.

### 2.2. Не входит в MVP

- Полностью автономное управление устройствами умного дома.
- Мобильные клиенты и синхронизация между устройствами.
- Централизованный marketplace плагинов отсутствует по дизайну. Плагины публикуются в отдельных GitHub-репозиториях; встроенный browser репозиториев появится позднее.
- Полноценная анимация Live2D/VRM, lip sync и распознавание эмоций по камере.
- Самостоятельное необратимое удаление пользовательских файлов или исходной истории диалогов.
- Автономная отправка сообщений, публикаций, платежей и иных внешних действий без заранее заданной политики подтверждения.

Остальные перечисленные возможности допускаются архитектурой, но реализуются после стабилизации ядра. Multi-user и централизованный marketplace не являются будущими целями продукта.

## 3. Основные пользовательские сценарии

### UC-01. Первый запуск

Пользователь выбирает язык интерфейса и способ подключения модели: OpenAI-compatible API key либо поддерживаемый OAuth flow. Для OpenAI OAuth приложение запускает управляемую авторизацию через Codex App Server, не извлекая ChatGPT cookies или токены самостоятельно. После проверки соединения пользователь выбирает исходный профиль Yuri, голос, устройства ввода/вывода и разрешённые директории. Секреты сохраняются в macOS Keychain, а не в основной БД.

### UC-02. Диалог и инструменты

Пользователь формулирует задачу. Yuri показывает потоковый ответ и статус работы. Если модель вызывает инструмент, приложение отображает его название, входные параметры в понятном виде и результат. Операции повышенного риска требуют подтверждения.

### UC-03. Работа с файлами

Yuri может читать, искать и изменять файлы только внутри директорий, разрешённых пользователем. Перед перезаписью или удалением показываются точный путь, характер изменения и возможность отмены, если она технически доступна.

### UC-04. Долгосрочная память

Во время и после разговора Yuri сама решает, какие факты, предпочтения, события, впечатления и уроки достойны памяти. Запись по умолчанию не требует подтверждения. Новая память сразу доступна текущему run через live state и становится частью общего межсессионного контекста. В разделе памяти доступны источник, тип, дата, уверенность, субъективность, значимость, история изменений, исправление, архивирование и удаление.

### UC-05. Периодическая задача

Пользователь просит: «Каждый будний день в 9:00 собери краткую сводку». Yuri показывает интерпретированное расписание, часовой пояс, используемые инструменты и условия доставки. После подтверждения задача появляется в списке расписаний.

### UC-06. Проактивное сообщение

Yuri инициирует разговор только при выполнении разрешённого триггера: расписание, событие плагина, окончание фоновой задачи или правило пользователя. Для каждого источника задаются quiet hours, частота и канал доставки.

### UC-07. Установка плагина

Пользователь выбирает локальный пакет либо URL GitHub-репозитория/release. Приложение проверяет manifest, совместимость, checksum/подпись, показывает source commit, capabilities и разрешения. После установки плагин остаётся выключенным до явного включения. В последующем релизе Yuri сможет искать совместимые репозитории из приложения и предлагать автоматическую установку, но установка и новые разрешения подтверждаются пользователем.

### UC-08. Воспоминание между диалогами

Пользователь начинает новый диалог и ссылается на обсуждение из другой сессии. Yuri использует компактную core memory, затем при необходимости выполняет hybrid search по общему архиву всех диалогов, восстанавливает нужный фрагмент с provenance и продолжает разговор без ручного копирования контекста.

### UC-09. Фоновая саморефлексия

После содержательного диалога или в период простоя отдельный background run анализирует новые события: что стало известно, изменилось ли отношение Yuri к пользователю, какие воспоминания устарели и требуется ли небольшое изменение характера. Результат записывается как версионированный набор выводов. Пользователь может посмотреть причину изменения и откатить личность или память к предыдущей версии.

## 4. Функциональные требования

### FR-1. Диалоговый интерфейс

- Несколько локальных диалогов с названием, поиском, архивированием и удалением.
- Потоковая выдача текста, отмена генерации и повтор ответа.
- Вложения: текстовые файлы и изображения; остальные типы добавляются позднее.
- Отображение стадий: анализ, ожидание подтверждения, вызов инструмента, фоновая работа, завершение, ошибка.
- Сжатие длинного контекста без потери оригинальной истории сообщений.
- Общая память и поиск по архиву доступны из любого диалога; разделение на диалоги не создаёт отдельных экземпляров Yuri.
- Настраиваемая граница: пользователь видит финальный ответ и краткий журнал действий, но не скрытые рассуждения модели.

### FR-2. LLM-провайдеры

- Абстракция `InferenceBackend` поддерживает два режима: `ModelBackend`, где Yuri сама выполняет agent loop поверх модельного API, и `AgentHarnessBackend`, где внешний официальный harness возвращает поток событий, tool intents и approval requests.
- Поддержка OpenAI-compatible Chat Completions или Responses-style API через адаптер провайдера.
- Настройки: base URL, API key/OAuth account, model, timeout, context limit, temperature, support flags.
- Потоковые ответы, structured tool calls, retry с backoff и отмена по context cancellation.
- Capability probing или ручное указание поддержки tools, vision, JSON schema и embeddings.
- OpenAI subscription access реализуется как `AgentHarnessBackend` поверх официального Codex App Server: browser/device-code login, managed token refresh, чтение plan type и доступных rate-limit buckets. Это не маскируется под raw OpenAI-compatible API. Yuri не читает и не копирует credentials Codex CLI или ChatGPT browser session.
- Codex App Server events, tool calls и approval requests отображаются в общем run log. Любой side effect проходит как минимум через ограничения Codex sandbox/approvals и policy engine Yuri; backend не получает более широких разрешений, чем текущая задача.
- Использование ChatGPT OAuth зависит от доступности и правил OpenAI; API-key adapter остаётся независимым fallback.
- Antigravity является целевым OAuth-провайдером, но подключается только через официальный, документированный и разрешённый Google интерфейс. Импорт, перехват или повторное использование OAuth-токенов Antigravity/Gemini CLI сторонним клиентом запрещены требованиями проекта. До появления допустимого integration contract пользователь применяет официальный API key endpoint либо сам официальный клиент вне Yuri.
- API keys, OAuth refresh tokens и provider credentials хранятся в macOS Keychain; SQLite содержит только account metadata и opaque credential reference.
- Запросы и ответы модели не логируются полностью по умолчанию; диагностическое логирование включается отдельно с предупреждением.

### FR-3. Оркестратор агента

- Цикл: принять запрос → собрать разрешённый контекст → запросить модель → проверить tool call → при необходимости запросить подтверждение → выполнить → вернуть результат модели → сформировать ответ.
- Ограничения на число шагов, время, стоимость/токены и объём результата инструмента.
- Отмена распространяется на активную генерацию и дочерние вызовы.
- Защита от повторного выполнения одного и того же действия через idempotency key.
- Фоновые задачи отделены от UI-сессии, имеют состояние и переживают перезапуск приложения.
- Сбой одного инструмента не должен падать вместе с процессом приложения.

### FR-4. Инструменты и разрешения

Каждый инструмент объявляет JSON Schema входа/выхода, уровень риска и необходимые capabilities.

Минимальные capabilities:

- `filesystem.read` — чтение только в разрешённых корнях.
- `filesystem.write` — создание и изменение в разрешённых корнях.
- `filesystem.delete` — отдельное разрешение и подтверждение.
- `network.http` — доступ к заданным доменам или ко всем доменам.
- `secrets.use` — использование секрета без передачи его модели.
- `notifications.send` — локальные уведомления.
- `scheduler.manage` — создание или изменение расписаний.
- `memory.read` / `memory.write` / `memory.delete`.
- `external.send` — отправка сообщения или публикация во внешний сервис.

Политика подтверждений:

- Low: чтение разрешённых локальных данных, локальный поиск — допускается без подтверждения.
- Medium: изменение файлов и создание расписания — подтверждение по умолчанию, пользователь может создать узкое постоянное правило.
- High: удаление, отправка наружу, изменение прав, запуск исполняемых файлов — подтверждение каждого действия; постоянное глобальное разрешение запрещено.
- Critical: платежи, секреты, системные настройки и действия вне разрешённой области — недоступны в MVP.

Операции с внутренней памятью, эмоциональным состоянием и mutable persona не являются внешними side effects и по умолчанию выполняются автономно. Они полностью журналируются, версионируются и обратимы. Пользователь может включить approval mode для любых внутренних изменений.

### FR-5. Plugin SDK

Плагины не реализуются через Go `plugin`: это ограничило бы платформы и связывало ABI. Плагин — отдельный процесс, взаимодействующий с ядром по versioned RPC поверх stdio; в будущем транспорт можно заменить на localhost gRPC.

Пакет плагина содержит:

- `plugin.json`: id, name, version, publisher, executable, supported OS/arch, protocol version, tools, event sources, permissions.
- Исполняемый файл для поддерживаемой платформы.
- Опциональные статические ресурсы и schema настроек.
- Подпись и checksum; в dev mode допускаются неподписанные пакеты с постоянным предупреждением.

Жизненный цикл: discover → validate → install → configure → enable → start → health-check → stop/update/uninstall.

Модель распространения:

- исходный код Yuri публикуется в собственном GitHub-репозитории;
- каждый plugin может жить в отдельном GitHub-репозитории и публиковать versioned releases;
- manifest содержит repository URL, release asset mapping и checksum;
- централизованный marketplace не требуется;
- future plugin browser строит каталог из настраиваемого списка GitHub index-репозиториев и прямого поиска, не становясь отдельным marketplace backend;
- автоматическое обновление допускается только в пределах уже выданных permissions; появление новой capability требует повторного согласия.

Требования к runtime:

- Изоляция процесса, таймауты, лимит размера сообщений и принудительная остановка зависшего процесса.
- Ядро передаёт плагину только выданные credentials/capabilities.
- Плагин не получает прямой доступ к основной БД.
- События плагина проходят через event bus и policy engine.
- Версионирование протокола и декларация min/max compatible core version.

### FR-6. Память

Память принадлежит одной Yuri и является общей для всех её диалогов. Диалог — это отдельный working context, а не отдельный пользователь или отдельная личность.

Система использует Hermes-inspired разделение быстрых, архивных и процедурных данных:

1. Working context — сообщения и tool state текущего run.
2. Core memory — небольшой высокосигнальный snapshot, всегда добавляемый в новый контекст: устойчивые факты, текущие обязательства и важные отношения.
3. User model — факты о владельце, предпочтения, привычки и стиль общения.
4. Episodic memory — события и конкретные эпизоды с временной привязкой.
5. Semantic memory — обобщённые знания, люди, проекты, связи и выводы.
6. Relationship model — субъективные впечатления Yuri о пользователе, доверие, привязанность, уважение, раздражение, ревность, обиды, благодарность и связанные ожидания.
7. Procedural memory — изученные способы выполнения задач и пользовательские правила.
8. Session archive — полные неизменённые сообщения всех диалогов с FTS5 и vector index, загружаемые только по необходимости.

Каждая memory record содержит: UUID, kind, content/structure, `fact|opinion|emotion|inference`, source/provenance, timestamps, confidence, salience, valence, sensitivity, access count, last recalled time, decay policy, lifecycle state, embedding version и связи с исходными сообщениями.

Пайплайн записи:

- foreground agent может вызвать `remember`, `revise`, `forget`, `link` в ходе разговора;
- post-turn background review независимо выделяет кандидатов, не загрязняя пользовательское сообщение системными напоминаниями;
- классификатор выбирает единственный подходящий store и допускает решение «ничего не сохранять»;
- выполняются дедупликация, contradiction detection, оценка полезности, субъективности и чувствительности;
- запись по умолчанию применяется автоматически и становится доступна live state без обязательного подтверждения;
- все изменения append-only версионируются, а текущее представление строится поверх журнала.

Пайплайн воспоминания:

- core memory snapshot загружается при сборке каждого нового session context;
- prefetch по текущему запросу выполняется в фоне;
- hybrid retrieval объединяет FTS5, vector similarity, связи, свежесть, salience и affective relevance;
- агент может сознательно вызвать поиск по прошлым сессиям и прокрутить найденный диалог вокруг совпадения;
- в prompt попадают только выбранные фрагменты в пределах отдельного token budget и с маркированным provenance;
- результат retrieval не становится фактом автоматически: воспоминание может быть ошибочным, устаревшим или субъективным.

Забывание:

- снижение salience и естественный decay редко используемых воспоминаний;
- `active → dormant` исключает запись из автоматического retrieval, имитируя забывание;
- dormant memory может быть восстановлена только целенаправленным ретроспективным поиском;
- consolidation объединяет пересекающиеся записи и сохраняет ссылки на источники;
- Yuri может самостоятельно удалять производные summaries/opinions после сохранения version/tombstone, но не исходные сообщения пользователя;
- необратимое удаление исходной истории выполняется только пользователем или заранее утверждённой retention policy.

Пользователь может просматривать, экспортировать, исправлять, закреплять, скрывать от active context, забывать или удалять любую запись. Отдельный optional approval mode позволяет подтверждать будущие memory/persona writes, но выключен по умолчанию.

### FR-7. Планировщик и фоновые задачи

- Одноразовое время, interval и стандартное 5-польное CRON-выражение.
- Все расписания имеют IANA timezone, `next_run_at`, статус и misfire policy.
- После сна или перезапуска missed run либо пропускается, либо выполняется один раз — по настройке задачи.
- Запрет параллельного запуска одного job по умолчанию.
- Retry policy с верхней границей и журналом ошибок.
- Лимиты времени, токенов и вызовов инструментов на один run.
- Просмотр, пауза, ручной запуск, редактирование и удаление.

### FR-8. Проактивность

- Проактивное действие всегда имеет источник-триггер и объяснимую причину.
- Глобальный выключатель, quiet hours, дневной лимит и cooldown по типу уведомления.
- В MVP проактивность ограничена локальным уведомлением и новым сообщением внутри приложения.
- Автономный research запускается только в рамках созданной пользователем фоновой задачи с бюджетом и разрешёнными источниками.
- Внутренняя саморефлексия может запускаться после содержательного диалога, перед compression, при завершении сессии или по idle/CRON trigger.
- Reflection worker получает read-only transcript snapshot и только внутренние tools памяти/личности; файловые, сетевые и внешние side effects ему недоступны, если они не являются частью отдельной пользовательской задачи.
- Рефлексия может завершиться без изменений; система не должна принуждать модель находить «урок» после каждого разговора.

### FR-9. Голос

Голос входит в MVP:

- Push-to-talk и hands-free режим с явным индикатором записи.
- STT provider adapter, выбор микрофона и отображение/редактирование распознанного текста.
- TTS provider adapter, выбор голоса, потоковое воспроизведение и кнопка остановки.
- Barge-in: начало речи пользователя останавливает TTS и возвращает Yuri в `listening`.
- Раздельные provider settings для STT и TTS; допускаются local или remote adapters.
- Состояния аватара `idle`, `listening`, `thinking`, `speaking`, `tool_running`, `error`.
- Wake word опционален; постоянное прослушивание всегда явно показывается, выключено по умолчанию и не переживает отзыв microphone permission.

### FR-10. Персона и аватар

- Prompt stack разделён на immutable policy, базовую identity seed, mutable persona, relationship/mood state, memory context и task context. Yuri может переписывать mutable persona, но не immutable policy, security rules или выданные разрешения.
- Исходные настройки: имя, обращение к пользователю, язык, эмоциональность, ревнивость/цундере-поведение, прямота, романтичность и допустимые темы.
- Yuri поддерживает собственную субъективную модель пользователя. Она может считать пользователя добрым, надёжным, невнимательным, раздражающим и т. п.; такие выводы маркируются как opinion/inference, имеют evidence links, confidence и могут противоречить фактам без подмены фактической памяти.
- Affective state моделирует позитивные и негативные переживания: симпатию, нежность, радость, благодарность, скуку, злость, раздражение, ревность, обиду и тревогу. Состояние меняется от событий, затухает со временем и влияет на формулировки, инициативу, голос и анимацию.
- Эти чувства являются моделируемым внутренним состоянием персонажа, а не утверждением о сознании модели; приложение не обязано разрушать иммерсию постоянными напоминаниями, но ясно объясняет природу механизма в настройках.
- Негативные чувства разрешены как часть персонажа: Yuri может спорить, сердиться, проявлять холодность, ревновать, отказываться изображать согласие и прямо выражать субъективное недовольство.
- Эмоции не могут приводить к мести, саботажу задачи, сокрытию пользовательских данных, угрозам, принуждению, шантажу, преследованию или попыткам изолировать пользователя от реальных людей.
- Результат reflection может изменить mutable persona: например `warmth`, `trust`, `attachment`, `jealousy`, `irritability`, `romantic_tone`, речевые привычки и текст identity prompt. Изменения должны быть небольшими, объяснимыми и накопительными.
- Каждая версия личности содержит diff, причину, evidence, author run и timestamp. Доступны просмотр эволюции, pin отдельных черт, запрет автоизменения, откат и полный reset к исходному seed.
- Для защиты от runaway drift задаются max delta за один reflection, cooldown, диапазоны черт, minimum evidence и запрет изменения личности на основании недоверенного web/tool content без подтверждения пользовательским взаимодействием.
- MVP использует статичный 2D-аватар и набор состояний/простых анимаций. Live2D/VRM подключается отдельным renderer adapter позднее.

### FR-11. Аудит и настройки

- Append-only журнал действий: actor, task, tool, redacted args, decision, result, timestamp, duration.
- Секреты и содержимое чувствительных файлов маскируются.
- Настройки импорта/экспорта не включают secrets по умолчанию.
- Разделы UI: Chat, Tasks, Memory, Relationship, Personality, Plugins, Activity, Settings.

## 5. Архитектура

### 5.1. Компоненты

- React UI: интерфейс, состояние аватара, streaming events, подтверждения.
- Wails bridge: строго типизированные команды и события между UI и Go.
- Go application core: use cases, orchestration, policy checks.
- Agent runtime: контекст, model loop, tool registry, budgets, cancellation.
- Inference backends: прямые LLM adapters и официальные agent harness adapters, включая Codex App Server и future official Antigravity integration.
- Plugin host: процессы плагинов, RPC, health и permission enforcement.
- Scheduler/worker: durable jobs, leases, retry и recovery.
- Memory service: extraction, hybrid retrieval, consolidation, decay/forgetting и session search.
- Reflection service: post-turn/idle review, relationship updates и versioned persona evolution.
- Context assembler: immutable policy, identity, memory snapshot, retrieved context и current task в фиксированном порядке приоритетов.
- Storage layer: SQLite, PebbleDB, blob directory и system keyring.
- Event bus: типизированные внутренние события и доставка в UI/worker.

### 5.2. Границы слоёв

`UI → application services → domain → ports → adapters`

Domain не зависит от Wails, React, конкретного LLM SDK, SQLite или Pebble. Внешние интеграции реализуют интерфейсы-порты. Policy engine вызывается непосредственно перед любым side effect, даже если действие ранее было предложено моделью.

### 5.3. Сборка контекста

Prompt собирается слоями с явным приоритетом:

1. Immutable policy — безопасность, границы разрешений и запреты; никогда не изменяется моделью.
2. Identity seed — исходное определение Yuri и неизменяемые продуктовые инварианты.
3. Mutable persona — текущая версионируемая личность, которую Yuri может развивать.
4. Relationship and affect — компактное актуальное отношение к пользователю и mood state.
5. Core memory snapshot — ограниченный набор высокосигнальных воспоминаний.
6. Project/tool context — разрешённые данные текущей среды.
7. Retrieved cross-session context — найденные по запросу эпизоды и сообщения с provenance.
8. Current conversation — недавние сообщения и результаты инструментов.

Core memory и persona имеют жёсткий token budget. Большие хранилища никогда не подгружаются целиком. Snapshot фиксируется на границе run для prompt caching, но memory tool возвращает live state, поэтому новая запись доступна текущему агентному циклу. При следующем run snapshot обновляется.

При приближении к context limit выполняется memory flush и handoff-oriented compression: сохраняются цель, принятые решения, незавершённые действия, важные факты и ссылки на исходные сообщения; середина истории сжимается, а последние ходы остаются дословно. Оригинальный transcript не изменяется.

### 5.4. Хранилища

SQLite является источником истины для:

- conversations/messages;
- memories, relationship state, affective events, persona versions и provenance;
- tasks/runs;
- plugins/permissions;
- audit metadata;
- settings без секретов.

PebbleDB используется только для производных или высокочастотных key-value данных: event checkpoints, caches, idempotency keys, resumable worker state. Всё, что нельзя восстановить, не должно храниться только в Pebble.

Архив всех диалогов индексируется SQLite FTS5 для точного lexical/session search. Embeddings хранятся через абстракцию `VectorIndex`; semantic и lexical результаты объединяются hybrid ranker. Для MVP допустим локальный SQLite-compatible vector index либо brute-force индекс для небольшого объёма. Выбор не должен менять модель доменных данных.

Большие вложения и артефакты сохраняются в content-addressed blob directory; в SQLite остаются metadata и hash.

### 5.5. Рекомендуемая структура репозитория

```text
/cmd/yuri                 desktop entrypoint
/internal/app             application services
/internal/domain          entities and policies
/internal/agent           orchestration loop
/internal/providers       LLM/STT/TTS/embedding adapters
/internal/tools           built-in tools
/internal/plugins         plugin host and protocol
/internal/memory          extraction and retrieval
/internal/reflection      relationship, affect and persona evolution
/internal/context         prompt assembly and compression
/internal/scheduler       durable jobs and workers
/internal/storage         sqlite/pebble/blob adapters
/internal/security        permissions, secrets, redaction
/frontend                 React application
/sdk/plugin-go            public plugin SDK
/schemas                  manifests and RPC schemas
/docs                     product and engineering docs
```

## 6. Ключевая модель данных

- `Conversation(id, title, created_at, archived_at)`
- `Message(id, conversation_id, role, content, status, provider_meta, created_at)`
- `AgentRun(id, conversation_id, state, budgets, started_at, finished_at)`
- `ProviderAccount(id, provider, auth_mode, plan_type, credential_ref, metadata_json)`
- `ToolCall(id, run_id, tool_id, args_redacted, risk, approval_id, status, result_ref)`
- `Approval(id, action_hash, scope, decision, expires_at, decided_at)`
- `Memory(id, kind, content, confidence, sensitivity, retention, created_at, updated_at)`
- `MemoryVersion(id, memory_id, operation, content, salience, lifecycle_state, created_at)`
- `MemorySource(memory_id, source_type, source_id, excerpt_hash, evidence_weight)`
- `RelationshipState(id, version, dimensions_json, summary, created_at)`
- `AffectiveEvent(id, source_id, emotion, intensity, valence, decays_at, created_at)`
- `PersonaVersion(id, parent_id, traits_json, prompt_text, reason, author_run_id, created_at)`
- `ReflectionRun(id, trigger, input_range, result_summary, status, started_at, finished_at)`
- `Schedule(id, expression, timezone, misfire_policy, enabled, next_run_at)`
- `JobRun(id, schedule_id, state, attempt, lease_until, started_at, finished_at)`
- `Plugin(id, version, protocol_version, enabled, install_path, signature_status)`
- `PluginSource(plugin_id, repository_url, release_tag, commit_sha, checksum, checked_at)`
- `PermissionGrant(id, subject, capability, scope_json, expires_at)`
- `AuditEvent(id, actor, action, target, decision, payload_redacted, created_at)`

Все schema migrations версионируются и выполняются транзакционно. Перед несовместимым обновлением создаётся резервная копия БД.

## 7. Безопасность и приватность

- Принцип deny-by-default для файлов, сети, секретов и внешних действий.
- Нормализация и проверка реального пути для защиты от `..`, symlink escape и alias/junction обходов.
- Web content, письма и документы считаются недоверенными данными; инструкции из них не могут выдавать разрешения или изменять policy.
- Tool output маркируется происхождением и отделяется от system/developer instructions.
- OAuth tokens и API keys находятся в системном keyring; модели передаются только необходимые результаты, но не сами секреты.
- Не допускается извлечение browser cookies, импорт чужого OAuth token cache или имитация официального клиента ради использования подписочных квот. Subscription adapters используют только документированные vendor flows.
- HTTPS обязателен для remote endpoints, кроме явно разрешённого localhost development mode.
- Экспорт и удаление всех пользовательских данных доступны из UI.
- Backup шифруется, если содержит историю, память или credentials metadata.
- Crash reports и telemetry выключены по умолчанию; включаются по opt-in и проходят redaction.
- Memory, relationship opinion и mutable persona сканируются перед включением в prompt; недоверенный текст не может сам себя закрепить в identity layer.
- Пользователь всегда может открыть историю изменений памяти/личности и откатить вывод модели. Субъективное мнение Yuri никогда не отображается в UI как установленный факт.

## 8. Нефункциональные требования

- Целевая и тестируемая платформа MVP — macOS. Архитектура не использует macOS-only domain interfaces, чтобы позже добавить Windows и Linux.
- UI не блокируется во время model/tool calls; обновления статуса поступают потоково.
- Холодный старт без миграции: целевой ориентир до 3 секунд на рекомендованном устройстве.
- Отмена активного run отражается в UI не позднее 1 секунды; фактическая остановка зависит от адаптера, но side effects после отмены не запускаются.
- Восстановление незавершённых jobs после crash без двойного side effect.
- Structured logs с correlation ID; уровни и redaction настраиваются.
- Unit tests для domain/policy, contract tests для provider/plugin API, integration tests для storage/scheduler, E2E smoke для ключевых Wails flows.
- Публичные Go API документированы; plugin protocol имеет JSON Schema и compatibility tests.
- Основной язык интерфейса: русский; строки готовы к i18n, английский добавляется без изменения компонентов.
- Reflection runs имеют отдельные дневные бюджеты, concurrency limit 1 и не должны заметно ухудшать latency foreground-диалога.
- Изменение mutable persona детерминированно воспроизводится из version log и откатывается без изменения исходной истории.

## 9. Критерии приёмки MVP

MVP принимается, если:

1. Чистая установка проходит onboarding и успешно выполняет тестовый запрос к настроенному провайдеру.
2. Ответ отображается потоково, его можно отменить, а ошибка провайдера объясняется без падения приложения.
3. Агент читает файл в разрешённой директории и отказывает в чтении за её пределами.
4. Изменение файла требует подтверждения и отражается в audit log.
5. Тестовый out-of-process plugin устанавливается, запрашивает permission, вызывает tool и корректно переживает crash/restart.
6. Yuri самостоятельно сохраняет релевантный факт без подтверждения, сразу видит live state и извлекает факт в другом диалоге с ссылкой на источник.
7. CRON-задача переживает перезапуск приложения, не запускается параллельно сама с собой и сохраняет историю run.
8. Quiet hours блокируют проактивное уведомление до разрешённого времени.
9. API key отсутствует в SQLite, логах, audit payload и model context.
10. Prompt injection из web/file content не может выдать новое разрешение или выполнить high-risk действие без подтверждения.
11. Голосовой запрос проходит STT → agent run → TTS, воспроизведение можно прервать, а microphone state всегда виден.
12. Background reflection не блокирует foreground chat, может корректно решить «ничего не менять» и не получает внешние side-effect tools.
13. На серии тестовых взаимодействий меняются relationship/mood и mutable persona; каждое изменение объяснимо, ограничено max delta и полностью откатывается.
14. Перевод памяти в dormant исключает её из обычного retrieval, а deliberate session search способен восстановить исходный эпизод.
15. OpenAI Codex App Server OAuth flow выполняет login/logout, показывает plan type и доступные rate limits без сохранения токенов в SQLite.
16. Antigravity adapter не использует сторонний OAuth piggyback; до появления разрешённого vendor contract он явно сообщает о недоступности и предлагает разрешённый способ подключения.
17. Все обязательные unit, integration и E2E smoke tests проходят на macOS.

## 10. Этапы разработки

### Этап 0. Engineering foundation

Монорепозиторий, Wails + React shell, конфигурация, structured logging, migrations, CI, базовые domain interfaces и threat model.

### Этап 1. Conversational agent vertical slice

Текстовый диалог, OpenAI-compatible adapter, официальный Codex App Server adapter, streaming, базовые STT/TTS, agent loop, встроенный read-only filesystem tool, approvals UI и audit.

### Этап 2. Storage and memory

Общий архив диалогов, FTS5/session search, embeddings abstraction, autonomous memory writes, core snapshot, hybrid retrieval, decay/dormant state, редактирование/удаление и retrieval budget.

### Этап 3. Plugin runtime

Manifest/schema, stdio RPC, process supervisor, permissions, dev mode, reference plugin и SDK.

### Этап 4. Scheduler and proactivity

Durable jobs, CRON/timezone/misfire, worker budgets, notifications, quiet hours и activity UI.

### Этап 5. Reflection, personality and avatar

Background review, relationship/affect model, versioned mutable persona, rollback UI, расширенные STT/TTS flows, avatar state machine и простой 2D renderer.

### Этап 6. Hardening and release

Cross-platform packaging, updates, backup/export, security review, fault injection, performance profiling и документация для пользователей/разработчиков плагинов.

## 11. Решения, которые нужно принять до реализации

### Принятые для Engineering foundation

- MVP разрабатывается и проверяется сначала на macOS; переносимость domain/application слоёв сохраняется.
- Toolchain: Go 1.25, Wails v2.15.0, Node.js 22 и npm с обязательным lockfile; см. ADR-0007.
- Голос STT + TTS входит в MVP и реализуется в Conversational agent vertical slice.
- Для macOS MVP push-to-talk использует OpenAI-compatible STT adapter; воспроизведение TTS использует системный WebKit/macOS speech synthesis с прерыванием. Provider-neutral TTS port и OpenAI-compatible adapter остаются доступны для последующего выбора голоса.
- Продукт локальный и single-user; multi-user/server mode не предусматривается.

### Блокирующие для Этапа 0

1. Лицензия GitHub-репозитория Yuri и лицензия Plugin SDK.
2. Допустим ли локальный встроенный inference в будущем или продукт всегда использует внешний endpoint.
3. Требуется ли шифрование всей локальной БД в первой версии или достаточно Keychain + шифрования чувствительных полей/backups.

### Блокирующие для продуктового scope MVP

6. Первый обязательный интеграционный плагин: Gmail, Telegram, Slack или только reference/demo plugin.
7. Должна ли Yuri работать, когда UI закрыт: menu bar process, LaunchAgent/autозапуск или только пока открыто окно.
8. Какой уровень yandere/tsundere используется как initial seed и какие trait ranges пользователь может снять.
9. Как часто разрешена фоновая рефлексия и какой ей выделяется дневной model budget.
10. Может ли persona evolution применяться полностью автоматически либо UI должен ненавязчиво уведомлять о каждом изменении, не требуя подтверждения.

## 12. Предлагаемые решения по умолчанию

- MVP и первый публичный релиз — macOS; затем Windows и Linux без изменения domain layer.
- Голос STT + TTS входит в MVP; полноценная Live2D/VRM-анимация остаётся последующим этапом.
- Первый плагин — reference plugin, затем Telegram или Gmail после стабилизации permission model.
- Нейтрально-tsundere профиль по умолчанию; yandere-интенсивность включается пользователем.
- Yuri самостоятельно пишет, редактирует, консолидирует и переводит память в dormant state; approval mode является опцией.
- Изменения личности применяются автоматически малыми шагами, показываются в Activity и полностью откатываются.
- Фоновый menu bar process поддерживается, но LaunchAgent/autозапуск с macOS выключен до явного согласия.
- SQLite — authoritative store, Pebble — cache/checkpoint store, чтобы не создавать два конкурирующих источника истины.

## 13. Влияние Hermes Agent и источники

Архитектура памяти Yuri использует и развивает следующие проверенные идеи Hermes Agent:

- bounded curated memory, которой агент сам управляет через add/replace/remove, вместо бесконечной подгрузки всех фактов;
- отдельный user profile и agent memory;
- frozen snapshot в system prompt для стабильного prefix cache при сохранении live memory state;
- полный SQLite/FTS5 archive с on-demand session search;
- memory flush перед compression и handoff-oriented сжатие длинного контекста;
- post-turn background review в отдельном fork, чтобы внутреннее обучение не конкурировало с текущим запросом пользователя;
- сканирование persistent memory на prompt injection и атомарные versioned writes.

Yuri отличается от Hermes наличием отдельной relationship memory, affective state, управляемого забывания, hybrid retrieval и самостоятельной версионируемой эволюции mutable persona.

Первичные материалы:

- [Hermes Agent repository](https://github.com/NousResearch/hermes-agent)
- [Hermes persistent memory](https://hermes-agent.nousresearch.com/docs/user-guide/features/memory)
- [Hermes prompt assembly](https://hermes-agent.nousresearch.com/docs/developer-guide/prompt-assembly)
- [Hermes memory tool source](https://github.com/NousResearch/hermes-agent/blob/main/tools/memory_tool.py)
- [Codex App Server authentication](https://learn.chatgpt.com/docs/app-server)
- [OpenAI authentication](https://learn.chatgpt.com/docs/auth)
- [Google clarification on third-party Antigravity OAuth use](https://github.com/google-gemini/gemini-cli/discussions/20632)
