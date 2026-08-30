package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateExternalURLAllowsOnlyCredentialFreeHTTP(t *testing.T) {
	for _, test := range []struct {
		value string
		ok    bool
	}{
		{value: "https://example.test/docs?q=hello world", ok: true},
		{value: "http://localhost:8080/status", ok: true},
		{value: "javascript:alert(1)"},
		{value: "data:text/html,boom"},
		{value: "file:///Users/owner/secret.txt"},
		{value: "https://owner:secret@example.test/"},
		{value: "/Users/owner/note.txt"},
	} {
		t.Run(test.value, func(t *testing.T) {
			_, err := validateExternalURL(test.value)
			if (err == nil) != test.ok {
				t.Fatalf("validateExternalURL(%q) error = %v", test.value, err)
			}
		})
	}
}

func TestResolveOpenableLocalPathCanonicalizesExistingTargets(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "note.txt")
	if err := os.WriteFile(target, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "note-link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveOpenableLocalPath(link)
	if err != nil {
		t.Fatal(err)
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != canonicalTarget {
		t.Fatalf("resolved path = %q, want %q", resolved, canonicalTarget)
	}
	for _, invalid := range []string{"relative.txt", filepath.Join(root, "missing.txt"), target + "\x00suffix"} {
		if _, err := resolveOpenableLocalPath(invalid); err == nil {
			t.Fatalf("invalid path %q was accepted", invalid)
		}
	}
}
