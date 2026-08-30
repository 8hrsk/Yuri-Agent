package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
