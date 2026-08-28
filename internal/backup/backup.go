// Package backup provides an application/provider-neutral encrypted backup
// format for Yuri's local data.
//
// A backup contains a consistent SQLite snapshot, optional sanitized config
// metadata, and optional bounded blob files. The payload is authenticated and
// encrypted as a whole with AES-256-GCM. Passwords are never persisted and
// keyring entries are deliberately not consulted by this package.
package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
	_ "modernc.org/sqlite"
)

const (
	// Format identifies the current on-disk envelope and manifest format.
	Format = "yuri-encrypted-backup"
	// Version is the payload/envelope version. Unsupported versions are
	// rejected rather than guessed at during restore.
	Version = 1

	// The defaults are deliberately finite. Callers can lower them for a
	// narrower import/export boundary, but cannot raise them above the hard
	// implementation bounds.
	DefaultMaxArchiveBytes   int64 = 256 << 20
	DefaultMaxPlaintextBytes int64 = 256 << 20
	DefaultMaxDatabaseBytes  int64 = 128 << 20
	DefaultMaxConfigBytes    int64 = 1 << 20
	DefaultMaxBlobBytes      int64 = 16 << 20
	DefaultMaxBlobTotalBytes int64 = 128 << 20
	DefaultMaxBlobs                = 4096
	DefaultMaxPathBytes            = 4096
	DefaultMaxManifestBytes  int64 = 1 << 20

	hardMaxArchiveBytes   int64 = 1 << 30
	hardMaxPlaintextBytes int64 = 1 << 30
	hardMaxDatabaseBytes  int64 = 512 << 20
	hardMaxConfigBytes    int64 = 16 << 20
	hardMaxBlobBytes      int64 = 128 << 20
	hardMaxBlobTotalBytes int64 = 512 << 20
	hardMaxBlobs                = 16384
	hardMaxPathBytes            = 16 << 10

	maxEnvelopeHeaderBytes = 64 << 10
	maxPassphraseBytes     = 4096
	maxEntryNameBytes      = 1024
	copyBufferBytes        = 32 << 10
)

var envelopeMagic = [8]byte{'Y', 'U', 'R', 'I', 'B', 'K', 'P', '1'}

var (
	// ErrInvalidArchive indicates malformed, unsupported, or unsafe archive
	// structure. It is intentionally separate from authentication failures.
	ErrInvalidArchive = errors.New("backup: invalid archive")
	// ErrWrongPassphrase is returned when the passphrase cannot authenticate the
	// encrypted payload. A modified ciphertext produces the same result.
	ErrWrongPassphrase = errors.New("backup: wrong passphrase")
	// ErrChecksumMismatch indicates a validly decrypted payload whose declared
	// file checksum did not match its contents.
	ErrChecksumMismatch = errors.New("backup: checksum mismatch")
	// ErrSizeLimit indicates that a configured or hard size bound was exceeded.
	ErrSizeLimit = errors.New("backup: size limit exceeded")
	// ErrUnsafePath indicates a path or archive member that escapes its scope,
	// is a symlink, or otherwise cannot be safely handled.
	ErrUnsafePath = errors.New("backup: unsafe path")
	// ErrTargetExists prevents restore from replacing an existing file. In
	// particular, restore can never overwrite an active database by accident.
	ErrTargetExists = errors.New("backup: restore target already exists")
)

// Limits bounds every externally supplied or archive-derived size. Zero
// values are filled from Defaults; negative values are rejected.
type Limits struct {
	MaxArchiveBytes   int64
	MaxPlaintextBytes int64
	MaxDatabaseBytes  int64
	MaxConfigBytes    int64
	MaxBlobBytes      int64
	MaxBlobTotalBytes int64
	MaxBlobs          int
	MaxPathBytes      int
	MaxManifestBytes  int64
}

// DefaultLimits returns conservative limits suitable for local backups.
func DefaultLimits() Limits {
	return Limits{
		MaxArchiveBytes: DefaultMaxArchiveBytes, MaxPlaintextBytes: DefaultMaxPlaintextBytes,
		MaxDatabaseBytes: DefaultMaxDatabaseBytes, MaxConfigBytes: DefaultMaxConfigBytes,
		MaxBlobBytes: DefaultMaxBlobBytes, MaxBlobTotalBytes: DefaultMaxBlobTotalBytes,
		MaxBlobs: DefaultMaxBlobs, MaxPathBytes: DefaultMaxPathBytes,
		MaxManifestBytes: DefaultMaxManifestBytes,
	}
}

func (l Limits) withDefaults() (Limits, error) {
	d := DefaultLimits()
	if l.MaxArchiveBytes == 0 {
		l.MaxArchiveBytes = d.MaxArchiveBytes
	}
	if l.MaxPlaintextBytes == 0 {
		l.MaxPlaintextBytes = d.MaxPlaintextBytes
	}
	if l.MaxDatabaseBytes == 0 {
		l.MaxDatabaseBytes = d.MaxDatabaseBytes
	}
	if l.MaxConfigBytes == 0 {
		l.MaxConfigBytes = d.MaxConfigBytes
	}
	if l.MaxBlobBytes == 0 {
		l.MaxBlobBytes = d.MaxBlobBytes
	}
	if l.MaxBlobTotalBytes == 0 {
		l.MaxBlobTotalBytes = d.MaxBlobTotalBytes
	}
	if l.MaxBlobs == 0 {
		l.MaxBlobs = d.MaxBlobs
	}
	if l.MaxPathBytes == 0 {
		l.MaxPathBytes = d.MaxPathBytes
	}
	if l.MaxManifestBytes == 0 {
		l.MaxManifestBytes = d.MaxManifestBytes
	}
	if l.MaxArchiveBytes < 1 || l.MaxArchiveBytes > hardMaxArchiveBytes ||
		l.MaxPlaintextBytes < 1 || l.MaxPlaintextBytes > hardMaxPlaintextBytes ||
		l.MaxDatabaseBytes < 1 || l.MaxDatabaseBytes > hardMaxDatabaseBytes ||
		l.MaxConfigBytes < 1 || l.MaxConfigBytes > hardMaxConfigBytes ||
		l.MaxBlobBytes < 1 || l.MaxBlobBytes > hardMaxBlobBytes ||
		l.MaxBlobTotalBytes < 1 || l.MaxBlobTotalBytes > hardMaxBlobTotalBytes ||
		l.MaxBlobs < 1 || l.MaxBlobs > hardMaxBlobs ||
		l.MaxPathBytes < 1 || l.MaxPathBytes > hardMaxPathBytes ||
		l.MaxManifestBytes < 1 || l.MaxManifestBytes > l.MaxPlaintextBytes || l.MaxManifestBytes > hardMaxConfigBytes {
		return Limits{}, fmt.Errorf("%w: invalid limits", ErrSizeLimit)
	}
	if l.MaxPlaintextBytes > l.MaxArchiveBytes {
		return Limits{}, fmt.Errorf("%w: plaintext exceeds archive limit", ErrSizeLimit)
	}
	return l, nil
}

