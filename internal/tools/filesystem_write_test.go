package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/security"
)

func newWriteFilesystemTool(t *testing.T, root string, maxBytes int64) *WriteFilesystemTool {
	return newWriteFilesystemToolWithLimits(t, root, maxBytes, 0)
}

func newWriteFilesystemToolWithLimits(t *testing.T, root string, maxBytes, maxExistingBytes int64) *WriteFilesystemTool {
	t.Helper()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	policy := security.NewPolicyEvaluator(
		security.WithPolicyClock(domain.FixedClock{At: now}),
		security.WithPolicyGrant(domain.PermissionGrant{
			ID: "write-grant", SubjectID: "agent", Capability: domain.CapabilityFilesystemWrite,
			Scope:     domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{root}},
			GrantedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		}),
	)
	tool, err := NewWriteFilesystem(WriteFilesystemConfig{
		Roots: []string{root}, Policy: policy, SubjectID: "agent",
		MaxInputBytes: maxBytes, MaxExistingBytes: maxExistingBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func TestWriteFilesystemBoundsExistingReplaceTargetAndCleansTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "large.txt")
	if err := os.WriteFile(target, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newWriteFilesystemToolWithLimits(t, root, 1024, 4)
	if _, err := tool.ExecuteApproved(context.Background(), WriteRequest{
		Operation: OperationReplace, Path: target, Content: "new", ExpectedSHA256: hashText("12345"),
	}); err == nil {
		t.Fatal("oversized existing file was accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".yuri-write-") {
			t.Fatalf("temporary file was not cleaned: %s", entry.Name())
		}
	}
}

func TestWriteFilesystemCreateRequiresApprovalAndIsExclusive(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "created.txt")
	tool := newWriteFilesystemTool(t, root, 1024)
	request := WriteRequest{Operation: OperationCreate, Path: target, Content: "hello"}

	if _, err := tool.Execute(context.Background(), request); !errors.Is(err, ErrApprovalNotGranted) {
		t.Fatalf("Execute() error = %v, want approval required", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unapproved create changed filesystem: %v", err)
	}
	result, err := tool.ExecuteApproved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "hello" {
		t.Fatalf("created content = %q, %v", content, err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != filepath.Join(canonicalRoot, "created.txt") || result.Bytes != 5 || result.Replaced {
		t.Fatalf("create result = %#v", result)
	}
	if _, err := tool.ExecuteApproved(context.Background(), request); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second create error = %v, want conflict", err)
	}
}

func TestWriteFilesystemReplaceRequiresCurrentHashAndPreservesMode(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "note.txt")
	if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	tool := newWriteFilesystemTool(t, root, 1024)
	wrong := WriteRequest{Operation: OperationReplace, Path: target, Content: "new", ExpectedSHA256: hashText("stale")}
	if _, err := tool.ExecuteApproved(context.Background(), wrong); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale replace error = %v, want conflict", err)
	}
	content, _ := os.ReadFile(target)
	if string(content) != "old" {
		t.Fatalf("stale replace changed content to %q", content)
	}
	request := WriteRequest{Operation: OperationReplace, Path: target, Content: "new", ExpectedSHA256: hashText("old")}
	result, err := tool.ExecuteApproved(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(target)
	if err != nil || string(content) != "new" || !result.Replaced {
		t.Fatalf("replace result = %#v, content = %q, err = %v", result, content, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("replacement mode = %v", info.Mode().Perm())
	}
}

func TestWriteFilesystemRejectsEscapesSymlinksAndOversizedContent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	tool := newWriteFilesystemTool(t, root, 4)

	if _, err := tool.ExecuteApproved(context.Background(), WriteRequest{
		Operation: OperationCreate, Path: filepath.Join(outside, "escape.txt"), Content: "x",
	}); !errors.Is(err, security.ErrPathNotAllowed) {
		t.Fatalf("outside create error = %v", err)
	}
	linkedDirectory := filepath.Join(root, "linked")
	if err := os.Symlink(outside, linkedDirectory); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := tool.ExecuteApproved(context.Background(), WriteRequest{
		Operation: OperationCreate, Path: filepath.Join(linkedDirectory, "escape.txt"), Content: "x",
	}); !errors.Is(err, security.ErrPathNotAllowed) {
		t.Fatalf("symlink parent error = %v", err)
	}
	outsideFile := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	targetLink := filepath.Join(root, "target.txt")
	if err := os.Symlink(outsideFile, targetLink); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.ExecuteApproved(context.Background(), WriteRequest{
		Operation: OperationReplace, Path: targetLink, Content: "x", ExpectedSHA256: hashText("secret"),
	}); !errors.Is(err, security.ErrPathNotAllowed) {
		t.Fatalf("symlink target error = %v", err)
	}
	if _, err := tool.ExecuteApproved(context.Background(), WriteRequest{
		Operation: OperationCreate, Path: filepath.Join(root, "large.txt"), Content: "12345",
	}); err == nil {
		t.Fatal("oversized content was accepted")
	}
}

func TestWriteFilesystemIsDenyByDefaultAndHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "note.txt")
	tool, err := NewWriteFilesystem(WriteFilesystemConfig{Roots: []string{root}, SubjectID: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.ExecuteApproved(context.Background(), WriteRequest{
		Operation: OperationCreate, Path: target, Content: "blocked",
	}); !errors.Is(err, domain.ErrNotPermitted) {
		t.Fatalf("deny-by-default error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	approved := newWriteFilesystemTool(t, root, 1024)
	if _, err := approved.ExecuteApproved(ctx, WriteRequest{
		Operation: OperationCreate, Path: target, Content: "blocked",
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled create error = %v", err)
	}
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
