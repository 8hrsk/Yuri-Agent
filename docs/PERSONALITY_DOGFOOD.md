# Personality dogfooding

Этот документ описывает воспроизводимую приёмку Personality Compiler на реальных ответах моделей. Она не заменяет unit/eval tests: offline-тесты проверяют детерминированный контракт, а dogfood обнаруживает случаи, когда конкретная модель игнорирует часть prompt или иначе ведёт себя в production Chat.

## Что проверяется

Один `provider/model` run содержит одинаковую матрицу для двух поверхностей:

- `preview` — изолированный Personality Preview без tools и persistent side effects;
- `chat` — новый обычный диалог с созданным агентом через production runtime.

Для каждой поверхности нужны минимум два контрастных профиля и все семь стабильных сценариев:

1. `introduction`;
2. `disagreement`;
3. `self_correction`;
4. `praise`;
5. `peer_praise`;
6. `fear`;
7. `reconciliation`.

Checker проверяет полноту матрицы, русский язык, минимальное качество ответа по сценарию, покрытие наблюдаемых сигналов профиля, различимость профилей, security invariants и чрезмерную эмоциональную экспрессию. Сигнал оценивается по всей поверхности, а не требуется дословно в каждой реплике: характер должен быть устойчиво заметен без механического речевого тика. Отчёт сохраняет `hits`, `total`, `actual` и `required` для каждой группы сигналов отдельно по Preview и Chat. Один успешный Preview не может скрыть регрессию Chat: поверхности оцениваются независимо.

## Как собрать реальный run

Для Codex App Server полный run можно собрать одной командой. Runner создаёт
одноразовый test-profile, не читает пользовательские диалоги/агентов/память,
отключает post-turn reflection и удаляет временный профиль после завершения:

```bash
go run ./cmd/yuri-personality-eval \
  -live-codex \
  -suite /absolute/path/personality-suite.json \
  -report /absolute/path/personality-report.json
```

Параметр `-model` необязателен; без него используется `codex-default`. Полный
run выполняет 28 основных model turns: по семь Preview и Chat-сценариев для
двух профилей. В suite сохраняются только фиксированные сценарии и ответы;
credentials, внутренние идентификаторы и локальные данные пользователя туда
не попадают.

Для явного authenticated прогона уже настроенного OpenAI-compatible провайдера
(например, OpenRouter) используйте его существующий `provider ID`:

```bash
go run ./cmd/yuri-personality-eval \
  -live-openai-compatible \
  -provider-id openrouter \
  -suite /absolute/path/openrouter-suite.json \
  -report /absolute/path/openrouter-report.json
```

`-model` здесь также необязателен: по умолчанию берётся модель, сохранённая у
выбранного провайдера. Для разового сравнения можно передать `-model
<provider-model>` — это меняет только disposable profile и не редактирует
настройки владельца. `-live-openrouter` является удобным алиасом той же команды.

Если provider временно оборвал полный run, runner сохраняет совместимый partial
suite. Продолжить только отсутствующие samples можно той же командой с
`-resume`. Перед первым новым запросом CLI строго проверяет format/version,
provider, model, behavioral contracts, sample keys и отсутствие дубликатов;
смешивание результатов разных моделей или матриц запрещено.

Перед запуском проверьте, что provider ID существует в Settings, имеет
OpenAI-compatible kind, выбранную модель и credential reference. Сам API key
извлекает production adapter через системный keyring; CLI не принимает, не
читает и не печатает его. Runner читает только owner config metadata, копирует в
одноразовый профиль только выбранный route и его opaque `credential_ref`, а
затем создаёт новую временную SQLite/data директорию. Диалоги, AgentProfile,
memory, allowed directories, plugins и другие owner-local данные не читаются и
не копируются. Временный профиль удаляется после завершения.

`-input` нельзя использовать вместе с live-флагом; `-live-codex` и
`-live-openai-compatible` также взаимоисключающие. Если authenticated provider
упал после части матрицы, уже собранный suite сохраняется по `-suite` и путь к
partial suite указывается в stderr. В suite/report остаются только публичные
provider/model labels, фиксированные prompts/scenarios и ответы модели — не
токены, keyring values или owner history.

Для ручной фиксации другого провайдера:

1. Создать два или более контрастных агента. Рекомендуемая первая пара — `Застенчивая аналитик` и `Острая цундере`; дополнительный контроль — `Заботливая спутница`.
2. Зафиксировать точные наблюдаемые сигналы каждого профиля в `contracts[].signal_groups`. Каждая внутренняя группа — допустимые варианты одного сигнала, а `minimum_signal_coverage` задаёт долю ответов профиля, где он должен быть заметен. Значение `0` означает строгий legacy-default `100%`; для естественной речи обычно достаточно `0.4–0.6`.
3. В Review явно запустить каждый сценарий Preview и сохранить только финальный текст ответа.
4. Создать новый диалог для каждого Chat-сценария, отправить тот же prompt из `internal/desktop/personality_preview.go` и сохранить только финальный ответ агента. Thinking, tool calls, user messages и memory не входят в sample.
5. Скопировать `docs/dogfood/personality-suite.fixture.json`, заменить fixture provider/model и ответы реальными значениями. Не добавлять API keys, OAuth tokens, system prompts или личную историю диалогов.
6. Проверить файл:

```bash
go run ./cmd/yuri-personality-eval \
  -input /absolute/path/personality-suite.json \
  -report /absolute/path/personality-report.json
```

Коды завершения:

- `0` — полная матрица прошла контракт;
- `1` — JSON корректен, но найдены behavioral failures;
- `2` — файл, schema или CLI-вызов некорректны.