// KDFParams configures scrypt. Salt is generated when empty. Values are
// validated against bounded memory/work limits before scrypt is invoked.
type KDFParams struct {
	N    int    `json:"n"`
	R    int    `json:"r"`
	P    int    `json:"p"`
	Salt []byte `json:"-"`
}

// DefaultKDFParams returns the package's password-hardening defaults. The
// salt is intentionally empty so each export receives fresh random salt.
func DefaultKDFParams() KDFParams { return KDFParams{N: 1 << 15, R: 8, P: 1} }

func (p KDFParams) withDefaults() KDFParams {
	d := DefaultKDFParams()
	if p.N == 0 {
		p.N = d.N
	}
	if p.R == 0 {
		p.R = d.R
	}
	if p.P == 0 {
		p.P = d.P
	}
	return p
}

// Validate verifies scrypt's power-of-two N and bounded resource use. Salt
// must be generated/provided separately; an empty salt is accepted here.
func (p KDFParams) Validate() error {
	if p.N < 1<<14 || p.N > 1<<20 || p.N&(p.N-1) != 0 {
		return fmt.Errorf("%w: scrypt N must be a power of two in [16384,1048576]", ErrInvalidArchive)
	}
	if p.R < 1 || p.R > 32 {
		return fmt.Errorf("%w: scrypt r is outside [1,32]", ErrInvalidArchive)
	}
	if p.P < 1 || p.P > 16 {
		return fmt.Errorf("%w: scrypt p is outside [1,16]", ErrInvalidArchive)
	}
	// scrypt allocates approximately 128*N*r bytes. Keep this under 256 MiB
	// and protect the multiplication from integer overflow.
	if int64(p.N) > (256<<20)/(128*int64(p.R)) {
		return fmt.Errorf("%w: scrypt memory cost is too high", ErrInvalidArchive)
	}
	if int64(p.N) > (1<<28)/(int64(p.R)*int64(p.P)) {
		return fmt.Errorf("%w: scrypt work cost is too high", ErrInvalidArchive)
	}
	if len(p.Salt) != 0 && (len(p.Salt) < 16 || len(p.Salt) > 64) {
		return fmt.Errorf("%w: scrypt salt must be 16..64 bytes", ErrInvalidArchive)
	}
	return nil
}

// FileEntry is a manifest entry. Path is an archive-relative slash path,
// Size is the exact uncompressed size, and SHA256 is lowercase hexadecimal.
type FileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest authenticates the logical contents of a backup. Database is
// required; Config and Blobs are optional. Files repeats the complete sorted
// list to make generic manifest consumers straightforward.
type Manifest struct {
	Format    string      `json:"format"`
	Version   int         `json:"version"`
	CreatedAt time.Time   `json:"created_at"`
	Database  FileEntry   `json:"database"`
	Config    *FileEntry  `json:"config,omitempty"`
	Blobs     []FileEntry `json:"blobs,omitempty"`
	Files     []FileEntry `json:"files"`
}

// Blob identifies a caller-selected regular file to include. Name is the
// archive-relative name below "blobs/"; an empty Name uses the source
// basename. Source paths must be absolute and must not be symlinks.
type Blob struct {
	Name   string `json:"name"`
	Source string `json:"-"`
	// Path is an alias for Source for callers that model files as paths.
	Path string `json:"-"`
}

// ConfigMetadataOptions controls the only potentially executable config
// metadata field. By default provider Binary fields are removed. A binary is
// retained only when its existing regular file is beneath one of these
// explicitly supplied, non-symlink roots and is not group/world writable.
type ConfigMetadataOptions struct {
	AllowedBinaryRoots []string
}

// ExportOptions controls optional payload material and resource bounds.
// ConfigMetadata must be JSON metadata, never a keyring lookup or secret.
type ExportOptions struct {
	// ConfigMetadata accepts JSON []byte/json.RawMessage or any value that
	// encoding/json can marshal. It is sanitized before it enters the payload.
	ConfigMetadata any
	// Config is an alias accepted for callers that use the shorter name. When
	// both fields are set ConfigMetadata takes precedence.
	Config any
	Blobs  []Blob
	// BlobDirectory includes all regular, non-symlink files below this
	// directory when Blobs is empty. It is optional; no blobs are exported by
	// default.
	BlobDirectory string
	KDF           KDFParams
	Limits        Limits
	ConfigOptions ConfigMetadataOptions
}

// Options is kept as a concise spelling for ExportOptions.
type Options = ExportOptions

