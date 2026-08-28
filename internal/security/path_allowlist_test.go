package security

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPathAllowlistResolveForWriteAllowsMissingFileUnderCanonicalParent(t *testing.T) {
	root := t.TempDir()
	allowlist, err := NewPathAllowlist([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "new.txt")
	resolved, err := allowlist.ResolveForWrite(target)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonicalRoot, "new.txt")
	if resolved != want {
		t.Fatalf("resolved = %q, want %q", resolved, want)
	}
}

func TestPathAllowlistResolveForWriteRejectsMissingParentAndSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	allowlist, err := NewPathAllowlist([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := allowlist.ResolveForWrite(filepath.Join(root, "missing", "new.txt")); !errors.Is(err, ErrPathNotFound) {
		t.Fatalf("missing parent error = %v", err)
	}
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target.txt")
	if err := os.Symlink(outsideFile, target); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := allowlist.ResolveForWrite(target); !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("symlink target error = %v", err)
	}
}
