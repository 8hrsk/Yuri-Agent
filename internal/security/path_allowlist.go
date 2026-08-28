// Package security contains policy and local-boundary checks shared by tools.
package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var (
	ErrInvalidRoot    = errors.New("security: invalid allowlist root")
	ErrPathNotAllowed = errors.New("security: path is outside the allowlist")
	ErrPathNotFound   = errors.New("security: path does not exist")
)

// PathAllowlist stores canonical, existing directory roots. All path checks
// are performed against EvalSymlinks results, which prevents a symlink inside
// an allowed directory from escaping into an unapproved directory.
type PathAllowlist struct {
	roots []string
}

func NewPathAllowlist(roots []string) (*PathAllowlist, error) {
	if len(roots) == 0 {
		return nil, fmt.Errorf("%w: at least one root is required", ErrInvalidRoot)
	}
	canonical := make([]string, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
			return nil, fmt.Errorf("%w: root must be an absolute path: %q", ErrInvalidRoot, root)
		}
		resolved, err := canonicalExistingPath(root)
		if err != nil {
			return nil, fmt.Errorf("%w: %q: %v", ErrInvalidRoot, root, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("%w: stat %q: %v", ErrInvalidRoot, root, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%w: root is not a directory: %q", ErrInvalidRoot, root)
		}
		if _, exists := seen[resolved]; exists {
			continue
		}
		seen[resolved] = struct{}{}
		canonical = append(canonical, resolved)
	}
	if len(canonical) == 0 {
		return nil, fmt.Errorf("%w: no usable roots", ErrInvalidRoot)
	}
	// Stable ordering makes diagnostics and tests deterministic. It does not
	// affect authorization because all roots are checked independently.
	sort.Strings(canonical)
	return &PathAllowlist{roots: canonical}, nil
}

// Roots returns a defensive copy of canonical roots.
func (a *PathAllowlist) Roots() []string {
	if a == nil {
		return nil
	}
	return append([]string(nil), a.roots...)
}

// Resolve canonicalizes path and returns it only when the existing target is
// inside one of the configured roots. Relative paths are rejected to avoid an
// implicit process-working-directory capability.
func (a *PathAllowlist) Resolve(path string) (string, error) {
	if a == nil || len(a.roots) == 0 {
		return "", fmt.Errorf("%w: allowlist is empty", ErrPathNotAllowed)
	}
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: path must be absolute", ErrPathNotAllowed)
	}
	resolved, err := canonicalExistingPath(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", ErrPathNotFound, path)
		}
		return "", fmt.Errorf("%w: %s: %v", ErrPathNotAllowed, path, err)
	}
	for _, root := range a.roots {
		if within(root, resolved) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrPathNotAllowed, path)
}

// ResolveForWrite canonicalizes a file target whose parent already exists.
// Unlike Resolve, the target itself may be absent, which is required for a
// bounded create operation. The canonical parent is checked against the
// allowlist so a symlinked directory cannot redirect the write outside a
// granted root. Existing symlink targets are rejected rather than followed.
func (a *PathAllowlist) ResolveForWrite(path string) (string, error) {
	if a == nil || len(a.roots) == 0 {
		return "", fmt.Errorf("%w: allowlist is empty", ErrPathNotAllowed)
	}
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: path must be absolute", ErrPathNotAllowed)
	}
	cleaned := filepath.Clean(path)
	base := filepath.Base(cleaned)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "", fmt.Errorf("%w: target must name a file", ErrPathNotAllowed)
	}
	parent, err := canonicalExistingPath(filepath.Dir(cleaned))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: parent of %s", ErrPathNotFound, path)
		}
		return "", fmt.Errorf("%w: parent of %s: %v", ErrPathNotAllowed, path, err)
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: parent is not a directory: %s", ErrPathNotAllowed, path)
	}
	allowed := false
	for _, root := range a.roots {
		if within(root, parent) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("%w: %s", ErrPathNotAllowed, path)
	}
	target := filepath.Join(parent, base)
	if targetInfo, err := os.Lstat(target); err == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: symlink write target: %s", ErrPathNotAllowed, path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: inspect target %s: %v", ErrPathNotAllowed, path, err)
	}
	return target, nil
}

// Contains reports whether an existing path is in the allowlist. It is a
// convenience wrapper for callers that do not need the canonical path.
func (a *PathAllowlist) Contains(path string) bool {
	_, err := a.Resolve(path)
	return err == nil
}

func canonicalExistingPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	if relative == "." {
		return true
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return !filepath.IsAbs(relative)
}
