// Package tools contains built-in tools that are executed only after policy
// and scope checks. This file intentionally implements read-only filesystem
// access; write, delete, and process execution belong to later milestones.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/security"
)

const (
	FilesystemReadToolID = "filesystem.read"
	OperationRead        = "read"
	OperationList        = "list"
	OperationSearch      = "search"
	defaultOutputBytes   = int64(128 * 1024)
	defaultMaxEntries    = 1000
	hardOutputBytes      = int64(16 * 1024 * 1024)
)

var ErrUnsupportedOperation = errors.New("tools: unsupported filesystem operation")

type ReadOnlyFilesystemConfig struct {
	Roots          []string
	Policy         domain.PolicyEngine
	SubjectID      domain.ID
	MaxOutputBytes int64
	MaxEntries     int
}

type ReadRequest struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Query     string `json:"query,omitempty"`
	MaxBytes  int64  `json:"max_bytes,omitempty"`
}

type DirectoryEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
}

type SearchMatch struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

type ReadResult struct {
	Operation  string           `json:"operation"`
	Path       string           `json:"path"`
	Content    string           `json:"content,omitempty"`
	Entries    []DirectoryEntry `json:"entries,omitempty"`
	Matches    []SearchMatch    `json:"matches,omitempty"`
	BytesRead  int64            `json:"bytes_read"`
	TotalBytes int64            `json:"total_bytes,omitempty"`
	Truncated  bool             `json:"truncated"`
}

type ToolDefinition struct {
	ID           string              `json:"id"`
	Description  string              `json:"description"`
	Risk         domain.RiskLevel    `json:"risk"`
	Capabilities []domain.Capability `json:"capabilities"`
	InputSchema  map[string]any      `json:"input_schema"`
	OutputSchema map[string]any      `json:"output_schema"`
}

// ReadOnlyFilesystemTool is safe to expose to an agent runtime. It resolves
// every path against canonical allowlist roots and asks Policy immediately
// before touching the filesystem.
type ReadOnlyFilesystemTool struct {
	allowlist      *security.PathAllowlist
	policy         domain.PolicyEngine
	subjectID      domain.ID
	maxOutputBytes int64
	maxEntries     int
}

// ReadOnlyFilesystem is kept as a concise alias for callers that use tool
// names rather than implementation names.
type ReadOnlyFilesystem = ReadOnlyFilesystemTool

func NewReadOnlyFilesystem(config ReadOnlyFilesystemConfig) (*ReadOnlyFilesystemTool, error) {
	allowlist, err := security.NewPathAllowlist(config.Roots)
	if err != nil {
		return nil, err
	}
	if config.SubjectID.Empty() {
		return nil, fmt.Errorf("%w: filesystem tool subject id is required", domain.ErrInvalidArgument)
	}
	if config.Policy == nil {
		// A nil policy is intentionally converted to a deny-by-default policy,
		// rather than silently treating the tool as unrestricted.
		config.Policy = security.NewPolicyEvaluator()
	}
	if config.MaxOutputBytes <= 0 {
		config.MaxOutputBytes = defaultOutputBytes
	} else if config.MaxOutputBytes > hardOutputBytes {
		config.MaxOutputBytes = hardOutputBytes
	}
	if config.MaxEntries <= 0 {
		config.MaxEntries = defaultMaxEntries
	}
	return &ReadOnlyFilesystemTool{
		allowlist: allowlist, policy: config.Policy, subjectID: config.SubjectID,
		maxOutputBytes: config.MaxOutputBytes, maxEntries: config.MaxEntries,
	}, nil
}

func NewReadOnlyFilesystemTool(config ReadOnlyFilesystemConfig) (*ReadOnlyFilesystemTool, error) {
	return NewReadOnlyFilesystem(config)
}

func (tool *ReadOnlyFilesystemTool) ID() string { return FilesystemReadToolID }

