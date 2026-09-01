# Security и жизненный цикл данных

## Назначение

Security boundaries отделяют model intent от реального IO, owner grants от
temporary approval и durable data от secrets. Без active scoped grant действие
denied; наличие personality, schedule или plugin manifest не создаёт
capability.

## Policy, grants и approval

[PolicyEvaluator](../../internal/security/policy.go) — deny-by-default
authorizer. Он выбирает active, unexpired PermissionGrant с совпадающей
capability и покрывающим scope:

| Risk | Результат при покрывающем grant |
| --- | --- |
| low | allow |
| medium/high | needs approval |
| critical | deny |

Approval — отдельное durable owner decision с exact action hash:
[internal/domain/approval.go](../../internal/domain/approval.go). Risk не
расширяет scope; revoked/expired grant перестаёт покрывать request.
Runtime вызывает policy непосредственно перед tool execution, а не при
модельном планировании. Desktop approval flow:
[internal/desktop/chat_approval.go](../../internal/desktop/chat_approval.go).

## Filesystem boundary

[PathAllowlist](../../internal/security/path_allowlist.go) принимает только
absolute existing directory roots, canonicalizes symlinks и стабильно хранит
roots. Resolve допускает лишь existing target внутри root. ResolveForWrite
проверяет canonical existing parent, разрешает отсутствующий leaf для create
и отвергает symlink target; relative path никогда не означает implicit current
working-directory permission.

Policy scope coverage дополнительно canonicalizes filesystem grant/request,
включая корректную обработку missing leaf:
[security/policy.go](../../internal/security/policy.go). Filesystem tools
в [filesystem_read.go](../../internal/tools/filesystem_read.go) и
[filesystem_write.go](../../internal/tools/filesystem_write.go) должны пройти
и allowlist, и policy/approval. Это уменьшает path traversal и symlink escape,
но не превращает UI path display в authorизацию.

## Web boundary

[web_fetch.go](../../internal/tools/web_fetch.go) разрешает bounded public
HTTP/HTTPS fetch только после network policy check. Он запрещает credentials,
небезопасные schemes/ports и local/LAN targets, проверяет redirect заново,
резолвит все адреса и pin-ит проверенный public IP для dial. Output/content
type/response size ограничены.

[web_search.go](../../internal/tools/web_search.go) использует нормализованный
SearXNG endpoint и bounded result count. Search result — data; чтение URL
производится отдельным web.fetch и проходит его SSRF boundary. Внешняя
страница, tool output и redirect metadata недоверенны для модели.

## Secrets, config и logs

[internal/security/keyring/store.go](../../internal/security/keyring/store.go)
хранит credential value только в OS keyring. Config/SQLite сохраняют opaque
reference с ограниченным форматом; keyring backend errors нормализуются, чтобы
не утечь command output или credential material.

[Config](../../internal/config/config.go) записывает не-секретные settings
атомарно в private directory. Provider adapters извлекают secret только во
время auth boundary. Runtime и provider layers не должны класть API key,
Authorization header, raw prompt с user secrets или raw upstream body в
audit/log/UI error; error redaction применяется до publication.

## Attachments, transcript и memory

Blob directory отделён от SQLite. Attachment/backstory/memory/archive не
становятся system instructions: assembler упаковывает их в untrusted envelope.
Memory lifecycle edit/hide/forget создаёт revision/tombstone; он не должен
молчаливо удалять immutable conversation message. Highly-sensitive и hidden
memory исключаются из обычного recall. Подробнее:
[memory-and-storage.md](memory-and-storage.md).

## Encrypted backup и restore

[internal/backup/backup.go](../../internal/backup/backup.go) экспортирует verified SQLite snapshot,
sanitized non-secret config metadata и bounded regular blobs. Current envelope
использует streaming AES-256-GCM frames с scrypt-derived key; associated data,
per-archive salt/nonce и frame sequence защищают от reorder/splice. Limits
ограничивают archive/plaintext/database/config/blob count/size, чтобы
attacker-controlled backup не выделил неограниченную память.

Restore сначала validates envelope/manifest/path/checksums и работает с
private temporary files; unsafe paths, symlinks, duplicate entries и size
overflow отклоняются. Legacy sealed envelope может читаться, но новый export
пишет streaming format. Bridge backup surface:
[internal/desktop/backup.go](../../internal/desktop/backup.go);
crypto/export source: [crypto.go](../../internal/backup/crypto.go),
[export.go](../../internal/backup/export.go).

Passphrase и decrypted plaintext не следует сохранять в config, logs или
diagnostic artifact. Restore — material state replacement operation: его
нужно выполнять через подтверждённый UI/backend flow, а не через model tool.

## Cancellation, audit и residual risk

Cancellation прерывает network/tool/backup contexts, но не отменяет уже
успешно завершённый external side effect. Audit фиксирует разрешённые действия
и terminal result без raw secrets; он не является криптографическим журналом
внешней системы.

Known implementation boundary: reserved Paths.PebbleDirectory не имеет
встроенного Pebble adapter в текущем дереве и не должен рассматриваться как
второй source of truth. Backup не делает network account/provider secrets
portable; owner должен заново предоставить credential в target environment.

## Tests

Покрытие security contracts:
[policy_test.go](../../internal/security/policy_test.go),
[path_allowlist_test.go](../../internal/security/path_allowlist_test.go),
[web_fetch_test.go](../../internal/tools/web_fetch_test.go),
[filesystem_write_test.go](../../internal/tools/filesystem_write_test.go),
[internal/backup/backup_test.go](../../internal/backup/backup_test.go) и
[kdf_salt_test.go](../../internal/backup/kdf_salt_test.go).
