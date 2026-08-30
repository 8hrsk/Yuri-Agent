package backup

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	_ "modernc.org/sqlite"
)

// decodeArchive authenticates an archive and validates every member without
// writing anything the caller can see. The decrypted payload and the verified
// SQLite snapshot are staged in private temporary files; the returned value owns
// them and every caller must close it.
func decodeArchive(ctx context.Context, archivePath, passphrase string, rawLimits Limits, configOptions ConfigMetadataOptions) (*decodedArchive, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	if err := validatePassphrase(passphrase); err != nil {
		return nil, err
	}
	limits, err := rawLimits.withDefaults()
	if err != nil {
		return nil, err
	}
	archivePath, err = validateAbsolutePath(archivePath, limits.MaxPathBytes, "archive")
	if err != nil {
		return nil, err
	}
	archive, info, err := openVerifiedRegular(archivePath)
	if err != nil {
		return nil, fmt.Errorf("read backup: %w", err)
	}
	defer archive.Close()
	if info.Size() > limits.MaxArchiveBytes {
		return nil, fmt.Errorf("%w: encrypted archive", ErrSizeLimit)
	}
	source := bufio.NewReaderSize(archive, 64<<10)

	header, headerBytes, err := readEnvelopeHeader(source)
	if err != nil {
		return nil, err
	}
	if header.Format != Format || header.Cipher != "AES-256-GCM" || header.KDF != "scrypt" {
		return nil, fmt.Errorf("%w: unsupported envelope", ErrInvalidArchive)
	}
	if header.Version != envelopeVersionSealed && header.Version != envelopeVersionChunked {
		return nil, fmt.Errorf("%w: unsupported envelope version %d", ErrInvalidArchive, header.Version)
	}
	params := KDFParams{N: header.N, R: header.R, P: header.P}
	if params.N == 0 || params.R == 0 || params.P == 0 {
		return nil, fmt.Errorf("%w: missing envelope KDF parameters", ErrInvalidArchive)
	}
	params.Salt, err = base64.RawStdEncoding.DecodeString(header.Salt)
	if err != nil {
		return nil, fmt.Errorf("%w: envelope salt", ErrInvalidArchive)
	}
	// ValidateForRestore, not Validate: the envelope salt is attacker-supplied
	// and there is no generate-on-empty step on this path.
	if err := params.ValidateForRestore(); err != nil {
		return nil, err
	}
	nonce, err := base64.RawStdEncoding.DecodeString(header.Nonce)
	if err != nil || len(nonce) != gcmNonceBytes {
		return nil, fmt.Errorf("%w: envelope nonce", ErrInvalidArchive)
	}
	bodySize := info.Size() - int64(len(envelopeMagic)) - 4 - int64(len(headerBytes))
	if bodySize < gcmTagBytes {
		return nil, fmt.Errorf("%w: truncated envelope body", ErrInvalidArchive)
	}
	if err := validateEnvelopeFraming(header, bodySize, limits); err != nil {
		return nil, err
	}

	key, err := deriveKey(ctx, passphrase, params)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(key)
	gcm, err := newGCM(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArchive, err)
	}
	associated := make([]byte, 0, len(envelopeMagic)+len(headerBytes))
	associated = append(associated, envelopeMagic[:]...)
	associated = append(associated, headerBytes...)

	payload, err := createPrivateTemp(".yuri-backup-payload-*.tmp")
	if err != nil {
		return nil, err
	}
	decoded := &decodedArchive{payloadPath: payload.Name(), temporaries: []string{payload.Name()}}
	defer func() {
		if err != nil {
			payload.Close()
			decoded.close()
		}
	}()

	buffered := bufio.NewWriterSize(payload, 64<<10)
	if header.Version == envelopeVersionChunked {
		_, err = openChunkStream(ctx, source, buffered, gcm, nonce, associated, header.ChunkSize, limits.MaxPlaintextBytes)
	} else {
		err = openSealedEnvelope(ctx, source, buffered, gcm, nonce, associated, header, limits)
	}
	if err != nil {
		return nil, err
	}
	if err = buffered.Flush(); err != nil {
		return nil, fmt.Errorf("write decrypted payload: %w", err)
	}
	if err = payload.Sync(); err != nil {
		return nil, fmt.Errorf("sync decrypted payload: %w", err)
	}
	if err = payload.Close(); err != nil {
		return nil, fmt.Errorf("close decrypted payload: %w", err)
	}
	if err = parsePayloadFile(ctx, decoded, limits, configOptions); err != nil {
		return nil, err
	}
	return decoded, nil
}

