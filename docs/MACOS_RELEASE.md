# macOS build и release boundary

macOS — единственная целевая ОС MVP. Репозиторий строит universal app (`arm64` + `x86_64`) для macOS 11 и новее. CI публикует только короткоживущий development artifact с ad-hoc подписью; это не публичный релиз.

## Воспроизводимый development smoke

Требования: Xcode Command Line Tools, Go и Node версий из `go.mod`/CI. Wails CLI не берётся из `PATH` или `@latest`: `make wails-install` устанавливает ровно `v2.15.0` в `.tools/`.

```text
make macos-smoke YURI_VERSION=0.7.0-dev
```

Команда:

1. собирает production-mode universal `.app` с `-trimpath`;
2. применяет явную ad-hoc подпись;
3. проверяет plist, bundle id, minimum OS, microphone usage string, обе архитектуры и целостность подписи;
4. создаёт zip через `ditto` и отдельный SHA-256 manifest.

Результат находится в `dist/macos/`. Development artifact нельзя выдавать за подписанный или notarized release.

## Публичный релиз

Подпись Developer ID, Apple notarization, stapling и публикация требуют внешних credentials и выполняются только в доверенном release environment. Они намеренно не автоматизируются обычным PR CI и не должны использовать secrets из developer checkout.

После внешнего signing/notarization поместите app и zip/checksum в пути, заданные `MACOS_APP`, `MACOS_ARTIFACT` и `MACOS_CHECKSUM`, затем запустите:

```text
make macos-release-check
```

Проверка требует:

- `Developer ID Application` signature, а не ad-hoc;
- успешный Gatekeeper assessment;
- валидный stapled notarization ticket;
- universal Mach-O;
- совпадающий SHA-256 архива.

Upload/publish, auto-update feed и certificate lifecycle остаются внешними действиями: этот target только проверяет уже подготовленный артефакт.

## Checklist

- Все `make check` и race-тесты зелёные.
- Backup/restore negative tests и SQLite integrity/fault tests зелёные.
- Версия приложения и release notes согласованы.
- Нет secrets в bundle, logs, generated bindings, archive или checksum manifest.
- App подписан, notarized и stapled; Gatekeeper assessment проходит на чистой системе.
- SHA-256 опубликован рядом с immutable release artifact.
- Проверены onboarding, text/voice, approvals, memory, plugin crash recovery, scheduler restart и quiet hours.
