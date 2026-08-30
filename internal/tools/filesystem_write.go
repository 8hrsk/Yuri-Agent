package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/security"
)

const (
	FilesystemWriteToolID = "filesystem.write"
	OperationCreate       = "create"
	OperationReplace      = "replace"
	defaultWriteBytes     = int64(1024 * 1024)
	hardWriteBytes        = int64(16 * 1024 * 1024)
	defaultExistingBytes  = int64(64 * 1024 * 1024)
	hardExistingBytes     = int64(1024 * 1024 * 1024)
)

var ErrApprovalNotGranted = errors.New("tools: explicit approval was not granted")

type WriteFilesystemConfig struct {
	Roots            []string
	Policy           domain.PolicyEngine
	SubjectID        domain.ID
	MaxInputBytes    int64
	MaxExistingBytes int64
}

type WriteRequest struct {
	Operation      string `json:"operation"`
	Path           string `json:"path"`
	Content        string `json:"content"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
}

type WriteResult struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
	Replaced  bool   `json:"replaced"`
}

// WriteFilesystemTool supports only explicit create and replace operations.
// It never creates parent directories, follows a target symlink, appends, or
// deletes. A replace requires the caller to bind approval to the current file
// hash so a changed file cannot be overwritten by a stale model decision.
type WriteFilesystemTool struct {
	allowlist        *security.PathAllowlist
	policy           domain.PolicyEngine
	subjectID        domain.ID
	maxInputBytes    int64
	maxExistingBytes int64
}

func NewWriteFilesystem(config WriteFilesystemConfig) (*WriteFilesystemTool, error) {
	allowlist, err := security.NewPathAllowlist(config.Roots)
	if err != nil {
		return nil, err
	}
	if config.SubjectID.Empty() {
		return nil, fmt.Errorf("%w: filesystem tool subject id is required", domain.ErrInvalidArgument)
	}
	if config.Policy == nil {
		config.Policy = security.NewPolicyEvaluator()
	}
	if config.MaxInputBytes <= 0 {
		config.MaxInputBytes = defaultWriteBytes
	} else if config.MaxInputBytes > hardWriteBytes {
		config.MaxInputBytes = hardWriteBytes
	}
	if config.MaxExistingBytes <= 0 {
		config.MaxExistingBytes = defaultExistingBytes
	} else if config.MaxExistingBytes > hardExistingBytes {
		config.MaxExistingBytes = hardExistingBytes
	}
	return &WriteFilesystemTool{
		allowlist: allowlist, policy: config.Policy, subjectID: config.SubjectID,
		maxInputBytes: config.MaxInputBytes, maxExistingBytes: config.MaxExistingBytes,
	}, nil
}

func (tool *WriteFilesystemTool) ID() string { return FilesystemWriteToolID }

func (tool *WriteFilesystemTool) ResolvePath(path string) (string, error) {
	if tool == nil || tool.allowlist == nil {
		return "", fmt.Errorf("%w: filesystem write tool is not initialized", domain.ErrInvalidArgument)
	}
	return tool.allowlist.ResolveForWrite(path)
}

func (tool *WriteFilesystemTool) Definition() ToolDefinition {
	return ToolDefinition{
		ID:          FilesystemWriteToolID,
		Description: "Create or atomically replace one regular file at an absolute local path. Every call requires explicit owner approval; missing directory access is requested in the same flow.",
		Risk:        domain.RiskMedium,
		Capabilities: []domain.Capability{
			domain.CapabilityFilesystemWrite,
		},
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"operation":       map[string]any{"type": "string", "enum": []string{OperationCreate, OperationReplace}},
				"path":            map[string]any{"type": "string", "description": "Absolute local file path. The application requests owner access when needed."},
				"content":         map[string]any{"type": "string"},
				"expected_sha256": map[string]any{"type": "string", "description": "Required for replace; SHA-256 of the current file."},
			},
			"required": []string{"operation", "path", "content"},
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"operation": map[string]any{"type": "string"},
				"path":      map[string]any{"type": "string"},
				"bytes":     map[string]any{"type": "integer"},
				"sha256":    map[string]any{"type": "string"},
				"replaced":  map[string]any{"type": "boolean"},
			},
		},
	}
}

// Execute is fail-closed for medium-risk writes. Agent runtimes must call
// ExecuteApproved only after their durable approval handler returns true.
func (tool *WriteFilesystemTool) Execute(ctx context.Context, request WriteRequest) (WriteResult, error) {
	return tool.execute(ctx, request, false)
}

func (tool *WriteFilesystemTool) ExecuteApproved(ctx context.Context, request WriteRequest) (WriteResult, error) {
	return tool.execute(ctx, request, true)
}

func (tool *WriteFilesystemTool) execute(ctx context.Context, request WriteRequest, approved bool) (WriteResult, error) {
	if tool == nil || tool.allowlist == nil || tool.policy == nil {
		return WriteResult{}, fmt.Errorf("%w: filesystem write tool is not initialized", domain.ErrInvalidArgument)
	}
	if err := contextError(ctx); err != nil {
		return WriteResult{}, err
	}
	operation := strings.ToLower(strings.TrimSpace(request.Operation))
	if operation != OperationCreate && operation != OperationReplace {
		return WriteResult{}, fmt.Errorf("%w: %q", ErrUnsupportedOperation, request.Operation)
	}
	if int64(len(request.Content)) > tool.maxInputBytes {
		return WriteResult{}, fmt.Errorf("%w: content exceeds %d bytes", domain.ErrInvalidArgument, tool.maxInputBytes)
	}
	if len(request.Path) > 4096 || strings.IndexFunc(request.Path, func(value rune) bool { return value < 0x20 || value == 0x7f }) >= 0 {
		return WriteResult{}, fmt.Errorf("%w: path contains unsupported characters or exceeds 4096 bytes", domain.ErrInvalidArgument)
	}
	resolved, err := tool.allowlist.ResolveForWrite(request.Path)
	if err != nil {
		return WriteResult{}, err
	}
	if err := tool.authorize(resolved, operation, approved); err != nil {
		return WriteResult{}, err
	}

	switch operation {
	case OperationCreate:
		return tool.create(ctx, resolved, request.Content, approved)
	case OperationReplace:
		return tool.replace(ctx, resolved, request.Content, request.ExpectedSHA256, approved)
	default:
		return WriteResult{}, fmt.Errorf("%w: %q", ErrUnsupportedOperation, operation)
	}
}

func (tool *WriteFilesystemTool) authorize(path, operation string, approved bool) error {
	decision, err := tool.policy.Evaluate(domain.PermissionRequest{
		SubjectID:  tool.subjectID,
		Capability: domain.CapabilityFilesystemWrite,
		Scope:      domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{path}},
		Action:     fmt.Sprintf("filesystem.%s %s", operation, path),
		Risk:       domain.RiskMedium,
	})
	if err != nil {
		return err
	}
	switch decision.Decision {
	case domain.PermissionAllow:
		return nil
	case domain.PermissionNeedsApproval:
		if approved {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrApprovalNotGranted, decision.Reason)
	case domain.PermissionDeny:
		return fmt.Errorf("%w: %s", domain.ErrNotPermitted, decision.Reason)
	default:
		return fmt.Errorf("%w: invalid policy decision", domain.ErrNotPermitted)
	}
}

func (tool *WriteFilesystemTool) create(ctx context.Context, path, content string, approved bool) (WriteResult, error) {
	if _, err := os.Lstat(path); err == nil {
		return WriteResult{}, fmt.Errorf("%w: create target already exists: %s", domain.ErrConflict, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return WriteResult{}, fmt.Errorf("inspect create target: %w", err)
	}
	temporaryName, err := writeTemporary(ctx, filepath.Dir(path), content, 0o600)
	if err != nil {
		return WriteResult{}, err
	}
	defer os.Remove(temporaryName)
	if _, err := tool.allowlist.ResolveForWrite(path); err != nil {
		return WriteResult{}, err
	}
	if err := tool.authorize(path, OperationCreate, approved); err != nil {
		return WriteResult{}, err
	}
	// link is an atomic create-if-absent commit on the same filesystem. It
	// cannot silently replace a file created after the first existence check.
	if err := os.Link(temporaryName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return WriteResult{}, fmt.Errorf("%w: create target already exists: %s", domain.ErrConflict, path)
		}
		return WriteResult{}, fmt.Errorf("commit created file: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return WriteResult{}, err
	}
	return writeResult(OperationCreate, path, content, false), nil
}

func (tool *WriteFilesystemTool) replace(ctx context.Context, path, content, expected string, approved bool) (WriteResult, error) {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if len(expected) != sha256.Size*2 {
		return WriteResult{}, fmt.Errorf("%w: replace requires a valid expected_sha256", domain.ErrInvalidArgument)
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return WriteResult{}, fmt.Errorf("%w: replace requires a valid expected_sha256", domain.ErrInvalidArgument)
	}
	info, currentHash, err := inspectReplaceTarget(ctx, path, tool.maxExistingBytes)
	if err != nil {
		return WriteResult{}, err
	}
	if currentHash != expected {
		return WriteResult{}, fmt.Errorf("%w: file changed before replacement", domain.ErrConflict)
	}
	temporaryName, err := writeTemporary(ctx, filepath.Dir(path), content, info.Mode().Perm())
	if err != nil {
		return WriteResult{}, err
	}
	defer os.Remove(temporaryName)
	if _, err := tool.allowlist.ResolveForWrite(path); err != nil {
		return WriteResult{}, err
	}
	_, latestHash, err := inspectReplaceTarget(ctx, path, tool.maxExistingBytes)
	if err != nil {
		return WriteResult{}, err
	}
	if latestHash != expected {
		return WriteResult{}, fmt.Errorf("%w: file changed before replacement", domain.ErrConflict)
	}
	if err := tool.authorize(path, OperationReplace, approved); err != nil {
		return WriteResult{}, err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return WriteResult{}, fmt.Errorf("commit replacement: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return WriteResult{}, err
	}
	return writeResult(OperationReplace, path, content, true), nil
}

func writeTemporary(ctx context.Context, directory, content string, mode os.FileMode) (string, error) {
	temporary, err := os.CreateTemp(directory, ".yuri-write-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary file: %w", err)
	}
	name := temporary.Name()
	fail := func(cause error) (string, error) {
		_ = temporary.Close()
		_ = os.Remove(name)
		return "", cause
	}
	if err := temporary.Chmod(mode.Perm()); err != nil {
		return fail(fmt.Errorf("set temporary file permissions: %w", err))
	}
	if err := contextError(ctx); err != nil {
		return fail(err)
	}
	if _, err := io.WriteString(temporary, content); err != nil {
		return fail(fmt.Errorf("write temporary file: %w", err))
	}
	if err := temporary.Sync(); err != nil {
		return fail(fmt.Errorf("sync temporary file: %w", err))
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("close temporary file: %w", err)
	}
	return name, nil
}

func inspectReplaceTarget(ctx context.Context, path string, maxBytes int64) (os.FileInfo, string, error) {
	if err := contextError(ctx); err != nil {
		return nil, "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", fmt.Errorf("inspect replace target: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("%w: replace target is not a regular file", ErrUnsupportedOperation)
	}
	if info.Size() > maxBytes {
		return nil, "", fmt.Errorf("%w: replace target exceeds %d bytes", domain.ErrInvalidArgument, maxBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open replace target: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	for {
		if err := contextError(ctx); err != nil {
			return nil, "", err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			_, _ = hash.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, "", fmt.Errorf("hash replace target: %w", readErr)
		}
	}
	return info, hex.EncodeToString(hash.Sum(nil)), nil
}

func writeResult(operation, path, content string, replaced bool) WriteResult {
	digest := sha256.Sum256([]byte(content))
	return WriteResult{
		Operation: operation, Path: path, Bytes: int64(len(content)),
		SHA256: hex.EncodeToString(digest[:]), Replaced: replaced,
	}
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open parent directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync parent directory: %w", err)
	}
	return nil
}
