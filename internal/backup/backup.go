// Package backup provides an application/provider-neutral encrypted backup
// format for Yuri's local data.
//
// A backup contains a consistent SQLite snapshot, optional sanitized config
// metadata, and optional bounded blob files. The payload is a zip authenticated
// and encrypted with AES-256-GCM in STREAM framing: a sequence of frames, each
// bound to its index and to the archive header, with the final frame marked.
// Export and restore therefore run in memory proportional to one frame rather
// than to the archive (see docs/PERFORMANCE.md). Archives written before that
// change carry a single whole-payload seal and are still restored; see
// envelopeHeader and validateEnvelopeFraming for the version dispatch.
// Passwords are never persisted and keyring entries are deliberately not
// consulted by this package.
package backup

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Export creates an encrypted backup at destination. destination must be an
// absolute path. A SQLite consistent snapshot is produced with VACUUM INTO;
// database/WAL sidecar files are never copied. The final archive is installed
// with a same-directory temporary file and an atomic no-replace install. An
// existing destination is rejected and is never overwritten.
func Export(ctx context.Context, database *sql.DB, destination, passphrase string, options ExportOptions) (Manifest, error) {
	if err := validateContext(ctx); err != nil {
		return Manifest{}, err
	}
	if database == nil {
		return Manifest{}, fmt.Errorf("%w: nil database", ErrInvalidArchive)
	}
	if err := validatePassphrase(passphrase); err != nil {
		return Manifest{}, err
	}
	limits, err := options.Limits.withDefaults()
	if err != nil {
		return Manifest{}, err
	}
	destination, err = validateAbsolutePath(destination, limits.MaxPathBytes, "destination")
	if err != nil {
		return Manifest{}, err
	}
	if err := checkContext(ctx); err != nil {
		return Manifest{}, err
	}
	parent := filepath.Dir(destination)
	// Check before MkdirAll: a missing child must not be created through an
	// existing symlinked ancestor.
	if err := ensureNoSymlinkComponents(parent); err != nil {
		return Manifest{}, fmt.Errorf("validate backup directory: %w", err)
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("create backup directory: %w", err)
	}
	if err := ensureNoSymlinkComponents(parent); err != nil {
		return Manifest{}, fmt.Errorf("validate backup directory: %w", err)
	}

	snapshot, err := os.CreateTemp(parent, ".yuri-backup-db-*.tmp")
	if err != nil {
		return Manifest{}, fmt.Errorf("create snapshot temporary file: %w", err)
	}
	snapshotPath := snapshot.Name()
	if err := snapshot.Chmod(0o600); err != nil {
		snapshot.Close()
		os.Remove(snapshotPath)
		return Manifest{}, fmt.Errorf("set snapshot permissions: %w", err)
	}
	if err := snapshot.Close(); err != nil {
		os.Remove(snapshotPath)
		return Manifest{}, fmt.Errorf("close snapshot temporary file: %w", err)
	}
	if err := os.Remove(snapshotPath); err != nil {
		return Manifest{}, fmt.Errorf("prepare snapshot path: %w", err)
	}
	defer os.Remove(snapshotPath)

	if err := vacuumInto(ctx, database, snapshotPath); err != nil {
		return Manifest{}, err
	}
	// VACUUM INTO creates the destination itself, so apply owner-only mode to
	// the resulting snapshot before it is read into the encrypted payload.
	if err := os.Chmod(snapshotPath, 0o600); err != nil {
		return Manifest{}, fmt.Errorf("set snapshot permissions: %w", err)
	}
	databaseEntry, err := hashFile(ctx, snapshotPath, limits.MaxDatabaseBytes, limits.MaxPathBytes)
	if err != nil {
		return Manifest{}, fmt.Errorf("hash sqlite snapshot: %w", err)
	}
	if err := checkRegularOwnerFile(snapshotPath); err != nil {
		return Manifest{}, fmt.Errorf("validate sqlite snapshot: %w", err)
	}

	configBytes, err := options.configBytes(limits)
	if err != nil {
		return Manifest{}, err
	}
	var configEntry *FileEntry
	if len(configBytes) > 0 {
		entry := FileEntry{Path: "config.json", Size: int64(len(configBytes))}
		entry.SHA256 = digestBytes(configBytes)
		configEntry = &entry
	}

	blobs, err := collectBlobs(ctx, options, limits)
	if err != nil {
		return Manifest{}, err
	}
	databaseEntry.Path = "database.sqlite3"
	manifest := Manifest{
		Format: Format, Version: Version, CreatedAt: time.Now().UTC(),
		Database: databaseEntry, Config: configEntry,
	}
	for _, blob := range blobs {
		manifest.Blobs = append(manifest.Blobs, blob.Entry)
	}
	manifest.Files = manifestEntries(manifest)
	if err := manifest.Validate(limits); err != nil {
		return Manifest{}, err
	}

	if err := checkContext(ctx); err != nil {
		return Manifest{}, err
	}

	// The zip payload is piped straight through the frame sealer into the
	// destination temporary file. Nothing between the snapshot on disk and the
	// archive on disk is ever held whole in memory.
	err = atomicWriteStream(ctx, destination, 0o600, func(out io.Writer) error {
		return encryptPayloadStream(ctx, out, passphrase, options.KDF, limits, func(plaintext io.Writer) error {
			return writePayload(ctx, plaintext, snapshotPath, configBytes, blobs, manifest, limits)
		})
	})
	if err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Backup is an alias for Export for callers that use backup-oriented naming.
func Backup(ctx context.Context, database *sql.DB, destination, passphrase string, options ExportOptions) (Manifest, error) {
	return Export(ctx, database, destination, passphrase, options)
}

// Create is an alias for Export.
func Create(ctx context.Context, database *sql.DB, destination, passphrase string, options ExportOptions) (Manifest, error) {
	return Export(ctx, database, destination, passphrase, options)
}

// Validate decrypts, authenticates, parses, checksums, and integrity-checks a
// backup without writing its contents to an application target. It returns
// only the validated manifest.
func Validate(ctx context.Context, archivePath, passphrase string, options RestoreOptions) (Manifest, error) {
	decoded, err := decodeArchive(ctx, archivePath, passphrase, options.Limits, options.ConfigOptions)
	if err != nil {
		return Manifest{}, err
	}
	defer decoded.close()
	return decoded.Manifest, nil
}

// Inspect is an alias for Validate.
func Inspect(ctx context.Context, archivePath, passphrase string, options RestoreOptions) (Manifest, error) {
	return Validate(ctx, archivePath, passphrase, options)
}

// Restore decrypts and validates a backup, then writes only to absent files
// under the caller-provided target. Existing files, including an active DB,
// are never overwritten. All validation occurs before any target is touched.
func Restore(ctx context.Context, archivePath, passphrase string, options RestoreOptions) (RestoreResult, error) {
	decoded, err := decodeArchive(ctx, archivePath, passphrase, options.Limits, options.ConfigOptions)
	if err != nil {
		return RestoreResult{}, err
	}
	defer decoded.close()
	target, limits, err := resolveRestoreTarget(options, decoded.Manifest)
	if err != nil {
		return RestoreResult{}, err
	}
	if err := checkContext(ctx); err != nil {
		return RestoreResult{}, err
	}
	if target.Directory != "" {
		if info, statErr := os.Lstat(target.Directory); statErr == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return RestoreResult{}, fmt.Errorf("%w: target directory", ErrUnsafePath)
			}
		} else if errors.Is(statErr, os.ErrNotExist) {
			// Validate existing ancestors before creating missing target
			// components; otherwise MkdirAll could follow a symlink.
			if err := ensureNoSymlinkComponents(target.Directory); err != nil {
				return RestoreResult{}, fmt.Errorf("validate restore directory: %w", err)
			}
			if err := os.MkdirAll(target.Directory, 0o700); err != nil {
				return RestoreResult{}, fmt.Errorf("create restore directory: %w", err)
			}
		} else {
			return RestoreResult{}, fmt.Errorf("stat restore directory: %w", statErr)
		}
		if err := ensureNoSymlinkComponents(target.Directory); err != nil {
			return RestoreResult{}, fmt.Errorf("validate restore directory: %w", err)
		}
	}
	if err := preflightBlobTargets(target, decoded.Manifest, limits); err != nil {
		return RestoreResult{}, err
	}

	result := RestoreResult{Manifest: decoded.Manifest, ConfigMetadata: append([]byte(nil), decoded.Config...)}
	written := make([]string, 0, 2+len(decoded.Manifest.Blobs))
	cleanup := func() {
		for _, path := range written {
			_ = os.Remove(path)
		}
	}
	// Every member was already checksummed by decodeArchive, before this
	// function touched the target. The bytes are streamed out of the staged
	// payload here and re-verified on the way, so nothing larger than a copy
	// buffer is resident.
	if err := copyStagedFile(ctx, target.DatabasePath, decoded.databasePath, decoded.Manifest.Database, limits.MaxDatabaseBytes, limits.MaxPathBytes); err != nil {
		return RestoreResult{}, err
	}
	written = append(written, target.DatabasePath)
	result.DatabasePath = target.DatabasePath
	if decoded.Manifest.Config != nil {
		if err := writeNewFile(ctx, target.ConfigPath, decoded.Config, limits.MaxConfigBytes, limits.MaxPathBytes); err != nil {
			cleanup()
			return RestoreResult{}, err
		}
		written = append(written, target.ConfigPath)
		result.ConfigPath = target.ConfigPath
	}
	if len(decoded.Manifest.Blobs) > 0 {
		if err := os.MkdirAll(target.BlobDirectory, 0o700); err != nil {
			cleanup()
			return RestoreResult{}, fmt.Errorf("create restore blob directory: %w", err)
		}
		if err := ensureNoSymlinkComponents(target.BlobDirectory); err != nil {
			cleanup()
			return RestoreResult{}, fmt.Errorf("validate restore blob directory: %w", err)
		}
		payload, reader, openErr := decoded.openPayload()
		if openErr != nil {
			cleanup()
			return RestoreResult{}, openErr
		}
		paths, blobErr := restoreBlobs(ctx, reader, target, decoded.Manifest, limits, &written)
		payload.Close()
		if blobErr != nil {
			cleanup()
			return RestoreResult{}, blobErr
		}
		result.BlobPaths = paths
	}
	if err := checkContext(ctx); err != nil {
		cleanup()
		return RestoreResult{}, err
	}
	return result, nil
}