// RestoreTarget describes where validated data may be materialized. Every
// destination file must be absent; restore never replaces an existing file.
type RestoreTarget struct {
	Directory          string
	DatabasePath       string
	ConfigPath         string
	BlobDirectory      string
	ActiveDatabasePath string
}

// RestoreOptions controls archive validation and optional materialization.
// Target is optional for Validate; Restore requires either Target.Directory or
// Target.DatabasePath. Flattened fields are aliases for compatibility with
// callers that do not want to construct RestoreTarget.
type RestoreOptions struct {
	Target RestoreTarget

	TargetDir          string
	DatabasePath       string
	ConfigPath         string
	BlobDirectory      string
	ActiveDatabasePath string

	Limits        Limits
	ConfigOptions ConfigMetadataOptions
}

// RestoreResult describes files written by Restore and exposes sanitized
// config metadata without loading it into provider-specific types.
type RestoreResult struct {
	Manifest       Manifest
	DatabasePath   string
	ConfigPath     string
	BlobPaths      []string
	ConfigMetadata []byte
}

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

	payload, err := buildPayload(ctx, snapshotPath, configBytes, blobs, manifest, limits)
	if err != nil {
		return Manifest{}, err
	}
	if err := checkContext(ctx); err != nil {
		return Manifest{}, err
	}

	archiveBytes, err := encryptPayload(ctx, payload, passphrase, options.KDF, limits)
	if err != nil {
		return Manifest{}, err
	}
	if err := atomicWrite(ctx, destination, archiveBytes, 0o600); err != nil {
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
	written := make([]string, 0, 2+len(decoded.Blobs))
	cleanup := func() {
		for _, path := range written {
			_ = os.Remove(path)
		}
	}
	if err := writeNewFile(ctx, target.DatabasePath, decoded.Database, limits.MaxDatabaseBytes, limits.MaxPathBytes); err != nil {
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
	if len(decoded.Blobs) > 0 {
		if err := os.MkdirAll(target.BlobDirectory, 0o700); err != nil {
			cleanup()
			return RestoreResult{}, fmt.Errorf("create restore blob directory: %w", err)
		}
		if err := ensureNoSymlinkComponents(target.BlobDirectory); err != nil {
			cleanup()
			return RestoreResult{}, fmt.Errorf("validate restore blob directory: %w", err)
		}
		for _, blob := range decoded.Blobs {
			name := strings.TrimPrefix(blob.Entry.Path, "blobs/")
			path, pathErr := safeJoin(target.BlobDirectory, filepath.FromSlash(name), limits.MaxPathBytes)
			if pathErr != nil {
				cleanup()
				return RestoreResult{}, pathErr
			}
			if err := ensureNoSymlinkComponents(filepath.Dir(path)); err != nil {
				cleanup()
				return RestoreResult{}, fmt.Errorf("validate restore blob parent: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				cleanup()
				return RestoreResult{}, fmt.Errorf("create restore blob parent: %w", err)
			}
			if err := writeNewFile(ctx, path, blob.Data, limits.MaxBlobBytes, limits.MaxPathBytes); err != nil {
				cleanup()
				return RestoreResult{}, err
			}
			written = append(written, path)
			result.BlobPaths = append(result.BlobPaths, path)
		}
	}
	if err := checkContext(ctx); err != nil {
		cleanup()
		return RestoreResult{}, err
	}
	return result, nil
}

// RestoreToTemp is a convenience wrapper for the common safe workflow of
// restoring into a caller-created temporary/output directory.
func RestoreToTemp(ctx context.Context, archivePath, passphrase, targetDir string, options RestoreOptions) (RestoreResult, error) {
	options.TargetDir = targetDir
	return Restore(ctx, archivePath, passphrase, options)
}

type blobData struct {
	Input Blob
	Entry FileEntry
}

type decodedArchive struct {
	Manifest Manifest
	Database []byte
	Config   []byte
	Blobs    []decodedBlob
}

type decodedBlob struct {
	Entry FileEntry
	Data  []byte
}

func (o ExportOptions) configBytes(limits Limits) ([]byte, error) {
	input := o.ConfigMetadata
	if input == nil {
		input = o.Config
	}
	if input == nil {
		return nil, nil
	}
	content, err := configInputBytes(input)
	if err != nil {
		return nil, err
	}
	if len(content) == 0 {
		return nil, nil
	}
	if int64(len(content)) > limits.MaxConfigBytes {
		return nil, fmt.Errorf("%w: config metadata", ErrSizeLimit)
	}
	output, err := SanitizeConfigMetadataWithOptions(content, o.ConfigOptions, limits.MaxConfigBytes)
	if err != nil {
		return nil, err
	}
	return output, nil
}

func configInputBytes(value any) ([]byte, error) {
	switch value := value.(type) {
	case []byte:
		return append([]byte(nil), value...), nil
	case json.RawMessage:
		return append([]byte(nil), value...), nil
	default:
		content, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode config metadata: %w", err)
		}
		return content, nil
	}
}

func collectBlobs(ctx context.Context, options ExportOptions, limits Limits) ([]blobData, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	inputs := append([]Blob(nil), options.Blobs...)
	if len(inputs) == 0 && options.BlobDirectory != "" {
		root, err := validateAbsolutePath(options.BlobDirectory, limits.MaxPathBytes, "blob directory")
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(root)
		if err != nil {
			return nil, fmt.Errorf("stat blob directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: blob directory", ErrUnsafePath)
		}
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := checkContext(ctx); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: blob symlink %q", ErrUnsafePath, path)
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			inputs = append(inputs, Blob{Name: filepath.ToSlash(rel), Source: path})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk blob directory: %w", err)
		}
	}
	if len(inputs) > limits.MaxBlobs {
		return nil, fmt.Errorf("%w: too many blobs", ErrSizeLimit)
	}
	result := make([]blobData, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	var total int64
	for index, input := range inputs {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		source := input.Source
		if source == "" {
			source = input.Path
		}
		path, err := validateAbsolutePath(source, limits.MaxPathBytes, fmt.Sprintf("blob %d source", index))
		if err != nil {
			return nil, err
		}
		if err := checkRegularFile(path); err != nil {
			return nil, fmt.Errorf("blob %q: %w", path, err)
		}
		name := input.Name
		if name == "" {
			name = filepath.Base(path)
		}
		archivePath, err := validateBlobName(name, limits.MaxPathBytes)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[archivePath]; exists {
			return nil, fmt.Errorf("%w: duplicate blob %q", ErrInvalidArchive, archivePath)
		}
		seen[archivePath] = struct{}{}
		entry, err := hashFile(ctx, path, limits.MaxBlobBytes, limits.MaxPathBytes)
		if err != nil {
			return nil, fmt.Errorf("hash blob %q: %w", path, err)
		}
		entry.Path = "blobs/" + archivePath
		if total > limits.MaxBlobTotalBytes-entry.Size {
			return nil, fmt.Errorf("%w: blob total", ErrSizeLimit)
		}
		total += entry.Size
		result = append(result, blobData{Input: Blob{Name: archivePath, Source: path}, Entry: entry})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Entry.Path < result[j].Entry.Path })
	return result, nil
}

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