// readEnvelopeHeader consumes the cleartext prologue and returns the header
// together with its exact bytes, which are the AEAD associated data.
func readEnvelopeHeader(source io.Reader) (envelopeHeader, []byte, error) {
	prologue := make([]byte, len(envelopeMagic)+4)
	if _, err := io.ReadFull(source, prologue); err != nil {
		return envelopeHeader{}, nil, fmt.Errorf("%w: envelope magic", ErrInvalidArchive)
	}
	if !bytes.Equal(prologue[:len(envelopeMagic)], envelopeMagic[:]) {
		return envelopeHeader{}, nil, fmt.Errorf("%w: envelope magic", ErrInvalidArchive)
	}
	headerSize := binary.BigEndian.Uint32(prologue[len(envelopeMagic):])
	if headerSize == 0 || headerSize > maxEnvelopeHeaderBytes {
		return envelopeHeader{}, nil, fmt.Errorf("%w: envelope header size", ErrInvalidArchive)
	}
	headerBytes := make([]byte, headerSize)
	if _, err := io.ReadFull(source, headerBytes); err != nil {
		return envelopeHeader{}, nil, fmt.Errorf("%w: truncated envelope header", ErrInvalidArchive)
	}
	var header envelopeHeader
	decoder := json.NewDecoder(bytes.NewReader(headerBytes))
	if err := decoder.Decode(&header); err != nil {
		return envelopeHeader{}, nil, fmt.Errorf("%w: decode envelope header", ErrInvalidArchive)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return envelopeHeader{}, nil, fmt.Errorf("%w: trailing envelope header data", ErrInvalidArchive)
	}
	return header, headerBytes, nil
}

// validateEnvelopeFraming applies the per-version rules that decide how the
// body is read. The two shapes are kept mutually exclusive so a v1 archive can
// never be coaxed into the framed reader or the reverse.
func validateEnvelopeFraming(header envelopeHeader, bodySize int64, limits Limits) error {
	switch header.Version {
	case envelopeVersionSealed:
		if header.Framing != "" || header.ChunkSize != 0 {
			return fmt.Errorf("%w: sealed envelope declares framing", ErrInvalidArchive)
		}
		if header.PlaintextSize < 0 || header.PlaintextSize > limits.MaxPlaintextBytes ||
			header.CiphertextSize < 0 || header.CiphertextSize != header.PlaintextSize+gcmTagBytes ||
			header.CiphertextSize > limits.MaxArchiveBytes {
			return fmt.Errorf("%w: envelope payload size", ErrSizeLimit)
		}
		if bodySize != header.CiphertextSize {
			return fmt.Errorf("%w: envelope trailing/truncated bytes", ErrInvalidArchive)
		}
	case envelopeVersionChunked:
		if header.Framing != framingSTREAM {
			return fmt.Errorf("%w: unsupported envelope framing %q", ErrInvalidArchive, header.Framing)
		}
		if header.ChunkSize < minChunkPlaintextBytes || header.ChunkSize > maxChunkPlaintextBytes {
			return fmt.Errorf("%w: envelope chunk size", ErrInvalidArchive)
		}
		// A framed envelope does not know its payload size when the header is
		// authenticated, so these fields must stay absent rather than carry an
		// unverifiable claim.
		if header.PlaintextSize != 0 || header.CiphertextSize != 0 {
			return fmt.Errorf("%w: framed envelope declares payload size", ErrInvalidArchive)
		}
	default:
		return fmt.Errorf("%w: unsupported envelope version %d", ErrInvalidArchive, header.Version)
	}
	return nil
}

