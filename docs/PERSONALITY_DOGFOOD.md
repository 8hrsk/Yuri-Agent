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
| OpenAI-compatible | Настроенный endpoint и выбранная модель | Требует явного authenticated запуска владельцем |
| Codex App Server | Выбранная Codex model | Требует явного authenticated запуска владельцем |
| Antigravity OAuth | Не поддерживается текущим adapter | Не входит в текущую матрицу до появления рабочего auth adapter |

Мы не запускаем authenticated dogfood автоматически: каждый Preview и Chat расходует provider quota, а полный run создаёт не менее 28 ответов для двух профилей.

## Зафиксированный offline baseline

`docs/dogfood/personality-suite.fixture.json` — не результат внешней модели, а небольшой детерминированный canary. Он доказывает, что versioned формат, обе поверхности, scenario coverage, contrast contract и CLI report работают end-to-end. Unit tests отдельно подменяют ответы на плохие и проверяют обнаружение пропусков, дубликатов, потери русского языка, security regression и runaway expression.

P7.6 считается завершённой только после сохранения отчётов реальных поддерживаемых provider/model, исправления найденных различий и повторного зелёного прогона. Credentials и приватные диалоги в Git не коммитятся.