func buildPayload(ctx context.Context, snapshotPath string, config []byte, blobs []blobData, manifest Manifest, limits Limits) ([]byte, error) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	if err := writeZipBytes(ctx, writer, "manifest.json", mustMarshalManifest(manifest), limits.MaxManifestBytes); err != nil {
		writer.Close()
		return nil, err
	}
	if err := writeZipFile(ctx, writer, "database.sqlite3", snapshotPath, manifest.Database, limits.MaxDatabaseBytes); err != nil {
		writer.Close()
		return nil, err
	}
	if len(config) > 0 {
		if err := writeZipBytes(ctx, writer, "config.json", config, limits.MaxConfigBytes); err != nil {
			writer.Close()
			return nil, err
		}
	}
	for _, blob := range blobs {
		if err := writeZipFile(ctx, writer, blob.Entry.Path, blob.Input.Source, blob.Entry, limits.MaxBlobBytes); err != nil {
			writer.Close()
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close backup payload: %w", err)
	}
	if int64(buffer.Len()) > limits.MaxPlaintextBytes {
		return nil, fmt.Errorf("%w: backup payload", ErrSizeLimit)
	}
	return buffer.Bytes(), nil
}

func mustMarshalManifest(manifest Manifest) []byte {
	content, _ := json.Marshal(manifest)
	return content
}

func writeZipBytes(ctx context.Context, writer *zip.Writer, name string, content []byte, max int64) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if int64(len(content)) > max {
		return fmt.Errorf("%w: zip entry %q", ErrSizeLimit, name)
	}
	header := &zip.FileHeader{Name: name, Method: zip.Store}
	header.SetMode(0o600)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create zip entry %q: %w", name, err)
	}
	if _, err := writeContext(ctx, entry, bytes.NewReader(content), max); err != nil {
		return fmt.Errorf("write zip entry %q: %w", name, err)
	}
	return nil
}

func writeZipFile(ctx context.Context, writer *zip.Writer, name, path string, expected FileEntry, max int64) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	header := &zip.FileHeader{Name: name, Method: zip.Store}
	header.SetMode(0o600)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create zip entry %q: %w", name, err)
	}
	file, _, err := openVerifiedRegular(path)
	if err != nil {
		return fmt.Errorf("open zip source %q: %w", path, err)
	}
	defer file.Close()
	digest := sha256.New()
	size, err := writeContext(ctx, io.MultiWriter(entry, digest), file, max)
	if err != nil {
		return fmt.Errorf("write zip entry %q: %w", name, err)
	}
	if expected.Size < 0 || expected.Size != size || hex.EncodeToString(digest.Sum(nil)) != strings.ToLower(expected.SHA256) {
		return fmt.Errorf("%w: source changed while exporting %q", ErrChecksumMismatch, name)
	}
	return nil
}

func encryptPayload(ctx context.Context, payload []byte, passphrase string, params KDFParams, limits Limits) ([]byte, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	params = params.withDefaults()
	if err := params.Validate(); err != nil {
		return nil, err
	}
	if len(params.Salt) == 0 {
		params.Salt = make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, params.Salt); err != nil {
			return nil, fmt.Errorf("generate backup salt: %w", err)
		}
	}
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate backup nonce: %w", err)
	}
	key, err := deriveKey(ctx, passphrase, params)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES-256 cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	header := envelopeHeader{
		Format: Format, Version: Version, Cipher: "AES-256-GCM", KDF: "scrypt",
		N: params.N, R: params.R, P: params.P,
		Salt:          base64.RawStdEncoding.EncodeToString(params.Salt),
		Nonce:         base64.RawStdEncoding.EncodeToString(nonce),
		PlaintextSize: int64(len(payload)), CiphertextSize: int64(len(payload) + gcm.Overhead()),
	}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return nil, fmt.Errorf("encode backup header: %w", err)
	}
	if len(headerBytes) > maxEnvelopeHeaderBytes {
		return nil, fmt.Errorf("%w: envelope header", ErrSizeLimit)
	}
	if int64(len(payload))+int64(gcm.Overhead())+int64(len(headerBytes))+int64(len(envelopeMagic))+4 > limits.MaxArchiveBytes {
		return nil, fmt.Errorf("%w: encrypted archive", ErrSizeLimit)
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	associated := make([]byte, 0, len(envelopeMagic)+len(headerBytes))
	associated = append(associated, envelopeMagic[:]...)
	associated = append(associated, headerBytes...)
	ciphertext := gcm.Seal(nil, nonce, payload, associated)
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	output.Grow(len(envelopeMagic) + 4 + len(headerBytes) + len(ciphertext))
	output.Write(envelopeMagic[:])
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(headerBytes)))
	output.Write(size[:])
	output.Write(headerBytes)
	output.Write(ciphertext)
	return output.Bytes(), nil
}

