package backup

import (
	"errors"
	"fmt"
	"time"
)

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

// ValidateForRestore applies Validate and additionally requires a real salt.
//
// Validate has to tolerate an empty salt because the export path validates
// parameters before generating one (see encryptPayload). Restore has no
// generation step: whatever salt the envelope header carries is the salt
// scrypt receives. Without this check an attacker-authored header containing
// "salt": "" derives the key with no salt at all, so the same passphrase
// yields the same key for every such archive and per-archive key separation is
// lost. Callers that derive a key from archive-supplied parameters must use
// this method rather than Validate.
func (p KDFParams) ValidateForRestore() error {
	if err := p.Validate(); err != nil {
		return err
	}
	if len(p.Salt) < 16 || len(p.Salt) > 64 {
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