// restoreBlobs streams every manifest blob out of the staged payload. written
// accumulates published paths so the caller can roll back a partial restore.
func restoreBlobs(ctx context.Context, reader *zip.Reader, target RestoreTarget, manifest Manifest, limits Limits, written *[]string) ([]string, error) {
	members := make(map[string]*zip.File, len(reader.File))
	for _, file := range reader.File {
		members[file.Name] = file
	}
	paths := make([]string, 0, len(manifest.Blobs))
	for _, entry := range manifest.Blobs {
		member, ok := members[entry.Path]
		if !ok {
			return nil, fmt.Errorf("%w: missing entry %q", ErrInvalidArchive, entry.Path)
		}
		name := strings.TrimPrefix(entry.Path, "blobs/")
		path, pathErr := safeJoin(target.BlobDirectory, filepath.FromSlash(name), limits.MaxPathBytes)
		if pathErr != nil {
			return nil, pathErr
		}
		if err := ensureNoSymlinkComponents(filepath.Dir(path)); err != nil {
			return nil, fmt.Errorf("validate restore blob parent: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create restore blob parent: %w", err)
		}
		source, openErr := member.Open()
		if openErr != nil {
			return nil, fmt.Errorf("%w: open zip entry %q", ErrInvalidArchive, entry.Path)
		}
		err := writeNewFileStream(ctx, path, source, &entry, limits.MaxBlobBytes, limits.MaxPathBytes)
		source.Close()
		if err != nil {
			return nil, err
		}
		*written = append(*written, path)
		paths = append(paths, path)
	}
	return paths, nil
}

// copyStagedFile publishes an already-verified staged file at path, re-checking
// its digest as the bytes are copied.
func copyStagedFile(ctx context.Context, path, stagedPath string, expected FileEntry, max int64, maxPath int) error {
	source, _, err := openVerifiedRegular(stagedPath)
	if err != nil {
		return err
	}
	defer source.Close()
	return writeNewFileStream(ctx, path, source, &expected, max, maxPath)
}

// RestoreToTemp is a convenience wrapper for the common safe workflow of
// restoring into a caller-created temporary/output directory.
func RestoreToTemp(ctx context.Context, archivePath, passphrase, targetDir string, options RestoreOptions) (RestoreResult, error) {
	options.TargetDir = targetDir
	return Restore(ctx, archivePath, passphrase, options)
}