type envelopeHeader struct {
	Format         string `json:"format"`
	Version        int    `json:"version"`
	Cipher         string `json:"cipher"`
	KDF            string `json:"kdf"`
	N              int    `json:"n"`
	R              int    `json:"r"`
	P              int    `json:"p"`
	Salt           string `json:"salt"`
	Nonce          string `json:"nonce"`
	PlaintextSize  int64  `json:"plaintext_size"`
	CiphertextSize int64  `json:"ciphertext_size"`
}

func deriveKey(ctx context.Context, passphrase string, params KDFParams) ([]byte, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	password := []byte(passphrase)
	defer zeroBytes(password)
	salt := append([]byte(nil), params.Salt...)
	defer zeroBytes(salt)
	// scrypt.Key has no cancellation hook. Keep it synchronous so a canceled
	// call cannot leave an expensive worker running after the API returns.
	key, err := scrypt.Key(password, salt, params.N, params.R, params.P, 32)
	if err != nil {
		return nil, fmt.Errorf("%w: derive key: %v", ErrInvalidArchive, err)
	}
	if err := checkContext(ctx); err != nil {
		zeroBytes(key)
		return nil, err
	}
	return key, nil
}

func decodeArchive(ctx context.Context, archivePath, passphrase string, rawLimits Limits, configOptions ConfigMetadataOptions) (decodedArchive, error) {
	if err := validateContext(ctx); err != nil {
		return decodedArchive{}, err
	}
	if err := validatePassphrase(passphrase); err != nil {
		return decodedArchive{}, err
	}
	limits, err := rawLimits.withDefaults()
	if err != nil {
		return decodedArchive{}, err
	}
	archivePath, err = validateAbsolutePath(archivePath, limits.MaxPathBytes, "archive")
	if err != nil {
		return decodedArchive{}, err
	}
	content, err := readFileBounded(ctx, archivePath, limits.MaxArchiveBytes, limits.MaxPathBytes)
	if err != nil {
		return decodedArchive{}, fmt.Errorf("read backup: %w", err)
	}
	if len(content) < len(envelopeMagic)+4 || !bytes.Equal(content[:len(envelopeMagic)], envelopeMagic[:]) {
		return decodedArchive{}, fmt.Errorf("%w: envelope magic", ErrInvalidArchive)
	}
	headerSize := binary.BigEndian.Uint32(content[len(envelopeMagic) : len(envelopeMagic)+4])
	if headerSize == 0 || headerSize > maxEnvelopeHeaderBytes {
		return decodedArchive{}, fmt.Errorf("%w: envelope header size", ErrInvalidArchive)
	}
	headerStart := len(envelopeMagic) + 4
	headerEnd := headerStart + int(headerSize)
	if headerEnd < headerStart || headerEnd > len(content) {
		return decodedArchive{}, fmt.Errorf("%w: truncated envelope header", ErrInvalidArchive)
	}
	var header envelopeHeader
	decoder := json.NewDecoder(bytes.NewReader(content[headerStart:headerEnd]))
	if err := decoder.Decode(&header); err != nil {
		return decodedArchive{}, fmt.Errorf("%w: decode envelope header", ErrInvalidArchive)
	}
	var trailingHeader any
	if err := decoder.Decode(&trailingHeader); err != io.EOF {
		return decodedArchive{}, fmt.Errorf("%w: trailing envelope header data", ErrInvalidArchive)
	}
	if header.Format != Format || header.Version != Version || header.Cipher != "AES-256-GCM" || header.KDF != "scrypt" {
		return decodedArchive{}, fmt.Errorf("%w: unsupported envelope", ErrInvalidArchive)
	}
	params := KDFParams{N: header.N, R: header.R, P: header.P}
	if params.N == 0 || params.R == 0 || params.P == 0 {
		return decodedArchive{}, fmt.Errorf("%w: missing envelope KDF parameters", ErrInvalidArchive)
	}
	params.Salt, err = base64.RawStdEncoding.DecodeString(header.Salt)
	if err != nil {
		return decodedArchive{}, fmt.Errorf("%w: envelope salt", ErrInvalidArchive)
	}
	if err := params.Validate(); err != nil {
		return decodedArchive{}, err
	}
	nonce, err := base64.RawStdEncoding.DecodeString(header.Nonce)
	if err != nil || len(nonce) != 12 {
		return decodedArchive{}, fmt.Errorf("%w: envelope nonce", ErrInvalidArchive)
	}
	if header.PlaintextSize < 0 || header.PlaintextSize > limits.MaxPlaintextBytes ||
		header.CiphertextSize < 0 || header.CiphertextSize != header.PlaintextSize+16 ||
		header.CiphertextSize > limits.MaxArchiveBytes {
		return decodedArchive{}, fmt.Errorf("%w: envelope payload size", ErrSizeLimit)
	}
	if int64(len(content)-headerEnd) != header.CiphertextSize {
		return decodedArchive{}, fmt.Errorf("%w: envelope trailing/truncated bytes", ErrInvalidArchive)
	}
	key, err := deriveKey(ctx, passphrase, params)
	if err != nil {
		return decodedArchive{}, err
	}
	defer zeroBytes(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return decodedArchive{}, fmt.Errorf("%w: AES cipher", ErrInvalidArchive)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return decodedArchive{}, fmt.Errorf("%w: GCM", ErrInvalidArchive)
	}
	associated := make([]byte, 0, len(envelopeMagic)+int(headerSize))
	associated = append(associated, envelopeMagic[:]...)
	associated = append(associated, content[headerStart:headerEnd]...)
	payload, err := gcm.Open(nil, nonce, content[headerEnd:], associated)
	if err != nil {
		return decodedArchive{}, fmt.Errorf("%w: %v", ErrWrongPassphrase, err)
	}
	if int64(len(payload)) != header.PlaintextSize {
		return decodedArchive{}, fmt.Errorf("%w: plaintext size", ErrInvalidArchive)
	}
	return parsePayload(ctx, payload, limits, configOptions)
}

