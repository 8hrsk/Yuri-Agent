import { useState } from 'react'

import type { EncryptedBackupInfo, YuriClient } from '../lib/contracts'
import { Icon } from './Icon'

type EncryptedBackupCardProps = {
  client: YuriClient
}

function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / (1024 * 1024)).toFixed(1)} MB`
}

export function EncryptedBackupCard({ client }: EncryptedBackupCardProps) {
  const [passphrase, setPassphrase] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [includeBlobs, setIncludeBlobs] = useState(true)
  const [busy, setBusy] = useState<'create' | 'validate' | 'restore'>()
  const [result, setResult] = useState<EncryptedBackupInfo>()
  const [feedback, setFeedback] = useState<{ kind: 'success' | 'error'; text: string }>()

  const run = async (operation: 'create' | 'validate' | 'restore') => {
    setFeedback(undefined)
    setResult(undefined)
    if (passphrase.length < 12) {
      setFeedback({ kind: 'error', text: 'Используйте пароль длиной не менее 12 символов.' })
      return
    }
    if (operation === 'create' && passphrase !== confirmation) {
      setFeedback({ kind: 'error', text: 'Пароли не совпадают.' })
      return
    }
    setBusy(operation)
    try {
      const info = operation === 'create'
        ? await client.createEncryptedBackup({ passphrase, includeBlobs })
        : operation === 'validate'
          ? await client.validateEncryptedBackup({ passphrase })
          : await client.restoreEncryptedBackup({ passphrase })
      if (!info) throw new Error('Backend не вернул сведения о backup.')
      setResult(info)
      setPassphrase('')
      setConfirmation('')
      setFeedback({
        kind: 'success',
        text: operation === 'create'
          ? 'Зашифрованная копия создана.'
          : operation === 'validate'
            ? 'Подлинность и целостность копии подтверждены.'
            : 'Копия восстановлена в отдельную директорию; активные данные Yuri не изменены.',
      })
    } catch (cause) {
      setFeedback({ kind: 'error', text: cause instanceof Error ? cause.message : 'Операция с backup завершилась ошибкой.' })
    } finally {
      setBusy(undefined)
    }
  }

  return (
    <section aria-labelledby="backup-title" className="settings-card backup-card">
      <div className="settings-card__heading">
        <div><span className="section-heading__overline">Data ownership</span><h2 id="backup-title">Зашифрованный backup</h2></div>
        <span className="settings-status settings-status--on"><i /> AES-256-GCM</span>
      </div>
      <p className="backup-card__intro">История, память и настройки экспортируются в один аутентифицированный файл. API keys, OAuth-токены и ссылки на Keychain исключаются.</p>
      <div className="settings-form backup-card__form">
        <label><span>Пароль backup <small>· не сохраняется</small></span><input autoComplete="new-password" onChange={(event) => setPassphrase(event.target.value)} type="password" value={passphrase} /></label>
        <label><span>Повторите пароль <small>· только для создания</small></span><input autoComplete="new-password" onChange={(event) => setConfirmation(event.target.value)} type="password" value={confirmation} /></label>
        <label className="toggle-label"><span>Включить вложения</span><button aria-checked={includeBlobs} className={`toggle${includeBlobs ? ' toggle--on' : ''}`} onClick={() => setIncludeBlobs((value) => !value)} role="switch" type="button"><i /></button></label>
      </div>
      <div className="settings-card__actions backup-card__actions">
        <button className="button button--accent" disabled={Boolean(busy)} onClick={() => void run('create')} type="button"><Icon name="lock" width={14} height={14} /> {busy === 'create' ? 'Создаю…' : 'Создать backup'}</button>
        <button className="button button--quiet" disabled={Boolean(busy)} onClick={() => void run('validate')} type="button">{busy === 'validate' ? 'Проверяю…' : 'Проверить файл'}</button>
        <button className="button button--quiet" disabled={Boolean(busy)} onClick={() => void run('restore')} type="button">{busy === 'restore' ? 'Восстанавливаю…' : 'Восстановить отдельно'}</button>
      </div>
      {feedback && <div className={`settings-feedback settings-feedback--${feedback.kind}`} role="status"><Icon name={feedback.kind === 'success' ? 'check' : 'warning'} width={15} height={15} /> {feedback.text}</div>}
      {result && <div className="backup-card__result"><strong>{result.restoredTo ? 'Восстановлено' : 'Файл проверен'}</strong><span>{result.restoredTo ?? result.path}</span><small>{formatBytes(result.sizeBytes)} · blobs: {result.blobCount} · {new Date(result.createdAt).toLocaleString('ru-RU')}</small></div>}
      <p className="settings-footnote"><Icon name="shield" width={13} height={13} /> Восстановление всегда идёт в отдельную директорию и никогда не перезаписывает активную базу данных.</p>
    </section>
  )
}
