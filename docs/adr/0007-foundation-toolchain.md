# ADR-0007: Toolchain Engineering foundation

- Статус: accepted
- Дата: 2026-08-27
- Область: Этап 0 / build and packaging

## Контекст

Воспроизводимая desktop-сборка требует одной закреплённой линии Go, Wails и Node.js. Репозиторий macOS-first, но domain/application пакеты и frontend должны проверяться независимо от desktop packaging.

## Решение

1. Минимальная версия Go для foundation — 1.25; точная версия задаётся директивой `go` в `go.mod`.
2. Desktop shell использует стабильную линию Wails v2, зафиксированную на v2.15.0. Миграция на Wails v3 требует отдельного ADR после выхода стабильного релиза и проверки API/packaging.
3. Frontend использует Node.js 22 LTS и npm. `package-lock.json` обязателен, локальная и CI-установка выполняется через `npm ci`.
4. Конфигурация Wails находится рядом с entrypoint в `cmd/yuri/wails.json`; это позволяет Wails v2 генерировать bindings и собирать Go package без переноса `main.go` в корень монорепозитория.
5. Обычная проверка выполняется через `make check`. Полная universal macOS OSS-сборка запускается через `make macos-smoke`; target сам использует закреплённый Wails CLI из `.tools/`, валидирует app metadata/архитектуры и создаёт SHA-256 manifest.
6. CI сохраняет zip и SHA-256 manifest только как GitHub Actions workflow artifact. В репозитории нет targets для внешнего распространения, публикации или credential-dependent signing.
7. Сгенерированные bindings, frontend assets и packaged binaries не являются исходниками и не коммитятся, кроме placeholder `.gitkeep` и platform packaging resources.

## Последствия

- CI и локальная разработка используют одинаковый lockfile и major-версии toolchain.
- Wails CLI остаётся отдельной локальной/CI зависимостью; unit-тесты ядра не зависят от GUI toolchain.
- Обновление Go, Node или Wails выполняется отдельным изменением с `make check` и macOS packaging smoke-test.