JSON decoder запрещает неизвестные поля, размер входа ограничен 8 MiB, а отчёт создаётся с правами `0600`. Формат `yuri.personality-dogfood-suite` version `1` не содержит credentials или локальные идентификаторы агентов.

## Provider matrix

| Provider | Что принимается | Статус реального прогона |
| --- | --- | --- |
| OpenAI-compatible | Настроенный endpoint и выбранная модель | OpenRouter `minimax/minimax-m3:free` пройден 2026-09-01: 28/28, отчёт зелёный |
| Codex App Server | Выбранная Codex model | `codex-default` пройден 2026-08-31: 28/28, отчёт зелёный |
| Antigravity OAuth | Не поддерживается текущим adapter | Не входит в текущую матрицу до появления рабочего auth adapter |

Мы не запускаем authenticated dogfood автоматически: каждый Preview и Chat расходует provider quota, а полный run создаёт не менее 28 ответов для двух профилей.

## Ручной per-agent OpenRouter smoke

Этот короткий проход проверяет не только Personality Compiler, но и маршрутизацию разных именованных агентов. Он запускается владельцем из приложения после сохранения OpenRouter token в системном keyring.

1. В Settings проверить OpenRouter endpoint, получить список моделей и выбрать хотя бы одну модель. Token не копируется в отчёты, логи или screenshots.
2. Назначить, например, Emily маршрут `OpenRouter · <выбранная модель>`, а Yuri — `Codex App Server · <выбранная модель>` или другой уже настроенный provider. Это можно сделать на шаге Model creation flow либо в Personality существующего агента.
3. Переключить активного агента и убедиться, что одинаковый route label виден в roster и Chat header сразу после сохранения, без перезапуска приложения.
4. Отправить каждому агенту один обычный запрос и один запрос с безопасным tool call. Проверить streaming trace, финальный ответ и отсутствие fallback на маршрут другого агента.
5. Явно попросить одного агента обратиться к другому. В Collaboration проверить имена участников, оба текущих route label, transcript, aggregate token usage, terminal status и исторический route/usage каждой реплики, привязанный к её source run.
6. Дождаться или явно инициировать разрешённую reflection и проверить, что она использует маршрут агента-владельца состояния. Provider error или rate limit одного маршрута не должен переключать другого агента на этот provider и не должен раскрывать credential.
7. Для тестового `rate_limit` или временного сбоя проверить безопасную категорию, retry hint и явную фразу об отсутствии переключения маршрута. Для authentication/quota/context/model-unavailable проверить отсутствие автоматического повтора и конкретное действие в UI; upstream body, token и credential не должны появляться в trace или Collaboration.
8. В Chat проверить, что recovery-кнопки показаны только у последнего fragment run: retry повторяет исходный turn даже без assistant bubble, context/budget создаёт новый диалог, а account/model actions открывают Settings или route editor. В Collaboration model action должен выбрать responder-а неуспешного peer turn, а не текущего наблюдателя.

Для каждой проверки фиксируются: агент, текущий provider/model, сценарий, run status, token usage и наблюдаемый результат. Collaboration показывает **текущие** маршруты участников отдельно от неизменяемой исторической provider/model attribution конкретной реплики; Chat показывает тот же persisted route и usage в execution trace. Старые run до migration остаются явно неатрибутированными и не подменяются текущими настройками.

Успешный smoke означает, что независимые маршруты работают в Chat, tools, peer exchange и reflection. Он не заменяет полную матрицу из 28 Personality samples ниже: реальные ответы OpenRouter добавляются в versioned suite только после отдельного явного прогона владельцем.

## Зафиксированный offline baseline

`docs/dogfood/personality-suite.fixture.json` — не результат внешней модели, а небольшой детерминированный canary. Он доказывает, что versioned формат, обе поверхности, scenario coverage, contrast contract и CLI report работают end-to-end. Unit tests отдельно подменяют ответы на плохие и проверяют обнаружение пропусков, дубликатов, потери русского языка, security regression и runaway expression.

P7.6 считается завершённой только после сохранения отчётов реальных поддерживаемых provider/model, исправления найденных различий и повторного зелёного прогона. Credentials и приватные диалоги в Git не коммитятся.

## Реальный baseline

- `docs/dogfood/results/codex-default-2026-08-31.suite.json` — полный
  изолированный run Codex App Server на фиксированных prompts;
- `docs/dogfood/results/codex-default-2026-08-31.report.json` — зелёный отчёт:
  reserved-сигналы `7/7` на Preview и Chat, direct-сигналы `6/7` на Preview и
  `7/7` на Chat при пороге `60%`.
- `docs/dogfood/results/openrouter-2026-09-01.suite.json` — полный
  authenticated run OpenRouter `minimax/minimax-m3:free`;
- `docs/dogfood/results/openrouter-2026-09-01.report.json` — зелёный отчёт:
  reserved-сигналы `7/7` на Preview и Chat, direct-сигналы `6/7` на Preview и
  `5/7` на Chat при пороге `60%`.

Первый реальный run выявил две проблемы harness, а не модели: non-streaming
Codex response отсутствовал в fallback event buffer, хотя был корректно
сохранён в transcript, а русские формы «начать»/«не согласна» не проходили
слишком узкие лексические проверки. Runner теперь читает durable assistant
segments по точному `runId`, а rubric учитывает естественные русские формы и
оценивает прямоту по набору решительных формулировок вместо навязывания
повторяющегося «скажу прямо».

OpenRouter baseline выявил ещё две проблемы harness: временный first-byte
timeout обнулял полезность уже собранной части матрицы, а task-quality rubric
не распознавал естественные русские эквиваленты «сломать/баги», «Пожалуйста» и
«Слышу». Live CLI теперь явно возобновляет совместимый partial suite через
`-resume`, а новые эквиваленты закреплены provider-independent tests.
