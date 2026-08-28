package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/backup"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const backupOperationTimeout = 10 * time.Minute

// EncryptedBackupInput is supplied only by an explicit owner action. The
// passphrase is used for this call and is never persisted, logged or audited.
type EncryptedBackupInput struct {
	Path         string `json:"path,omitempty"`
	Passphrase   string `json:"passphrase"`
	IncludeBlobs bool   `json:"includeBlobs,omitempty"`
}

// EncryptedBackupInspectInput validates an archive without restoring it.
type EncryptedBackupInspectInput struct {
	Path       string `json:"path,omitempty"`
	Passphrase string `json:"passphrase"`
}

// EncryptedBackupRestoreInput materializes a validated archive into a
// separate directory. It never replaces or activates Yuri's live database.
type EncryptedBackupRestoreInput struct {
	Path            string `json:"path,omitempty"`
	TargetDirectory string `json:"targetDirectory,omitempty"`
	Passphrase      string `json:"passphrase"`
}

// EncryptedBackupView is safe for the UI. It contains no key material and no
// config payload, only archive metadata and a user-selected local path.
type EncryptedBackupView struct {
	Path       string `json:"path"`
	CreatedAt  string `json:"createdAt"`
	SizeBytes  int64  `json:"sizeBytes"`
	BlobCount  int    `json:"blobCount"`
	HasConfig  bool   `json:"hasConfig"`
	RestoredTo string `json:"restoredTo,omitempty"`
}

// CreateEncryptedBackup creates a password-encrypted, authenticated export.
// An empty path opens the native macOS save dialog from this user gesture.
func (b *Bridge) CreateEncryptedBackup(input EncryptedBackupInput) (EncryptedBackupView, error) {
	ctx, cancel := b.backupContext()
	defer cancel()
	if err := validateBackupPassphrase(input.Passphrase); err != nil {
		return EncryptedBackupView{}, err
	}

	path := strings.TrimSpace(input.Path)
	if path == "" {
		var err error
		path, err = b.chooseBackupDestination(ctx)
		if err != nil {
			return EncryptedBackupView{}, err
		}
		if path == "" {
			return EncryptedBackupView{}, errors.New("создание резервной копии отменено")
		}
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return EncryptedBackupView{}, fmt.Errorf("некорректный путь резервной копии: %w", err)
	}

	b.mu.RLock()
	database := b.database
	value := b.config
	paths := b.paths
	b.mu.RUnlock()
	options := backup.ExportOptions{ConfigMetadata: value}
	if input.IncludeBlobs {
		options.BlobDirectory = paths.BlobDirectory
	}
	manifest, err := backup.Export(ctx, database, path, input.Passphrase, options)
	if err != nil {
		return EncryptedBackupView{}, err
	}
	view, err := backupView(path, manifest)
	if err != nil {
		return EncryptedBackupView{}, err
	}
	b.recordBackupAudit(ctx, "backup.create", path, manifest)
	return view, nil
}

// ValidateEncryptedBackup authenticates and integrity-checks an archive. An
// empty path opens the native file dialog from this user gesture.
func (b *Bridge) ValidateEncryptedBackup(input EncryptedBackupInspectInput) (EncryptedBackupView, error) {
	ctx, cancel := b.backupContext()
	defer cancel()
	if err := validateBackupPassphrase(input.Passphrase); err != nil {
		return EncryptedBackupView{}, err
	}
	path, err := b.resolveBackupSource(ctx, input.Path)
	if err != nil {
		return EncryptedBackupView{}, err
	}
	manifest, err := backup.Validate(ctx, path, input.Passphrase, backup.RestoreOptions{})
	if err != nil {
		return EncryptedBackupView{}, err
	}
	view, err := backupView(path, manifest)
	if err != nil {
		return EncryptedBackupView{}, err
	}
	b.recordBackupAudit(ctx, "backup.validate", path, manifest)
	return view, nil
}

