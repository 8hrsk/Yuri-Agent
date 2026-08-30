package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type blobData struct {
	Input Blob
	Entry FileEntry
}

// decodedArchive is the validated result of decodeArchive.
//
// It deliberately holds no bulk plaintext. The decrypted zip payload and the
// verified SQLite snapshot live in private temporary files that close() unlinks;
// only the manifest and the sanitized config metadata (bounded by
// MaxConfigBytes) are kept in memory.
type decodedArchive struct {
	Manifest Manifest
	Config   []byte

	payloadPath  string
	databasePath string
	temporaries  []string
}

func (d *decodedArchive) close() {
	for _, path := range d.temporaries {
		_ = os.Remove(path)
	}
	d.temporaries = nil
}

// openPayload reopens the decrypted payload for a second streaming pass.
func (d *decodedArchive) openPayload() (*os.File, *zip.Reader, error) {
	file, info, err := openVerifiedRegular(d.payloadPath)
	if err != nil {
		return nil, nil, err
	}
	reader, err := zip.NewReader(file, info.Size())
	if err != nil {
		file.Close()
		return nil, nil, fmt.Errorf("%w: zip payload: %v", ErrInvalidArchive, err)
	}
	return file, reader, nil
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

// writePayload streams the zip payload into destination. Each member is copied
// straight from its source file through the zip writer, so the payload is never
// materialized: the caller supplies a writer that seals and forwards frames.
//
// MaxPlaintextBytes is enforced as the payload is produced rather than measured
// afterwards, because there is no longer an afterwards.
func writePayload(ctx context.Context, destination io.Writer, snapshotPath string, config []byte, blobs []blobData, manifest Manifest, limits Limits) error {
	counter := &countingWriter{inner: destination, remaining: limits.MaxPlaintextBytes}
	writer := zip.NewWriter(counter)
	if err := writeZipBytes(ctx, writer, "manifest.json", mustMarshalManifest(manifest), limits.MaxManifestBytes); err != nil {
		writer.Close()
		return err
	}
	if err := writeZipFile(ctx, writer, "database.sqlite3", snapshotPath, manifest.Database, limits.MaxDatabaseBytes); err != nil {
		writer.Close()
		return err
	}
	if len(config) > 0 {
		if err := writeZipBytes(ctx, writer, "config.json", config, limits.MaxConfigBytes); err != nil {
			writer.Close()
			return err
		}
	}
	for _, blob := range blobs {
		if err := writeZipFile(ctx, writer, blob.Entry.Path, blob.Input.Source, blob.Entry, limits.MaxBlobBytes); err != nil {
			writer.Close()
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close backup payload: %w", err)
	}
	return nil
}

// countingWriter fails the export as soon as the payload would exceed its
// budget instead of letting an unbounded amount of plaintext through.
type countingWriter struct {
	inner     io.Writer
	remaining int64
}

func (c *countingWriter) Write(content []byte) (int, error) {
	if int64(len(content)) > c.remaining {
		return 0, fmt.Errorf("%w: backup payload", ErrSizeLimit)
	}
	n, err := c.inner.Write(content)
	c.remaining -= int64(n)
	return n, err
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
