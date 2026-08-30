import { useCallback, useMemo, useRef, useState } from 'react'

import type { PluginEnableRequest, PluginPermission, PluginRecord } from '../lib/contracts'
import {
  buildEnableRequest,
  declaredScope,
  draftNeedsUnrestrictedConfirmation,
  draftScope,
  emptyConsentDraft,
  isUnrestrictedScope,
  pluginScopeKinds,
} from '../lib/plugin-consent'
import type { PluginConsentDraft } from '../lib/plugin-consent'
import { Icon } from './Icon'
import { ModalShell } from './ModalShell'

function describeScope(kind: string, values: string[]): string {
  if (kind === 'unrestricted' || !kind) return 'без ограничений'
  if (values.length === 0) return kind
  return `${kind}: ${values.join(', ')}`
}

function initialDrafts(permissions: readonly PluginPermission[]): Record<string, PluginConsentDraft> {
  const drafts: Record<string, PluginConsentDraft> = {}
  for (const permission of permissions) {
    // Nothing is pre-approved: a manifest declaration is a request. An already
    // granted capability stays checked so re-consenting does not silently
    // revoke what the owner approved earlier.
    const draft = emptyConsentDraft(permission.granted)
    if (draft.approved && isUnrestrictedScope(declaredScope(permission))) draft.allowUnrestricted = true
    drafts[permission.capability] = draft
  }
  return drafts
}

/**
 * Owner consent for enabling a plugin.
 *
 * The dialog exists because the manifest and the grant are different things.
 * It lists what the plugin *asks* for, hands the owner a per-capability
 * decision, lets them narrow a declared scope instead of accepting it whole,
 * and makes an unbounded grant a separate deliberate act. Capabilities left
 * unchecked are simply absent from the request, which is how the backend is
 * told to refuse them.
 *
 * Modal behaviour (focus trap, Escape, `inert`) comes from ModalShell, the
 * same one ApprovalDialog uses.
 */
export function PluginConsentDialog({
  busy,
  error,
  onCancel,
  onConfirm,
  plugin,
}: {
  busy: boolean
  /** A rejection from the backend, shown verbatim rather than swallowed. */
  error?: string
  onCancel: () => void
  onConfirm: (request: PluginEnableRequest) => void
  plugin: PluginRecord
}) {
  const permissions = plugin.permissions
  const cancelRef = useRef<HTMLButtonElement>(null)
  const [drafts, setDrafts] = useState<Record<string, PluginConsentDraft>>(() => initialDrafts(permissions))
  const [issues, setIssues] = useState<Record<string, string>>({})

  const handleEscape = useCallback(() => {
    if (busy) return
    onCancel()
  }, [busy, onCancel])

  const update = (capability: string, patch: Partial<PluginConsentDraft>) => {
    setDrafts((current) => ({ ...current, [capability]: { ...current[capability], ...patch } }))
    setIssues((current) => {
      if (!(capability in current)) return current
      const next = { ...current }
      delete next[capability]
      return next
    })
  }

  const approvedCount = useMemo(
    () => permissions.filter((permission) => drafts[permission.capability]?.approved).length,
    [drafts, permissions],
  )

  const submit = () => {
    const result = buildEnableRequest(plugin.id, permissions, drafts)
    if (!result.request) {
      setIssues(result.errors)
      return
    }
    setIssues({})
    onConfirm(result.request)
  }

  return (
    <ModalShell
      backdropClassName="approval-backdrop"
      className="approval-dialog plugin-consent"
      describedBy="plugin-consent-description"
      initialFocusRef={cancelRef}
      labelledBy="plugin-consent-title"
      onEscape={handleEscape}
    >
      <div className="approval-dialog__mark"><Icon name="shield" width={22} height={22} /></div>
      <span className="section-heading__overline">Запрос доступа</span>
      <h2 id="plugin-consent-title">{`Включить «${plugin.name}»`}</h2>
      <p id="plugin-consent-description">
        Плагин запрашивает эти capabilities. Пока вы не отметите их сами, ничего не выдаётся: невыбранные
        запросы будут отклонены во время работы плагина.
      </p>

      {permissions.length === 0 ? (
        <p className="approval-dialog__hint">Плагин не запрашивает capabilities. Он будет включён без единого доступа.</p>
      ) : (
        <ul className="plugin-consent__list">
          {permissions.map((permission) => {
            const draft = drafts[permission.capability] ?? emptyConsentDraft()
            const declared = declaredScope(permission)
            const effective = draftScope(permission, draft)
            const needsUnrestricted = draftNeedsUnrestrictedConfirmation(permission, draft)
            const issue = issues[permission.capability]
            return (
              <li className="plugin-consent__row" key={permission.capability}>
                <label className="plugin-consent__approve">
                  <input
                    checked={draft.approved}
                    disabled={busy}
                    onChange={(event) => update(permission.capability, { approved: event.target.checked })}
                    type="checkbox"
                  />
                  <span>
                    <strong>{permission.capability}</strong>
                    <small>{`Запрошено: ${describeScope(declared.kind, declared.values)}`}</small>
                    {permission.description && <small>{permission.description}</small>}
                  </span>
                </label>

                {draft.approved && (
                  <div className="plugin-consent__scope">
                    <label>
                      <span>{`Вид scope для ${permission.capability}`}</span>
                      <select
                        disabled={busy}
                        onChange={(event) => update(permission.capability, { scopeKind: event.target.value })}
                        value={draft.scopeKind}
                      >
                        <option value="">как в манифесте</option>
                        {pluginScopeKinds.map((kind) => <option key={kind} value={kind}>{kind}</option>)}
                      </select>
                    </label>
                    <label>
                      <span>{`Значения scope для ${permission.capability}`}</span>
                      <input
                        disabled={busy || draft.scopeKind === 'unrestricted'}
                        onChange={(event) => update(permission.capability, { scopeValues: event.target.value })}
                        placeholder={declared.values.join(', ') || 'через запятую'}
                        spellCheck={false}
                        value={draft.scopeValues}
                      />
                    </label>
                    <label>
                      <span>{`Срок действия в часах для ${permission.capability}`}</span>
                      <input
                        disabled={busy}
                        min={1}
                        onChange={(event) => update(permission.capability, { expiresInHours: event.target.value })}
                        placeholder="бессрочно"
                        type="number"
                        value={draft.expiresInHours}
                      />
                    </label>
                    <p className="plugin-consent__effective">{`Будет выдано: ${describeScope(effective.kind, effective.values)}`}</p>

                    {needsUnrestricted && (
                      <label className="plugin-consent__unrestricted">
                        <input
                          checked={draft.allowUnrestricted}
                          disabled={busy}
                          onChange={(event) => update(permission.capability, { allowUnrestricted: event.target.checked })}
                          type="checkbox"
                        />
                        <span>{`Подтверждаю неограниченный доступ для ${permission.capability}`}</span>
                      </label>
                    )}

                    {issue && <p className="plugin-consent__issue" role="alert">{issue}</p>}
                  </div>
                )}
              </li>
            )
          })}
        </ul>
      )}

      {error && <p className="approval-dialog__error" role="alert">{error}</p>}

      <div className="approval-dialog__actions">
        <button className="button button--quiet" disabled={busy} onClick={onCancel} ref={cancelRef} type="button">Отмена</button>
        <button className="button button--accent" disabled={busy} onClick={submit} type="button">
          {busy ? 'Включаю…' : `Включить с выбранными доступами (${approvedCount}/${permissions.length})`}
        </button>
      </div>
    </ModalShell>
  )
}
