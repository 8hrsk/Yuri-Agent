package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func manifestEntries(manifest Manifest) []FileEntry {
	entries := []FileEntry{manifest.Database}
	if manifest.Config != nil {
		entries = append(entries, *manifest.Config)
	}
	entries = append(entries, manifest.Blobs...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

// Validate checks public manifest invariants. It is useful to callers that
// receive a manifest from Export and to tests/fuzzers for untrusted manifests.
func (m Manifest) Validate(limits Limits) error {
	limits, err := limits.withDefaults()
	if err != nil {
		return err
	}
	if m.Format != Format || m.Version != Version || m.CreatedAt.IsZero() {
		return fmt.Errorf("%w: manifest format/version/timestamp", ErrInvalidArchive)
	}
	if err := validateEntry(m.Database, "database.sqlite3", limits.MaxDatabaseBytes, limits.MaxPathBytes); err != nil {
		return err
	}
	if m.Config != nil {
		if err := validateEntry(*m.Config, "config.json", limits.MaxConfigBytes, limits.MaxPathBytes); err != nil {
			return err
		}
	}
	if len(m.Blobs) > limits.MaxBlobs {
		return fmt.Errorf("%w: manifest blob count", ErrSizeLimit)
	}
	var total int64
	seen := make(map[string]struct{}, len(m.Blobs)+2)
	for _, entry := range m.Blobs {
		if err := validateEntry(entry, "", limits.MaxBlobBytes, limits.MaxPathBytes); err != nil {
			return err
		}
		if !strings.HasPrefix(entry.Path, "blobs/") {
			return fmt.Errorf("%w: blob path %q", ErrUnsafePath, entry.Path)
		}
		if _, exists := seen[entry.Path]; exists {
			return fmt.Errorf("%w: duplicate manifest path %q", ErrInvalidArchive, entry.Path)
		}
		seen[entry.Path] = struct{}{}
		if total > limits.MaxBlobTotalBytes-entry.Size {
			return fmt.Errorf("%w: manifest blob total", ErrSizeLimit)
		}
		total += entry.Size
	}
	expected := manifestEntries(m)
	if len(expected) != len(m.Files) {
		return fmt.Errorf("%w: manifest files list", ErrInvalidArchive)
	}
	for index := range expected {
		if expected[index] != m.Files[index] {
			return fmt.Errorf("%w: manifest files are not sorted or do not match", ErrInvalidArchive)
		}
	}
	return nil
}

func validateEntry(entry FileEntry, exactPath string, maxSize int64, maxPath int) error {
	if exactPath != "" && entry.Path != exactPath {
		return fmt.Errorf("%w: expected %q, got %q", ErrInvalidArchive, exactPath, entry.Path)
	}
	if err := validateArchiveName(entry.Path, maxPath); err != nil {
		return err
	}
	if entry.Size < 0 || entry.Size > maxSize {
		return fmt.Errorf("%w: entry %q", ErrSizeLimit, entry.Path)
	}
	if len(entry.SHA256) != sha256.Size*2 {
		return fmt.Errorf("%w: checksum for %q", ErrInvalidArchive, entry.Path)
	}
	if _, err := hex.DecodeString(entry.SHA256); err != nil {
		return fmt.Errorf("%w: checksum for %q", ErrInvalidArchive, entry.Path)
	}
	return nil
}

func resolveRestoreTarget(options RestoreOptions, manifest Manifest) (RestoreTarget, Limits, error) {
	limits, err := options.Limits.withDefaults()
	if err != nil {
		return RestoreTarget{}, Limits{}, err
	}
	target := options.Target
	if target.Directory == "" {
		target.Directory = options.TargetDir
	}
	if target.DatabasePath == "" {
		target.DatabasePath = options.DatabasePath
	}
	if target.ConfigPath == "" {
		target.ConfigPath = options.ConfigPath
	}
	if target.BlobDirectory == "" {
		target.BlobDirectory = options.BlobDirectory
	}
	if target.ActiveDatabasePath == "" {
		target.ActiveDatabasePath = options.ActiveDatabasePath
	}
	if target.Directory == "" && target.DatabasePath == "" {
		return RestoreTarget{}, Limits{}, fmt.Errorf("%w: restore target is required", ErrInvalidArchive)
	}
	if target.Directory != "" {
		target.Directory, err = validateAbsolutePath(target.Directory, limits.MaxPathBytes, "restore directory")
		if err != nil {
			return RestoreTarget{}, Limits{}, err
		}
	}
	if target.DatabasePath == "" {
		target.DatabasePath = filepath.Join(target.Directory, "database.sqlite3")
	}
	target.DatabasePath, err = validateAbsolutePath(target.DatabasePath, limits.MaxPathBytes, "restore database")
	if err != nil {
		return RestoreTarget{}, Limits{}, err
	}
	if target.ConfigPath == "" && manifest.Config != nil {
		target.ConfigPath = filepath.Join(target.Directory, "config.json")
	}
	if manifest.Config != nil {
		target.ConfigPath, err = validateAbsolutePath(target.ConfigPath, limits.MaxPathBytes, "restore config")
		if err != nil {
			return RestoreTarget{}, Limits{}, err
		}
	}
	if len(manifest.Blobs) > 0 {
		if target.BlobDirectory == "" {
			target.BlobDirectory = filepath.Join(target.Directory, "blobs")
		}
		target.BlobDirectory, err = validateAbsolutePath(target.BlobDirectory, limits.MaxPathBytes, "restore blob directory")
		if err != nil {
			return RestoreTarget{}, Limits{}, err
		}
	}
	if target.ActiveDatabasePath != "" {
		active, activeErr := validateAbsolutePath(target.ActiveDatabasePath, limits.MaxPathBytes, "active database")
		if activeErr != nil {
			return RestoreTarget{}, Limits{}, activeErr
		}
		if samePath(active, target.DatabasePath) {
			return RestoreTarget{}, Limits{}, fmt.Errorf("%w: active database", ErrTargetExists)
		}
	}
	for _, path := range []string{target.DatabasePath, target.ConfigPath} {
		if path == "" {
			continue
		}
		if info, statErr := os.Lstat(path); statErr == nil {
			_ = info
			return RestoreTarget{}, Limits{}, fmt.Errorf("%w: %s", ErrTargetExists, path)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return RestoreTarget{}, Limits{}, fmt.Errorf("stat restore target %q: %w", path, statErr)
		}
	}
	return target, limits, nil
}

func preflightBlobTargets(target RestoreTarget, manifest Manifest, limits Limits) error {
	if len(manifest.Blobs) == 0 {
		return nil
	}
	if info, statErr := os.Lstat(target.BlobDirectory); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: restore blob directory", ErrUnsafePath)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("stat restore blob directory: %w", statErr)
	}
	if err := ensureNoSymlinkComponents(target.BlobDirectory); err != nil {
		return fmt.Errorf("validate restore blob directory: %w", err)
	}
	for _, entry := range manifest.Blobs {
		name := strings.TrimPrefix(entry.Path, "blobs/")
		path, err := safeJoin(target.BlobDirectory, filepath.FromSlash(name), limits.MaxPathBytes)
		if err != nil {
			return err
		}
		if err := ensureNoSymlinkComponents(filepath.Dir(path)); err != nil {
			return fmt.Errorf("validate restore blob parent: %w", err)
		}
		if _, statErr := os.Lstat(path); statErr == nil {
			return fmt.Errorf("%w: %s", ErrTargetExists, path)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("stat restore blob target: %w", statErr)
		}
	}
	return nil
}