func (tool *ReadOnlyFilesystemTool) Definition() ToolDefinition {
	return ToolDefinition{
		ID:           FilesystemReadToolID,
		Description:  "Read or inspect files and directories inside explicitly allowed local roots.",
		Risk:         domain.RiskLow,
		Capabilities: []domain.Capability{domain.CapabilityFilesystemRead},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"operation": map[string]any{"type": "string", "enum": []string{OperationRead, OperationList, OperationSearch}},
				"path":      map[string]any{"type": "string", "description": "Absolute path inside an allowed root."},
				"query":     map[string]any{"type": "string"},
				"max_bytes": map[string]any{"type": "integer", "minimum": 1},
			},
			"required": []string{"path"},
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"operation": map[string]any{"type": "string"},
				"path":      map[string]any{"type": "string"},
				"content":   map[string]any{"type": "string"},
				"entries":   map[string]any{"type": "array"},
				"matches":   map[string]any{"type": "array"},
				"truncated": map[string]any{"type": "boolean"},
			},
		},
	}
}

func (tool *ReadOnlyFilesystemTool) AllowedRoots() []string {
	if tool == nil || tool.allowlist == nil {
		return nil
	}
	return tool.allowlist.Roots()
}

func (tool *ReadOnlyFilesystemTool) Execute(ctx context.Context, request ReadRequest) (ReadResult, error) {
	if tool == nil || tool.allowlist == nil {
		return ReadResult{}, fmt.Errorf("%w: filesystem tool is not initialized", domain.ErrInvalidArgument)
	}
	if err := contextError(ctx); err != nil {
		return ReadResult{}, err
	}
	operation := strings.ToLower(strings.TrimSpace(request.Operation))
	if operation == "" {
		operation = OperationRead
	}
	if operation != OperationRead && operation != OperationList && operation != OperationSearch {
		return ReadResult{}, fmt.Errorf("%w: %q", ErrUnsupportedOperation, request.Operation)
	}
	if strings.TrimSpace(request.Path) == "" {
		return ReadResult{}, fmt.Errorf("%w: filesystem path is required", domain.ErrInvalidArgument)
	}
	if request.MaxBytes < 0 {
		return ReadResult{}, fmt.Errorf("%w: max_bytes cannot be negative", domain.ErrInvalidArgument)
	}
	resolved, err := tool.allowlist.Resolve(request.Path)
	if err != nil {
		return ReadResult{}, err
	}
	decision, err := tool.policy.Evaluate(domain.PermissionRequest{
		SubjectID:  tool.subjectID,
		Capability: domain.CapabilityFilesystemRead,
		Scope:      domain.CapabilityScope{Kind: domain.ScopeFilesystem, Values: []string{resolved}},
		Action:     fmt.Sprintf("filesystem.%s %s", operation, resolved),
		Risk:       domain.RiskLow,
	})
	if err != nil {
		return ReadResult{}, err
	}
	if decision.Decision != domain.PermissionAllow {
		return ReadResult{}, fmt.Errorf("%w: %s", domain.ErrNotPermitted, decision.Reason)
	}
	switch operation {
	case OperationRead:
		return tool.read(ctx, resolved, request.MaxBytes)
	case OperationList:
		return tool.list(ctx, resolved)
	case OperationSearch:
		return tool.search(ctx, resolved, request.Query)
	default:
		return ReadResult{}, fmt.Errorf("%w: %q", ErrUnsupportedOperation, operation)
	}
}

func (tool *ReadOnlyFilesystemTool) read(ctx context.Context, path string, requestedMax int64) (ReadResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return ReadResult{}, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ReadResult{}, fmt.Errorf("stat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return ReadResult{}, fmt.Errorf("%w: %s is not a regular file", ErrUnsupportedOperation, path)
	}
	maxBytes := tool.outputLimit(requestedMax)
	// maxBytes is capped at hardOutputBytes during construction, so adding one
	// is safe and lets us distinguish an exact-limit file from a truncated one.
	limited, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return ReadResult{}, fmt.Errorf("read file: %w", err)
	}
	truncated := int64(len(limited)) > maxBytes
	if truncated {
		limited = limited[:maxBytes]
	}
	if err := contextError(ctx); err != nil {
		return ReadResult{}, err
	}
	return ReadResult{
		Operation: OperationRead, Path: path, Content: string(limited),
		BytesRead: int64(len(limited)), TotalBytes: info.Size(), Truncated: truncated,
	}, nil
}

