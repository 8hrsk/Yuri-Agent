package desktop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/config"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func newBackupTestBridge(t *testing.T) *Bridge {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{
		ConfigDirectory: filepath.Join(root, "config"),
		ConfigFile:      filepath.Join(root, "config", "config.json"),
		DataDirectory:   filepath.Join(root, "data"),
	}.WithDataDirectory(filepath.Join(root, "data"))
	if err := os.MkdirAll(paths.BlobDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := storage.Open(context.Background(), paths.DatabaseFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatal(err)
	}
	value := config.Default(paths)
	value.Providers = []config.ProviderConfig{{
		ID: "main", Kind: config.ProviderOpenAICompatible, DisplayName: "Main",
		BaseURL: "https://api.example.com/v1", Model: "model", CredentialRef: "keyring:secret", Enabled: true,
	}}
	return &Bridge{database: database, repositories: repositories, paths: paths, config: value}
}

func TestEncryptedBackupBridgeRoundTripWithoutSecretLeak(t *testing.T) {
	bridge := newBackupTestBridge(t)
	archive := filepath.Join(t.TempDir(), "owner.yuribackup")
	const passphrase = "a sufficiently long backup password"

	created, err := bridge.CreateEncryptedBackup(EncryptedBackupInput{Path: archive, Passphrase: passphrase})
	if err != nil {
		t.Fatal(err)
	}
	if created.Path != archive || created.SizeBytes == 0 || !created.HasConfig {
		t.Fatalf("unexpected created backup: %#v", created)
	}
	validated, err := bridge.ValidateEncryptedBackup(EncryptedBackupInspectInput{Path: archive, Passphrase: passphrase})
	if err != nil {
		t.Fatal(err)
	}
	if validated.CreatedAt != created.CreatedAt || validated.SizeBytes != created.SizeBytes {
		t.Fatalf("validated backup mismatch: created=%#v validated=%#v", created, validated)
	}

	restoreRoot := filepath.Join(t.TempDir(), "restored")
	restored, err := bridge.RestoreEncryptedBackup(EncryptedBackupRestoreInput{
		Path: archive, TargetDirectory: restoreRoot, Passphrase: passphrase,
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.RestoredTo != restoreRoot {
		t.Fatalf("restore target = %q", restored.RestoredTo)
	}
	configBytes, err := os.ReadFile(filepath.Join(restoreRoot, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configBytes), "keyring:secret") || strings.Contains(string(configBytes), passphrase) {
		t.Fatalf("restored metadata contains credential material: %s", configBytes)
	}

	events, err := bridge.repositories.Audit.List(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("backup audit count = %d, want 3", len(events))
	}
	for _, event := range events {
		if strings.Contains(event.PayloadRedacted, passphrase) || strings.Contains(event.PayloadRedacted, filepath.Dir(archive)) {
			t.Fatalf("backup audit leaked secret/path: %#v", event)
		}
	}
}

func TestEncryptedBackupBridgeRequiresExplicitPathOutsideWails(t *testing.T) {
	bridge := newBackupTestBridge(t)
	_, err := bridge.CreateEncryptedBackup(EncryptedBackupInput{Passphrase: "a sufficiently long backup password"})
	if err == nil || !strings.Contains(err.Error(), "путь") {
		t.Fatalf("CreateEncryptedBackup() error = %v", err)
	}
}

func TestEncryptedBackupBridgeRejectsShortPassphrase(t *testing.T) {
	bridge := newBackupTestBridge(t)
	_, err := bridge.CreateEncryptedBackup(EncryptedBackupInput{
		Path: filepath.Join(t.TempDir(), "backup.yuribackup"), Passphrase: "short",
	})
	if err == nil || !strings.Contains(err.Error(), "12") {
		t.Fatalf("CreateEncryptedBackup() error = %v", err)
	}
}
