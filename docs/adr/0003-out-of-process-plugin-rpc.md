# ADR-0003: Плагины работают out-of-process через versioned stdio RPC

- Статус: accepted
- Дата: 2026-08-27
- Область: Этап 0 boundary; runtime реализуется на Этапе 3

## Контекст

Плагины должны быть переносимыми между macOS, Windows и Linux и не должны связывать ABI с конкретной сборкой Go. Плагин — потенциально недоверенный исполняемый код. In-process `go plugin` не даёт нужной изоляции и усложняет обновление/краш recovery.

## Решение

1. Plugin package содержит manifest, executable для target OS/arch, protocol version, declared tools/event sources/permissions и checksum/signature metadata.
2. Core запускает plugin как отдельный процесс и обменивается сообщениями по versioned JSON-RPC-подобному протоколу поверх stdio.
3. Host — единственная сторона, которая предоставляет capability-mediated calls, scoped credentials, tool results и event publication.
4. Plugin не получает direct path к SQLite, Keychain, UI sockets или неописанным файловым roots.
5. Каждое сообщение ограничивается по размеру и времени; supervisor выполняет health-check, lease, graceful stop и принудительное завершение зависшего процесса.
6. Manifest declaration пересекается с выданным пользовательским grant. Plugin не может сам расширить capability/scope.
7. Package устанавливается выключенным. Подпись/checksum/source commit/compatibility записываются в SQLite и отображаются владельцу. Unsigned package разрешён только в явном dev mode.

## Последствия

Положительные:

- crash плагина не завершает UI/core;
- можно писать плагины на разных языках;
- permission enforcement остаётся в одном host policy boundary.

Ограничения:

- RPC protocol, packaging и process supervision нужно тестировать отдельно;
- отдельный процесс не является полной OS sandbox без дополнительной macOS sandbox policy;
- latency и serialization overhead выше, чем у in-process call.

## Отложенные вопросы

- точный wire schema и transport upgrade (например, localhost gRPC) после reference plugin;
- code signing/notarization и OS sandbox profile для release packages;
- GitHub repository browsing/automatic install — отдельная product feature после core/plugin security review.

