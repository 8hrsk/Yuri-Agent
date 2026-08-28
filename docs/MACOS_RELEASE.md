# macOS OSS build

macOS — целевая и тестируемая ОС MVP. Репозиторий собирает universal app (`arm64` + `x86_64`) для macOS 11 и новее. Единственный автоматизированный путь — проверяемый OSS-артефакт GitHub Actions: universal `.app`, zip-архив и SHA-256 manifest. Workflow сохраняет их только как CI artifact с ограниченным сроком хранения.

Этот артефакт предназначен для CI, разработки и ручного тестирования. Проект не управляет signing identities, notarization или внешним каналом распространения. Внешние credentials, сертификаты и публикация за пределами CI artifact не используются.

## Воспроизводимая сборка

Требования: Xcode Command Line Tools, Go и Node версий из `go.mod`/CI. Wails CLI не берётся из `PATH` или `@latest`: `make wails-install` устанавливает ровно `v2.15.0` в `.tools/`.

```text
make macos-smoke YURI_VERSION=0.7.0
```

Команда:

1. собирает production-mode universal `.app` с `-trimpath` и минимальной версией macOS 11.0.0;
2. проверяет `Info.plist`, bundle id, версии, minimum OS, microphone usage string и обе архитектуры;
3. создаёт zip через `ditto`, записывает SHA-256 manifest и тут же проверяет его.

Результат находится в `dist/macos/`. Архив и manifest должны быть получены из одной сборки; manifest нельзя редактировать вручную.

## Граница CI

Job `macos-foundation` в `.github/workflows/ci.yml` запускает тот же `make macos-smoke`, а затем загружает `dist/macos/*.zip` и `dist/macos/*.sha256` через `actions/upload-artifact`. Это единственное автоматическое сохранение артефактов. CI artifact не является установщиком, обновляющим каналом или обещанием совместимости за пределами проверенной macOS universal сборки.

## Wails launch и UI smoke

После сборки можно проверить фактический запуск `.app` и onboarding interaction без Playwright или внешнего WebDriver:

```text
make macos-launch-smoke MACOS_APP=cmd/yuri/build/bin/yuri.app
make macos-ui-smoke MACOS_APP=cmd/yuri/build/bin/yuri.app
```

Оба target запускают bundle через `/usr/bin/open -n -W`, поэтому проверяется именно macOS application lifecycle, а не отдельный executable. Launch Services явно получает закрытые тестовые переменные окружения: приложение использует временный profile root, после загрузки WebKit DOM атомарно публикует readiness marker и само вызывает штатный quit. Скрипт проверяет marker, создание изолированной SQLite БД, завершение процесса и `PRAGMA query_only=ON; PRAGMA integrity_check`. Временный root удаляется после успешного и аварийного завершения; при ошибке выводится launch diagnostics.

`macos-launch-smoke` остаётся быстрым lifecycle smoke с границей `OnDomReady`. `macos-ui-smoke` дополнительно подключает bridge, который существует только при явном `YURI_TEST_MODE=1` и изолированном profile root. Встроенный WebKit-сценарий ждёт React UI, нажимает welcome, заполняет provider form, отправляет typed payload, проверяет success screen и открывает Chat. Reporter принимает только фиксированную последовательность checkpoint, записывает результат с правами `0600`, маскирует canary и завершает приложение. В production launch этот bridge не bind-ится.

Provider response в UI-smoke детерминированно подменяется на renderer bridge boundary, поэтому тест не требует сети или credentials. Реальный Bridge → OpenAI-compatible HTTP/SSE lifecycle отдельно проверяется race-enabled `make mvp-smoke`. Вместе эти проверки покрывают UI interaction и production backend path без нестабильного внешнего сервиса. Smoke требует macOS interactive GUI session.

Для локальной проверки уже собранного bundle можно вызвать валидатор напрямую:

```text
scripts/validate-macos-oss.sh \
  --app cmd/yuri/build/bin/yuri.app \
  --version 0.7.0
```

Проверка архива выполняется тем же checksum helper:

```text
scripts/checksum-artifact.sh --verify \
  dist/macos/yuri-0.7.0-macos-universal.zip \
  dist/macos/yuri-0.7.0-macos-universal.zip.sha256
```

## Checklist

- Все `make check` и race-тесты зелёные.
- Backup/restore negative tests и SQLite integrity/fault tests зелёные.
- Версия приложения и release notes согласованы с `cmd/yuri/wails.json`.
- Нет secrets в bundle, logs, generated bindings, archive или checksum manifest.
- Валидатор подтверждает `APPL`, bundle id `ai.ordo.yuri`, macOS 11.0.0 и обе Mach-O архитектуры.
- SHA-256 manifest создан автоматически и проходит повторную проверку.
- Проверены onboarding, text/voice, approvals, memory, plugin crash recovery, scheduler restart и quiet hours.
- Native WebKit onboarding smoke проходит welcome → provider → success → Chat без утечки test canary.