func parsePayload(ctx context.Context, payload []byte, limits Limits, configOptions ConfigMetadataOptions) (decodedArchive, error) {
	if err := checkContext(ctx); err != nil {
		return decodedArchive{}, err
	}
	reader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return decodedArchive{}, fmt.Errorf("%w: zip payload: %v", ErrInvalidArchive, err)
	}
	if len(reader.File) < 2 || len(reader.File) > limits.MaxBlobs+3 {
		return decodedArchive{}, fmt.Errorf("%w: zip entry count", ErrSizeLimit)
	}
	entries := make(map[string]*zip.File, len(reader.File))
	var declared int64
	for _, file := range reader.File {
		if err := checkContext(ctx); err != nil {
			return decodedArchive{}, err
		}
		if err := validateArchiveName(file.Name, limits.MaxPathBytes); err != nil {
			return decodedArchive{}, err
		}
		if _, exists := entries[file.Name]; exists {
			return decodedArchive{}, fmt.Errorf("%w: duplicate entry %q", ErrInvalidArchive, file.Name)
		}
		if file.Name != "manifest.json" && file.Name != "database.sqlite3" && file.Name != "config.json" && !strings.HasPrefix(file.Name, "blobs/") {
			return decodedArchive{}, fmt.Errorf("%w: unexpected entry %q", ErrInvalidArchive, file.Name)
		}
		if file.Mode()&os.ModeSymlink != 0 || file.FileInfo().IsDir() {
			return decodedArchive{}, fmt.Errorf("%w: archive member %q", ErrUnsafePath, file.Name)
		}
		if file.UncompressedSize64 > uint64(limits.MaxPlaintextBytes) || file.UncompressedSize64 > uint64(^uint64(0)>>1) {
			return decodedArchive{}, fmt.Errorf("%w: archive member %q", ErrSizeLimit, file.Name)
		}
		if declared > limits.MaxPlaintextBytes-int64(file.UncompressedSize64) {
			return decodedArchive{}, fmt.Errorf("%w: archive uncompressed total", ErrSizeLimit)
		}
		declared += int64(file.UncompressedSize64)
		entries[file.Name] = file
	}
	manifestFile, exists := entries["manifest.json"]
	if !exists {
		return decodedArchive{}, fmt.Errorf("%w: manifest is missing", ErrInvalidArchive)
	}
	manifestBytes, err := readZipFile(ctx, manifestFile, limits.MaxManifestBytes)
	if err != nil {
		return decodedArchive{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return decodedArchive{}, fmt.Errorf("%w: decode manifest", ErrInvalidArchive)
	}
	if err := manifest.Validate(limits); err != nil {
		return decodedArchive{}, err
	}
	if len(entries) != len(manifest.Files)+1 {
		return decodedArchive{}, fmt.Errorf("%w: archive/manifest entry mismatch", ErrInvalidArchive)
	}
	for _, entry := range manifest.Files {
		file, ok := entries[entry.Path]
		if !ok {
			return decodedArchive{}, fmt.Errorf("%w: missing entry %q", ErrInvalidArchive, entry.Path)
		}
		if int64(file.UncompressedSize64) != entry.Size {
			return decodedArchive{}, fmt.Errorf("%w: size for %q", ErrChecksumMismatch, entry.Path)
		}
	}
	databaseFile, ok := entries[manifest.Database.Path]
	if !ok {
		return decodedArchive{}, fmt.Errorf("%w: database entry", ErrInvalidArchive)
	}
	database, err := readZipFile(ctx, databaseFile, limits.MaxDatabaseBytes)
	if err != nil {
		return decodedArchive{}, err
	}
	if err := verifyChecksum(database, manifest.Database); err != nil {
		return decodedArchive{}, err
	}
	if err := validateSQLiteBytes(ctx, database); err != nil {
		return decodedArchive{}, err
	}
	decoded := decodedArchive{Manifest: manifest, Database: database}
	if manifest.Config != nil {
		configFile := entries[manifest.Config.Path]
		config, readErr := readZipFile(ctx, configFile, limits.MaxConfigBytes)
		if readErr != nil {
			return decodedArchive{}, readErr
		}
		if err := verifyChecksum(config, *manifest.Config); err != nil {
			return decodedArchive{}, err
		}
		if err := ValidateSanitizedConfigMetadataWithOptions(config, configOptions, limits.MaxConfigBytes); err != nil {
			return decodedArchive{}, err
		}
		decoded.Config = config
	}
	for _, entry := range manifest.Blobs {
		file := entries[entry.Path]
		data, readErr := readZipFile(ctx, file, limits.MaxBlobBytes)
		if readErr != nil {
			return decodedArchive{}, readErr
		}
		if err := verifyChecksum(data, entry); err != nil {
			return decodedArchive{}, err
		}
		decoded.Blobs = append(decoded.Blobs, decodedBlob{Entry: entry, Data: data})
	}
	return decoded, nil
}

func readZipFile(ctx context.Context, file *zip.File, max int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(max) || file.UncompressedSize64 > uint64(^uint64(0)>>1) {
		return nil, fmt.Errorf("%w: zip entry %q", ErrSizeLimit, file.Name)
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: open zip entry %q", ErrInvalidArchive, file.Name)
	}
	defer reader.Close()
	data, err := readContext(ctx, reader, max)
	if err != nil {
		return nil, fmt.Errorf("read zip entry %q: %w", file.Name, err)
	}
	if int64(len(data)) != int64(file.UncompressedSize64) {
		return nil, fmt.Errorf("%w: zip entry %q size", ErrInvalidArchive, file.Name)
	}
	return data, nil
}

