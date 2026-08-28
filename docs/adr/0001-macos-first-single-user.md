# ADR-0001: macOS-first локальное single-user desktop-приложение

- Статус: accepted
- Дата: 2026-08-27
- Область: Этап 0 / deployment boundary

## Контекст

Yuri разворачивается локально для одного владельца. Все диалоги, память, отношения, mutable persona, разрешения и фоновые задачи принадлежат одному локальному профилю. Multi-user, серверный multi-tenant режим, shared workspace и удалённая авторизация не входят в продуктовую модель.

MVP нужно быстро проверять на одной desktop-платформе, но domain-слой нельзя связывать с APIs конкретной ОС: после стабилизации возможны Windows и Linux релизы.

## Решение

1. Целевая и тестируемая платформа MVP — macOS.
2. UI реализуется в Wails + React, core — в Go, работающем в том же локальном приложении.
3. Один локальный installation/profile является владельцем всех durable данных. В моделях данных не вводятся `user_id`, tenant routing или network session semantics только ради будущего multi-user.
4. Файловые roots, microphone, notifications и Keychain выдаются через нативные macOS permissions и локально сохраняемые grants.
5. Platform-specific функции скрываются за ports/adapters. В domain/application не импортируются macOS frameworks.
6. Windows/Linux добавляются последующими релизами через новые adapters и packaging targets, а не через изменение domain invariants.

## Последствия

Положительные:

- проще threat model, onboarding, backup и data ownership;
- можно использовать Keychain и macOS packaging как первый поддерживаемый OSS path;
- нет ложного обещания серверной изоляции пользователей.

Ограничения:

- CI и build verification сначала ориентированы на macOS;
- локальный процесс защищён только моделью безопасности текущей OS account;
- UI не должен предполагать, что приложение доступно через remote API.

## Что не следует делать

- добавлять server mode или общий plugin registry как скрытый scope;
- зашивать macOS calls в domain layer;
- считать Wails bridge границей авторизации без повторной проверки на стороне Go core.