func (tool *ReadOnlyFilesystemTool) list(ctx context.Context, path string) (ReadResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return ReadResult{}, fmt.Errorf("stat directory: %w", err)
	}
	if !info.IsDir() {
		return ReadResult{}, fmt.Errorf("%w: %s is not a directory", ErrUnsupportedOperation, path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return ReadResult{}, fmt.Errorf("read directory: %w", err)
	}
	result := ReadResult{Operation: OperationList, Path: path, Entries: make([]DirectoryEntry, 0)}
	usedBytes := int64(0)
	for index, entry := range entries {
		if err := contextError(ctx); err != nil {
			return ReadResult{}, err
		}
		if index >= tool.maxEntries {
			result.Truncated = true
			break
		}
		entryType := entry.Type()
		size := int64(0)
		if entryType.IsRegular() {
			if entryInfo, infoErr := entry.Info(); infoErr == nil {
				size = entryInfo.Size()
			}
		}
		candidate := DirectoryEntry{Name: entry.Name(), Type: fileType(entryType), Size: size}
		encoded, marshalErr := json.Marshal(candidate)
		if marshalErr != nil {
			return ReadResult{}, fmt.Errorf("encode directory entry: %w", marshalErr)
		}
		if usedBytes+int64(len(encoded)) > tool.maxOutputBytes {
			result.Truncated = true
			break
		}
		result.Entries = append(result.Entries, candidate)
		usedBytes += int64(len(encoded))
	}
	result.BytesRead = usedBytes
	return result, nil
}

func (tool *ReadOnlyFilesystemTool) search(ctx context.Context, path, query string) (ReadResult, error) {
	if strings.TrimSpace(query) == "" {
		return ReadResult{}, fmt.Errorf("%w: search query is required", domain.ErrInvalidArgument)
	}
	info, err := os.Stat(path)
	if err != nil {
		return ReadResult{}, fmt.Errorf("stat search path: %w", err)
	}
	if !info.IsDir() {
		return ReadResult{}, fmt.Errorf("%w: search path is not a directory", ErrUnsupportedOperation)
	}
	needle := strings.ToLower(query)
	result := ReadResult{Operation: OperationSearch, Path: path, Matches: make([]SearchMatch, 0)}
	usedBytes := int64(0)
	err = filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A single unreadable child must not turn into an escape or crash;
			// continue walking siblings while the result remains bounded.
			return nil
		}
		if err := contextError(ctx); err != nil {
			return err
		}
		if current != path && !strings.Contains(strings.ToLower(entry.Name()), needle) {
			if entry.IsDir() {
				return nil
			}
			return nil
		}
		if len(result.Matches) >= tool.maxEntries {
			result.Truncated = true
			return filepath.SkipAll
		}
		match := SearchMatch{Path: current, Type: fileType(entry.Type())}
		encoded, marshalErr := json.Marshal(match)
		if marshalErr != nil {
			return marshalErr
		}
		if usedBytes+int64(len(encoded)) > tool.maxOutputBytes {
			result.Truncated = true
			return filepath.SkipAll
		}
		if current != path {
			result.Matches = append(result.Matches, match)
			usedBytes += int64(len(encoded))
		}
		return nil
	})
	if err != nil {
		return ReadResult{}, fmt.Errorf("search directory: %w", err)
	}
	result.BytesRead = usedBytes
	sort.Slice(result.Matches, func(i, j int) bool { return result.Matches[i].Path < result.Matches[j].Path })
	return result, nil
}

func (tool *ReadOnlyFilesystemTool) outputLimit(requested int64) int64 {
	if requested <= 0 || requested > tool.maxOutputBytes {
		return tool.maxOutputBytes
	}
	return requested
}

func fileType(mode os.FileMode) string {
	switch {
	case mode.IsDir():
		return "directory"
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode.IsRegular():
		return "file"
	default:
		return mode.String()
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", domain.ErrInvalidArgument)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
