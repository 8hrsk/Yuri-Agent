package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestExportValidateAndRestoreRoundTrip(t *testing.T) {
	database, databasePath := testDatabase(t)
	defer database.Close()
	if _, err := database.Exec("CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO notes(body) VALUES ('consistent snapshot')"); err != nil {
		t.Fatal(err)
	}

	blobPath := filepath.Join(t.TempDir(), "blob.bin")
	if err := os.WriteFile(blobPath, []byte("blob payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "nested", "backup.ybk")
	config := []byte(`{"version":1,"providers":[{"id":"main","credential_ref":"keyring-ref","binary":"/tmp/unsafe-provider","model":"safe-model","api_key":"sk-test"}],"locale":"ru-RU"}`)
	manifest, err := Export(context.Background(), database, archivePath, "correct horse battery staple", ExportOptions{
		ConfigMetadata: config,
		Blobs:          []Blob{{Name: "note.bin", Source: blobPath}},
	})
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if manifest.Format != Format || manifest.Version != Version || manifest.Database.Path != "database.sqlite3" {
		t.Fatalf("manifest = %#v", manifest)
	}
	if len(manifest.Blobs) != 1 || manifest.Config == nil {
		t.Fatalf("manifest optional entries = %#v", manifest)
	}
	if info, err := os.Stat(archivePath); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("archive permissions = %o", info.Mode().Perm())
	}
	beforeArchive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Export(context.Background(), database, archivePath, "another passphrase", ExportOptions{}); !errors.Is(err, ErrTargetExists) {
		t.Fatalf("Export() existing destination error = %v", err)
	}
	afterArchive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeArchive) != string(afterArchive) {
		t.Fatal("Export() changed an existing destination")
	}

	validated, err := Validate(context.Background(), archivePath, "correct horse battery staple", RestoreOptions{})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if validated.Database.SHA256 != manifest.Database.SHA256 {
		t.Fatalf("validated database checksum = %q, want %q", validated.Database.SHA256, manifest.Database.SHA256)
	}

	target := filepath.Join(t.TempDir(), "restore")
	result, err := RestoreToTemp(context.Background(), archivePath, "correct horse battery staple", target, RestoreOptions{})
	if err != nil {
		t.Fatalf("RestoreToTemp() error = %v", err)
	}
	if result.DatabasePath != filepath.Join(target, "database.sqlite3") || result.ConfigPath == "" || len(result.BlobPaths) != 1 {
		t.Fatalf("restore result = %#v", result)
	}
	if info, err := os.Stat(result.DatabasePath); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("restored database permissions = %o", info.Mode().Perm())
	}
	if strings.Contains(string(result.ConfigMetadata), "credential_ref") || strings.Contains(string(result.ConfigMetadata), "keyring-ref") || strings.Contains(string(result.ConfigMetadata), "api_key") || strings.Contains(string(result.ConfigMetadata), "sk-test") || strings.Contains(string(result.ConfigMetadata), "unsafe-provider") || strings.Contains(string(result.ConfigMetadata), `"binary"`) {
		t.Fatalf("sanitized config contains secret/unsafe provider fields: %s", result.ConfigMetadata)
	}
	var count int
	restored, err := sql.Open("sqlite", sqliteDSN(result.DatabasePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.QueryRow("SELECT count(*) FROM notes").Scan(&count); err != nil {
		t.Fatalf("query restored DB: %v", err)
	}
	restored.Close()
	if count != 1 {
		t.Fatalf("restored row count = %d", count)
	}
	_ = databasePath
}

func TestWrongPassphraseAndTamperAreAuthenticated(t *testing.T) {
	database, _ := testDatabase(t)
	defer database.Close()
	archivePath := filepath.Join(t.TempDir(), "backup.ybk")
	if _, err := Export(context.Background(), database, archivePath, "right", ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(context.Background(), archivePath, "wrong", RestoreOptions{}); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase error = %v", err)
	}
	content, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	content[len(content)-1] ^= 0x40
	if err := os.WriteFile(archivePath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(context.Background(), archivePath, "right", RestoreOptions{}); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("tamper error = %v", err)
	}
}

func TestCancellationLeavesNoArchive(t *testing.T) {
	database, _ := testDatabase(t)
	defer database.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	archivePath := filepath.Join(t.TempDir(), "cancelled.ybk")
	if _, err := Export(ctx, database, archivePath, "right", ExportOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Export() error = %v", err)
	}
	if _, err := os.Stat(archivePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled export archive stat = %v", err)
	}
}

func TestAtomicWritersPreserveExistingDestination(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "existing.backup")
	original := []byte("keep this archive")
	if err := os.WriteFile(destination, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(context.Background(), destination, []byte("replacement"), 0o600); !errors.Is(err, ErrTargetExists) {
		t.Fatalf("atomicWrite() error = %v", err)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(original) {
		t.Fatalf("atomicWrite() changed existing destination to %q", content)
	}

	restoreDestination := filepath.Join(directory, "existing.restore")
	if err := os.WriteFile(restoreDestination, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeNewFile(context.Background(), restoreDestination, []byte("replacement"), 1<<20, DefaultMaxPathBytes); !errors.Is(err, ErrTargetExists) {
		t.Fatalf("writeNewFile() error = %v", err)
	}
	content, err = os.ReadFile(restoreDestination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(original) {
		t.Fatalf("writeNewFile() changed existing destination to %q", content)
	}
}

func TestAtomicWritersRejectSymlinkedAncestorsBeforeCreation(t *testing.T) {
	directory := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(directory, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	destination := filepath.Join(link, "created.backup")
	if err := atomicWrite(context.Background(), destination, []byte("must not write"), 0o600); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("atomicWrite() symlink ancestor error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "created.backup")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink ancestor destination stat = %v", err)
	}
	restoreDestination := filepath.Join(link, "nested", "restored.sqlite3")
	if err := writeNewFile(context.Background(), restoreDestination, []byte("must not restore"), 1<<20, DefaultMaxPathBytes); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("writeNewFile() symlink ancestor error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink ancestor restore directory stat = %v", err)
	}
}

func TestRestoreNeverOverwritesActiveDatabase(t *testing.T) {
	database, activePath := testDatabase(t)
	defer database.Close()
	if _, err := database.Exec("CREATE TABLE keep (value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO keep(value) VALUES ('active')"); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "backup.ybk")
	if _, err := Export(context.Background(), database, archivePath, "right", ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Restore(context.Background(), archivePath, "right", RestoreOptions{
		Target: RestoreTarget{DatabasePath: activePath, ActiveDatabasePath: activePath},
	})
	if !errors.Is(err, ErrTargetExists) {
		t.Fatalf("restore active database error = %v", err)
	}
	after, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("active database changed after rejected restore")
	}
}

func TestManifestRejectsChecksumAndUnsafeBlob(t *testing.T) {
	manifest := Manifest{Format: Format, Version: Version, Database: FileEntry{Path: "database.sqlite3", Size: 1, SHA256: strings.Repeat("0", 63)}, Files: []FileEntry{{Path: "database.sqlite3", Size: 1, SHA256: strings.Repeat("0", 63)}}}
	if err := manifest.Validate(DefaultLimits()); err == nil {
		t.Fatal("manifest with short checksum was accepted")
	}
	if _, err := validateBlobName("../escape", DefaultMaxPathBytes); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("unsafe blob path error = %v", err)
	}
}

func TestSanitizeConfigMetadataRemovesSecretFields(t *testing.T) {
	input := []byte(`{"CredentialRef":"ref","API-Key":"key","provider":{"Binary":"relative","model":"m"},"nested":[{"access_token":"token","ok":true}]}`)
	output, err := SanitizeConfigMetadata(input)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(output, &value); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSanitizedConfigMetadata(output, DefaultMaxConfigBytes); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(output), "ref") || strings.Contains(string(output), "key") || strings.Contains(string(output), "Binary") || strings.Contains(string(output), "token") {
		t.Fatalf("sanitized output = %s", output)
	}
}

func testDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "active.sqlite3")
	database, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Ping(); err != nil {
		database.Close()
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	return database, path
}

func sqliteDSN(path string) string {
	urlPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		urlPath = "/" + urlPath
	}
	return (&url.URL{Scheme: "file", Path: urlPath}).String()
}
