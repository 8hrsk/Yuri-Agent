package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/security"
)

func newFilesystemTool(t *testing.T, root string, maxBytes int64) *ReadOnlyFilesystemTool {
	t.Helper()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	policy := security.NewPolicyEvaluator(
		security.WithPolicyClock(domain.FixedClock{At: now}),
		security.WithPolicyGrant(domain.PermissionGrant{
			ID: "filesystem-grant", SubjectID: "agent", Capability: domain.CapabilityFilesystemRead,
			Scope:     domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{root}},
			GrantedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		}),
	)
	tool, err := NewReadOnlyFilesystem(ReadOnlyFilesystemConfig{
		Roots: []string{root}, Policy: policy, SubjectID: "agent", MaxOutputBytes: maxBytes, MaxEntries: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func TestReadOnlyFilesystemBoundsReadAndRejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	file := filepath.Join(root, "note.txt")
	escape := filepath.Join(root, "escape.txt")
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(file, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, escape); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	tool := newFilesystemTool(t, root, 4)
	result, err := tool.Execute(context.Background(), ReadRequest{Operation: OperationRead, Path: file})
	if err != nil {
		t.Fatalf("read error = %v", err)
	}
	if result.Content != "0123" || !result.Truncated || result.BytesRead != 4 || result.TotalBytes != 10 {
		t.Fatalf("bounded read = %#v", result)
	}
	if _, err := tool.Execute(context.Background(), ReadRequest{Operation: OperationRead, Path: escape}); !errors.Is(err, security.ErrPathNotAllowed) {
		t.Fatalf("escape error = %v, want path not allowed", err)
	}
}

func TestReadOnlyFilesystemListSearchAndDenyByDefault(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "alpha.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "beta.log"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	tool := newFilesystemTool(t, root, 1024)
	listed, err := tool.Execute(context.Background(), ReadRequest{Operation: OperationList, Path: root})
	if err != nil || len(listed.Entries) != 3 {
		t.Fatalf("list = %#v, %v", listed, err)
	}
	searched, err := tool.Execute(context.Background(), ReadRequest{Operation: OperationSearch, Path: root, Query: "ALP"})
	if err != nil || len(searched.Matches) != 1 || !strings.HasSuffix(searched.Matches[0].Path, "alpha.txt") {
		t.Fatalf("search = %#v, %v", searched, err)
	}
	noPolicy, err := NewReadOnlyFilesystem(ReadOnlyFilesystemConfig{Roots: []string{root}, SubjectID: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noPolicy.Execute(context.Background(), ReadRequest{Operation: OperationRead, Path: filepath.Join(root, "alpha.txt")}); !errors.Is(err, domain.ErrNotPermitted) {
		t.Fatalf("nil policy error = %v, want not permitted", err)
	}
}

func TestReadOnlyFilesystemHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newFilesystemTool(t, root, 100)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Execute(ctx, ReadRequest{Path: file}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}
}
