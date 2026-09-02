package backup

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func writeNewFile(ctx context.Context, path string, content []byte, max int64, maxPath int) error {
	if int64(len(content)) > max {
		return fmt.Errorf("%w: restore output", ErrSizeLimit)
	}
	return writeNewFileStream(ctx, path, bytes.NewReader(content), nil, max, maxPath)
}

// writeNewFileStream materializes src at path through a same-directory
// temporary file and a no-replace install, never buffering the content.
//
// When expected is non-nil the size and SHA-256 are recomputed as the bytes go
// past and a mismatch fails before the file is published, so a payload that
// changed between validation and materialization cannot be installed.
func writeNewFileStream(ctx context.Context, path string, src io.Reader, expected *FileEntry, max int64, maxPath int) error {
	if _, err := validateAbsolutePath(path, maxPath, "restore output"); err != nil {
		return err
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
	digest := sha256.New()
	size, err := writeContext(ctx, io.MultiWriter(temporary, digest), src, max)
	if err != nil {
		temporary.Close()
		return fmt.Errorf("write restore output: %w", err)
	}
	if expected != nil {
		if size != expected.Size || hex.EncodeToString(digest.Sum(nil)) != strings.ToLower(expected.SHA256) {
			temporary.Close()
			return fmt.Errorf("%w: %s", ErrChecksumMismatch, expected.Path)
		}
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
	return atomicWriteStream(ctx, destination, mode, func(out io.Writer) error {
		_, err := writeContext(ctx, out, bytes.NewReader(content), int64(len(content)))
		return err
	})
}

// atomicWriteStream publishes whatever produce writes, without ever holding the
// output in memory. The same-directory temporary file, the owner-only mode, and
// the no-replace install are unchanged from the buffered version.
func atomicWriteStream(ctx context.Context, destination string, mode os.FileMode, produce func(io.Writer) error) error {
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
	// Removed on every path: success (after the link is installed), failure,
	// and cancellation. A partially written plaintext-derived archive never
	// survives this function.
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return fmt.Errorf("chmod backup temporary file: %w", err)
	}
	buffered := bufio.NewWriterSize(temporary, 64<<10)
	if err := produce(buffered); err != nil {
		temporary.Close()
		return err
	}
	if err := buffered.Flush(); err != nil {
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

// createPrivateTemp opens an owner-only temporary file for staging decrypted
// plaintext. The caller owns removal on every path.
func createPrivateTemp(pattern string) (*os.File, error) {
	file, err := os.CreateTemp("", pattern)
	if err != nil {
		return nil, fmt.Errorf("create temporary file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, fmt.Errorf("chmod temporary file: %w", err)
	}
	return file, nil
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
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
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