// openSealedEnvelope reads a version 1 body, which is one AES-256-GCM
// ciphertext and therefore has to be authenticated as a whole. Archives written
// before the framed format still restore through here; the whole-payload memory
// cost is inherent to the format and is bounded by MaxPlaintextBytes.
func openSealedEnvelope(ctx context.Context, source io.Reader, destination io.Writer, gcm cipher.AEAD, nonce, associated []byte, header envelopeHeader, limits Limits) error {
	ciphertext, err := readContext(ctx, io.LimitReader(source, header.CiphertextSize), header.CiphertextSize)
	if err != nil {
		return fmt.Errorf("read backup: %w", err)
	}
	if int64(len(ciphertext)) != header.CiphertextSize {
		return fmt.Errorf("%w: envelope trailing/truncated bytes", ErrInvalidArchive)
	}
	payload, err := gcm.Open(nil, nonce, ciphertext, associated)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWrongPassphrase, err)
	}
	if int64(len(payload)) != header.PlaintextSize {
		return fmt.Errorf("%w: plaintext size", ErrInvalidArchive)
	}
	if int64(len(payload)) > limits.MaxPlaintextBytes {
		return fmt.Errorf("%w: decrypted payload", ErrSizeLimit)
	}
	if _, err := writeContext(ctx, destination, bytes.NewReader(payload), int64(len(payload))); err != nil {
		return fmt.Errorf("write decrypted payload: %w", err)
	}
	return nil
}

// parsePayloadFile validates the decrypted zip without materializing it. Every
// member is streamed twice at most: once here to prove its checksum, and once
// more by Restore to write it out. Only the manifest and the config metadata,
// both bounded by small limits, are held in memory.
func parsePayloadFile(ctx context.Context, decoded *decodedArchive, limits Limits, configOptions ConfigMetadataOptions) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	file, reader, err := decoded.openPayload()
	if err != nil {
		return err
	}
	defer file.Close()

	entries, err := validatePayloadEntries(ctx, reader, limits)
	if err != nil {
		return err
	}
	manifestFile, exists := entries["manifest.json"]
	if !exists {
		return fmt.Errorf("%w: manifest is missing", ErrInvalidArchive)
	}
	manifestBytes, err := readZipFile(ctx, manifestFile, limits.MaxManifestBytes)
	if err != nil {
		return err
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("%w: decode manifest", ErrInvalidArchive)
	}
	if err := manifest.Validate(limits); err != nil {
		return err
	}
	if len(entries) != len(manifest.Files)+1 {
		return fmt.Errorf("%w: archive/manifest entry mismatch", ErrInvalidArchive)
	}
	for _, entry := range manifest.Files {
		member, ok := entries[entry.Path]
		if !ok {
			return fmt.Errorf("%w: missing entry %q", ErrInvalidArchive, entry.Path)
		}
		if int64(member.UncompressedSize64) != entry.Size {
			return fmt.Errorf("%w: size for %q", ErrChecksumMismatch, entry.Path)
		}
	}
	databaseFile, ok := entries[manifest.Database.Path]
	if !ok {
		return fmt.Errorf("%w: database entry", ErrInvalidArchive)
	}
	snapshot, err := createPrivateTemp(".yuri-backup-restore-db-*.sqlite3")
	if err != nil {
		return err
	}
	decoded.temporaries = append(decoded.temporaries, snapshot.Name())
	decoded.databasePath = snapshot.Name()
	size, digest, err := copyZipEntry(ctx, databaseFile, snapshot, limits.MaxDatabaseBytes)
	if err != nil {
		snapshot.Close()
		return err
	}
	if err := snapshot.Sync(); err != nil {
		snapshot.Close()
		return fmt.Errorf("sync restored sqlite snapshot: %w", err)
	}
	if err := snapshot.Close(); err != nil {
		return fmt.Errorf("close restored sqlite snapshot: %w", err)
	}
	if err := verifyStreamChecksum(size, digest, manifest.Database); err != nil {
		return err
	}
	if err := validateSQLiteFile(ctx, decoded.databasePath); err != nil {
		return err
	}
	decoded.Manifest = manifest

	if manifest.Config != nil {
		config, readErr := readZipFile(ctx, entries[manifest.Config.Path], limits.MaxConfigBytes)
		if readErr != nil {
			return readErr
		}
		if err := verifyChecksum(config, *manifest.Config); err != nil {
			return err
		}
		if err := ValidateSanitizedConfigMetadataWithOptions(config, configOptions, limits.MaxConfigBytes); err != nil {
			return err
		}
		decoded.Config = config
	}
	// Blobs are proved here and written by Restore. Verifying before any target
	// is touched keeps the package's validate-then-materialize contract even
	// though the bytes themselves never sit in memory.
	for _, entry := range manifest.Blobs {
		size, digest, blobErr := copyZipEntry(ctx, entries[entry.Path], io.Discard, limits.MaxBlobBytes)
		if blobErr != nil {
			return blobErr
		}
		if err := verifyStreamChecksum(size, digest, entry); err != nil {
			return err
		}
	}
	return nil
}