// RestoreEncryptedBackup restores into an owner-selected directory without
// modifying the active database. Activation remains a separate offline flow.
func (b *Bridge) RestoreEncryptedBackup(input EncryptedBackupRestoreInput) (EncryptedBackupView, error) {
	ctx, cancel := b.backupContext()
	defer cancel()
	if err := validateBackupPassphrase(input.Passphrase); err != nil {
		return EncryptedBackupView{}, err
	}
	archivePath, err := b.resolveBackupSource(ctx, input.Path)
	if err != nil {
		return EncryptedBackupView{}, err
	}
	target := strings.TrimSpace(input.TargetDirectory)
	if target == "" {
		if !b.hasAppContext() {
			return EncryptedBackupView{}, errors.New("директория восстановления обязательна вне Wails UI")
		}
		target, err = wailsruntime.OpenDirectoryDialog(ctx, wailsruntime.OpenDialogOptions{
			Title:                "Восстановить копию Yuri в отдельную директорию",
			CanCreateDirectories: true,
		})
		if err != nil {
			return EncryptedBackupView{}, fmt.Errorf("выбрать директорию восстановления: %w", err)
		}
		if target == "" {
			return EncryptedBackupView{}, errors.New("восстановление резервной копии отменено")
		}
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return EncryptedBackupView{}, fmt.Errorf("некорректная директория восстановления: %w", err)
	}

	b.mu.RLock()
	activeDatabasePath := b.paths.DatabaseFile
	b.mu.RUnlock()
	result, err := backup.Restore(ctx, archivePath, input.Passphrase, backup.RestoreOptions{
		TargetDir:          target,
		ActiveDatabasePath: activeDatabasePath,
	})
	if err != nil {
		return EncryptedBackupView{}, err
	}
	view, err := backupView(archivePath, result.Manifest)
	if err != nil {
		return EncryptedBackupView{}, err
	}
	view.RestoredTo = target
	b.recordBackupAudit(ctx, "backup.restore", archivePath, result.Manifest)
	return view, nil
}

func (b *Bridge) backupContext() (context.Context, context.CancelFunc) {
	b.mu.RLock()
	appContext := b.appCtx
	b.mu.RUnlock()
	if appContext == nil {
		appContext = context.Background()
	}
	return context.WithTimeout(appContext, backupOperationTimeout)
}

func (b *Bridge) hasAppContext() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.appCtx != nil
}

func (b *Bridge) chooseBackupDestination(ctx context.Context) (string, error) {
	if !b.hasAppContext() {
		return "", errors.New("путь резервной копии обязателен вне Wails UI")
	}
	return wailsruntime.SaveFileDialog(ctx, wailsruntime.SaveDialogOptions{
		Title:                "Создать зашифрованную копию Yuri",
		DefaultFilename:      "yuri-" + time.Now().Format("2006-01-02") + ".yuribackup",
		CanCreateDirectories: true,
		Filters:              []wailsruntime.FileFilter{{DisplayName: "Yuri encrypted backup (*.yuribackup)", Pattern: "*.yuribackup"}},
	})
}

func (b *Bridge) resolveBackupSource(ctx context.Context, rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		if !b.hasAppContext() {
			return "", errors.New("путь резервной копии обязателен вне Wails UI")
		}
		var err error
		path, err = wailsruntime.OpenFileDialog(ctx, wailsruntime.OpenDialogOptions{
			Title:   "Выбрать зашифрованную копию Yuri",
			Filters: []wailsruntime.FileFilter{{DisplayName: "Yuri encrypted backup (*.yuribackup)", Pattern: "*.yuribackup"}},
		})
		if err != nil {
			return "", fmt.Errorf("выбрать резервную копию: %w", err)
		}
		if path == "" {
			return "", errors.New("выбор резервной копии отменён")
		}
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("некорректный путь резервной копии: %w", err)
	}
	return path, nil
}

func backupView(path string, manifest backup.Manifest) (EncryptedBackupView, error) {
	info, err := os.Stat(path)
	if err != nil {
		return EncryptedBackupView{}, fmt.Errorf("прочитать сведения о резервной копии: %w", err)
	}
	return EncryptedBackupView{
		Path: path, CreatedAt: manifest.CreatedAt.UTC().Format(time.RFC3339Nano),
		SizeBytes: info.Size(), BlobCount: len(manifest.Blobs), HasConfig: manifest.Config != nil,
	}, nil
}

func validateBackupPassphrase(passphrase string) error {
	if len(passphrase) < 12 {
		return errors.New("пароль резервной копии должен содержать не менее 12 символов")
	}
	if len(passphrase) > 4096 {
		return errors.New("пароль резервной копии слишком длинный")
	}
	return nil
}

func (b *Bridge) recordBackupAudit(ctx context.Context, action, path string, manifest backup.Manifest) {
	if b.repositories == nil || b.repositories.Audit == nil {
		return
	}
	id, err := domain.NewID("audit")
	if err != nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"archive_name": filepath.Base(path),
		"blob_count":   len(manifest.Blobs),
		"has_config":   manifest.Config != nil,
		"format":       manifest.Format,
		"version":      manifest.Version,
	})
	err = b.repositories.Audit.Append(ctx, storage.AuditEvent{
		ID: id, Actor: domain.ActorUser, Action: action, Target: filepath.Base(path),
		Decision: domain.PermissionAllow, PayloadRedacted: string(payload), CreatedAt: time.Now().UTC(),
	})
	if err != nil && b.logger != nil {
		b.logger.ErrorContext(ctx, "append backup audit", "action", action, "error", err)
	}
}
