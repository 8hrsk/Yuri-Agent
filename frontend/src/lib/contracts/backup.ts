export interface EncryptedBackupInput {
  /** Empty in Wails to request the native save dialog. */
  path?: string
  passphrase: string
  includeBlobs?: boolean
}

export interface EncryptedBackupInspectInput {
  /** Empty in Wails to request the native open dialog. */
  path?: string
  passphrase: string
}

export interface EncryptedBackupRestoreInput extends EncryptedBackupInspectInput {
  /** Empty in Wails to request a separate target directory. */
  targetDirectory?: string
}

export interface EncryptedBackupInfo {
  path: string
  createdAt: string
  sizeBytes: number
  blobCount: number
  hasConfig: boolean
  restoredTo?: string
}