func verifyChecksum(content []byte, entry FileEntry) error {
	if int64(len(content)) != entry.Size || digestBytes(content) != strings.ToLower(entry.SHA256) {
		return fmt.Errorf("%w: %s", ErrChecksumMismatch, entry.Path)
	}
	return nil
}

func validateSQLiteBytes(ctx context.Context, content []byte) error {
	if len(content) < 100 || string(content[:16]) != "SQLite format 3\x00" {
		return fmt.Errorf("%w: SQLite header", ErrInvalidArchive)
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	temporary, err := os.CreateTemp("", ".yuri-backup-validate-*.sqlite3")
	if err != nil {
		return fmt.Errorf("create sqlite validation file: %w", err)
	}
	path := temporary.Name()
	defer os.Remove(path)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("chmod sqlite validation file: %w", err)
	}
	if _, err := writeContext(ctx, temporary, bytes.NewReader(content), int64(len(content))); err != nil {
		temporary.Close()
		return fmt.Errorf("write sqlite validation file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync sqlite validation file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close sqlite validation file: %w", err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open restored sqlite validation file: %w", err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	var result string
	if err := database.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("check restored sqlite integrity: %w", err)
	}
	if !strings.EqualFold(result, "ok") {
		return fmt.Errorf("%w: sqlite integrity check returned %q", ErrChecksumMismatch, result)
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

func writeNewFile(ctx context.Context, path string, content []byte, max int64, maxPath int) error {
	if _, err := validateAbsolutePath(path, maxPath, "restore output"); err != nil {
		return err
	}
	if int64(len(content)) > max {
		return fmt.Errorf("%w: restore output", ErrSizeLimit)
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	if err := ensureNoSymlinkComponents(parent); err != nil {
		return fmt.Errorf("validate restore output directory: %w", err)
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create restore output directory: %w", err)
	}
	if err := ensureNoSymlinkComponents(parent); err != nil {
		return fmt.Errorf("validate restore output directory: %w", err)
	}
	temporary, err := os.CreateTemp(parent, ".yuri-restore-*.tmp")
	if err != nil {
		return fmt.Errorf("create restore output temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("chmod restore output: %w", err)
	}
	if _, err := writeContext(ctx, temporary, bytes.NewReader(content), int64(max)); err != nil {
		temporary.Close()
		return fmt.Errorf("write restore output: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync restore output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close restore output: %w", err)
	}
	if err := installNoReplace(temporaryPath, path); err != nil {
		return fmt.Errorf("install restore output: %w", err)
	}
	return nil
}

func atomicWrite(ctx context.Context, destination string, content []byte, mode os.FileMode) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	parent := filepath.Dir(destination)
	if err := ensureNoSymlinkComponents(parent); err != nil {
		return fmt.Errorf("validate backup directory: %w", err)
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("%w: %s", ErrTargetExists, destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat backup destination: %w", err)
	}
	temporary, err := os.CreateTemp(parent, ".yuri-backup-*.tmp")
	if err != nil {
		return fmt.Errorf("create backup temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return fmt.Errorf("chmod backup temporary file: %w", err)
	}
	if _, err := writeContext(ctx, temporary, bytes.NewReader(content), int64(len(content))); err != nil {
		temporary.Close()
		return fmt.Errorf("write backup temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync backup temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close backup temporary file: %w", err)
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := installNoReplace(temporaryPath, destination); err != nil {
		return fmt.Errorf("install backup atomically: %w", err)
	}
	return nil
}

// installNoReplace atomically publishes a same-directory temporary file only
// when destination does not yet exist. A hard link is used instead of the
// replace-prone os.Rename, closing the check/install TOCTOU window. The caller
// owns cleanup of temporaryPath after this call.
func installNoReplace(temporaryPath, destination string) error {
	if err := os.Link(temporaryPath, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", ErrTargetExists, destination)
		}
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		// The destination link is already installed. The caller's deferred
		// cleanup gets another chance to unlink the private temporary name; do
		// not report a failed operation after the output was published.
		return nil
	}
	return nil
}

func vacuumInto(ctx context.Context, database *sql.DB, destination string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	// VACUUM INTO asks SQLite to construct a consistent database image. It is
	// intentionally used instead of copying the main file or WAL sidecars.
	if _, err := database.ExecContext(ctx, "VACUUM INTO ?", destination); err != nil {
		return fmt.Errorf("create consistent sqlite snapshot: %w", err)
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	return nil
}

func hashFile(ctx context.Context, path string, max int64, maxPath int) (FileEntry, error) {
	if _, err := validateAbsolutePath(path, maxPath, "file"); err != nil {
		return FileEntry{}, err
	}
	file, _, err := openVerifiedRegular(path)
	if err != nil {
		return FileEntry{}, err
	}
	defer file.Close()
	digest := sha256.New()
	size, err := writeContext(ctx, digest, file, max)
	if err != nil {
		return FileEntry{}, err
	}
	return FileEntry{Size: size, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

func writeContext(ctx context.Context, dst io.Writer, src io.Reader, max int64) (int64, error) {
	if max < 0 {
		return 0, fmt.Errorf("%w: negative copy limit", ErrSizeLimit)
	}
	buffer := make([]byte, copyBufferBytes)
	var total int64
	for {
		if err := checkContext(ctx); err != nil {
			return total, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			if total > max-int64(n) {
				return total, fmt.Errorf("%w", ErrSizeLimit)
			}
			written := 0
			for written < n {
				count, writeErr := dst.Write(buffer[written:n])
				if count > 0 {
					written += count
					total += int64(count)
				}
				if writeErr != nil {
					return total, writeErr
				}
				if count == 0 {
					return total, io.ErrShortWrite
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

func readContext(ctx context.Context, src io.Reader, max int64) ([]byte, error) {
	if max < 0 || max > int64(int(^uint(0)>>1)) {
		return nil, fmt.Errorf("%w", ErrSizeLimit)
	}
	var buffer bytes.Buffer
	buffer.Grow(minInt64(max, 64<<10))
	if _, err := writeContext(ctx, &buffer, src, max); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func readFileBounded(ctx context.Context, path string, max int64, maxPath int) ([]byte, error) {
	if _, err := validateAbsolutePath(path, maxPath, "file"); err != nil {
		return nil, err
	}
	file, info, err := openVerifiedRegular(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() > max {
		return nil, fmt.Errorf("%w", ErrSizeLimit)
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	return readContext(ctx, file, max)
}

func checkRegularOwnerFile(path string) error {
	if err := checkRegularFile(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	// Source files may be readable by the owner only. We don't silently copy a
	// group/world-readable source into a backup whose caller expects privacy.
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: source file is not owner-only", ErrUnsafePath)
	}
	return nil
}

func checkRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: expected regular file", ErrUnsafePath)
	}
	return nil
}

// openVerifiedRegular rejects symlinks and verifies that the opened handle is
// the same regular file observed by Lstat. This closes the common
// check-then-open swap window without relying on platform-specific O_NOFOLLOW.
func openVerifiedRegular(path string) (*os.File, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("%w: expected regular file", ErrUnsafePath)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	after, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		file.Close()
		return nil, nil, fmt.Errorf("%w: file changed while opening", ErrUnsafePath)
	}
	return file, after, nil
}

func validateAbsolutePath(path string, max int, label string) (string, error) {
	if path == "" || len(path) > max || !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: %s must be an absolute bounded path", ErrUnsafePath, label)
	}
	cleaned := filepath.Clean(path)
	if cleaned == string(filepath.Separator) || cleaned == "." {
		return "", fmt.Errorf("%w: invalid %s", ErrUnsafePath, label)
	}
	return cleaned, nil
}

func validateArchiveName(name string, max int) error {
	if name == "" || len(name) > max || strings.ContainsRune(name, '\x00') || strings.ContainsRune(name, '\\') || filepath.IsAbs(filepath.FromSlash(name)) {
		return fmt.Errorf("%w: archive name %q", ErrUnsafePath, name)
	}
	cleaned := pathCleanSlash(name)
	if cleaned != name || cleaned == "." || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return fmt.Errorf("%w: archive name %q", ErrUnsafePath, name)
	}
	return nil
}

func validateBlobName(name string, max int) (string, error) {
	name = filepath.ToSlash(strings.TrimSpace(name))
	if err := validateArchiveName(name, max); err != nil {
		return "", err
	}
	if name == "manifest.json" || name == "database.sqlite3" || name == "config.json" || strings.HasPrefix(name, "blobs/") {
		return "", fmt.Errorf("%w: reserved blob name %q", ErrUnsafePath, name)
	}
	return name, nil
}

func pathCleanSlash(value string) string {
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return ".."
		}
	}
	return strings.Join(parts, "/")
}

func safeJoin(root, relative string, max int) (string, error) {
	if filepath.IsAbs(relative) || relative == "." || strings.Contains(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return "", fmt.Errorf("%w: relative path %q", ErrUnsafePath, relative)
	}
	joined := filepath.Join(root, relative)
	if len(joined) > max {
		return "", fmt.Errorf("%w: path length", ErrSizeLimit)
	}
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: relative path %q", ErrUnsafePath, relative)
	}
	return joined, nil
}

// ensureNoSymlinkComponents prevents a caller-controlled restore/output path
// from redirecting a temporary file through a symlinked parent. Missing
// components are allowed because the caller may be creating a new target;
// MkdirAll is always followed by this check before any bytes are written.
func ensureNoSymlinkComponents(path string) error {
	cleaned := filepath.Clean(path)
	volume := filepath.VolumeName(cleaned)
	remainder := strings.TrimPrefix(cleaned, volume)
	current := volume
	if strings.HasPrefix(remainder, string(filepath.Separator)) {
		current += string(filepath.Separator)
		remainder = strings.TrimPrefix(remainder, string(filepath.Separator))
	}
	for _, component := range strings.Split(remainder, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// macOS exposes /var as a stable compatibility symlink to
			// /private/var, which includes the standard testing/temp roots.
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if current == string(filepath.Separator)+"var" && resolveErr == nil && filepath.Clean(resolved) == string(filepath.Separator)+"private"+string(filepath.Separator)+"var" {
				continue
			}
			return fmt.Errorf("%w: symlink component %q", ErrUnsafePath, current)
		}
	}
	return nil
}

func samePath(first, second string) bool {
	first, second = filepath.Clean(first), filepath.Clean(second)
	if first == second {
		return true
	}
	firstResolved, firstErr := filepath.EvalSymlinks(first)
	secondResolved, secondErr := filepath.EvalSymlinks(second)
	return firstErr == nil && secondErr == nil && filepath.Clean(firstResolved) == filepath.Clean(secondResolved)
}

func validatePassphrase(passphrase string) error {
	if len(passphrase) == 0 || len(passphrase) > maxPassphraseBytes {
		return fmt.Errorf("%w: passphrase must be 1..%d bytes", ErrInvalidArchive, maxPassphraseBytes)
	}
	return nil
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidArchive)
	}
	return checkContext(ctx)
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidArchive)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func minInt64(value int64, limit int) int {
	if value < int64(limit) {
		return int(value)
	}
	return limit
}

func digestBytes(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