func validatePayloadEntries(ctx context.Context, reader *zip.Reader, limits Limits) (map[string]*zip.File, error) {
	if len(reader.File) < 2 || len(reader.File) > limits.MaxBlobs+3 {
		return nil, fmt.Errorf("%w: zip entry count", ErrSizeLimit)
	}
	entries := make(map[string]*zip.File, len(reader.File))
	var declared int64
	for _, file := range reader.File {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		if err := validateArchiveName(file.Name, limits.MaxPathBytes); err != nil {
			return nil, err
		}
		if _, exists := entries[file.Name]; exists {
			return nil, fmt.Errorf("%w: duplicate entry %q", ErrInvalidArchive, file.Name)
		}
		if file.Name != "manifest.json" && file.Name != "database.sqlite3" && file.Name != "config.json" && !strings.HasPrefix(file.Name, "blobs/") {
			return nil, fmt.Errorf("%w: unexpected entry %q", ErrInvalidArchive, file.Name)
		}
		if file.Mode()&os.ModeSymlink != 0 || file.FileInfo().IsDir() {
			return nil, fmt.Errorf("%w: archive member %q", ErrUnsafePath, file.Name)
		}
		if file.UncompressedSize64 > uint64(limits.MaxPlaintextBytes) || file.UncompressedSize64 > uint64(^uint64(0)>>1) {
			return nil, fmt.Errorf("%w: archive member %q", ErrSizeLimit, file.Name)
		}
		if declared > limits.MaxPlaintextBytes-int64(file.UncompressedSize64) {
			return nil, fmt.Errorf("%w: archive uncompressed total", ErrSizeLimit)
		}
		declared += int64(file.UncompressedSize64)
		entries[file.Name] = file
	}
	return entries, nil
}

// copyZipEntry streams one member to destination and returns the byte count and
// the SHA-256 computed on the way past. Pass io.Discard to verify only.
func copyZipEntry(ctx context.Context, file *zip.File, destination io.Writer, max int64) (int64, string, error) {
	if file == nil {
		return 0, "", fmt.Errorf("%w: missing zip entry", ErrInvalidArchive)
	}
	if file.UncompressedSize64 > uint64(max) || file.UncompressedSize64 > uint64(^uint64(0)>>1) {
		return 0, "", fmt.Errorf("%w: zip entry %q", ErrSizeLimit, file.Name)
	}
	if err := checkContext(ctx); err != nil {
		return 0, "", err
	}
	reader, err := file.Open()
	if err != nil {
		return 0, "", fmt.Errorf("%w: open zip entry %q", ErrInvalidArchive, file.Name)
	}
	defer reader.Close()
	digest := sha256.New()
	size, err := writeContext(ctx, io.MultiWriter(destination, digest), reader, max)
	if err != nil {
		return 0, "", fmt.Errorf("read zip entry %q: %w", file.Name, err)
	}
	if size != int64(file.UncompressedSize64) {
		return 0, "", fmt.Errorf("%w: zip entry %q size", ErrInvalidArchive, file.Name)
	}
	return size, hex.EncodeToString(digest.Sum(nil)), nil
}

func readZipFile(ctx context.Context, file *zip.File, max int64) ([]byte, error) {
	if file == nil {
		return nil, fmt.Errorf("%w: missing zip entry", ErrInvalidArchive)
	}
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

func verifyStreamChecksum(size int64, digest string, entry FileEntry) error {
	if size != entry.Size || digest != strings.ToLower(entry.SHA256) {
		return fmt.Errorf("%w: %s", ErrChecksumMismatch, entry.Path)
	}
	return nil
}

// validateSQLiteFile integrity-checks an already-extracted snapshot in place.
// The file is a private temporary; opening it read-only never touches the
// caller's active database.
func validateSQLiteFile(ctx context.Context, path string) error {
	file, _, err := openVerifiedRegular(path)
	if err != nil {
		return err
	}
	magic := make([]byte, 16)
	if _, err := io.ReadFull(file, magic); err != nil || string(magic) != "SQLite format 3\x00" {
		file.Close()
		return fmt.Errorf("%w: SQLite header", ErrInvalidArchive)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close sqlite validation file: %w", err)
	}
	if err := checkContext(ctx); err != nil {
		return err
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
