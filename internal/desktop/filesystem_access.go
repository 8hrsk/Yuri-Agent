package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/security"
	builtintools "github.com/OrdoAI/yuri-agent/internal/tools"
)

type filesystemAccess struct {
	Operation      string
	Path           string
	PermissionRoot string
	Allowed        bool
}

func resolveFilesystemAccess(call agent.ToolCall) (filesystemAccess, error) {
	switch call.Name {
	case builtintools.FilesystemReadToolID:
		var request builtintools.ReadRequest
		if err := json.Unmarshal(call.Arguments, &request); err != nil {
			return filesystemAccess{}, fmt.Errorf("decode filesystem request: %w", err)
		}
		operation := strings.ToLower(strings.TrimSpace(request.Operation))
		if operation == "" {
			operation = builtintools.OperationRead
		}
		if operation != builtintools.OperationRead && operation != builtintools.OperationList && operation != builtintools.OperationSearch {
			return filesystemAccess{}, fmt.Errorf("unsupported filesystem read operation %q", request.Operation)
		}
		path, err := canonicalReadPath(request.Path)
		if err != nil {
			return filesystemAccess{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return filesystemAccess{}, fmt.Errorf("inspect filesystem path: %w", err)
		}
		if operation == builtintools.OperationRead && !info.Mode().IsRegular() {
			return filesystemAccess{}, fmt.Errorf("filesystem read target is not a regular file: %s", path)
		}
		if (operation == builtintools.OperationList || operation == builtintools.OperationSearch) && !info.IsDir() {
			return filesystemAccess{}, fmt.Errorf("filesystem %s target is not a directory: %s", operation, path)
		}
		root := path
		if !info.IsDir() {
			root = filepath.Dir(path)
		}
		return filesystemAccess{Operation: operation, Path: path, PermissionRoot: root}, nil
	case builtintools.FilesystemWriteToolID:
		var request builtintools.WriteRequest
		if err := json.Unmarshal(call.Arguments, &request); err != nil {
			return filesystemAccess{}, fmt.Errorf("decode filesystem write request: %w", err)
		}
		operation := strings.ToLower(strings.TrimSpace(request.Operation))
		if operation != builtintools.OperationCreate && operation != builtintools.OperationReplace {
			return filesystemAccess{}, fmt.Errorf("unsupported filesystem write operation %q", request.Operation)
		}
		path, parent, err := canonicalWritePath(request.Path)
		if err != nil {
			return filesystemAccess{}, err
		}
		return filesystemAccess{Operation: operation, Path: path, PermissionRoot: parent}, nil
	default:
		return filesystemAccess{}, fmt.Errorf("tool %q does not request filesystem access", call.Name)
	}
}

func filesystemAccessForRoots(call agent.ToolCall, roots []string) (filesystemAccess, error) {
	access, err := resolveFilesystemAccess(call)
	if err != nil {
		return filesystemAccess{}, err
	}
	if len(roots) == 0 {
		return access, nil
	}
	allowlist, err := security.NewPathAllowlist(roots)
	if err != nil {
		return filesystemAccess{}, err
	}
	if call.Name == builtintools.FilesystemWriteToolID {
		_, err = allowlist.ResolveForWrite(access.Path)
	} else {
		_, err = allowlist.Resolve(access.Path)
	}
	if err == nil {
		access.Allowed = true
		return access, nil
	}
	if errors.Is(err, security.ErrPathNotAllowed) {
		return access, nil
	}
	return filesystemAccess{}, err
}

func canonicalReadPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("filesystem path must be absolute")
	}
	if invalidFilesystemPath(path) {
		return "", fmt.Errorf("filesystem path contains unsupported characters or exceeds 4096 bytes")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve filesystem path: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func canonicalWritePath(path string) (string, string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", "", fmt.Errorf("filesystem path must be absolute")
	}
	if invalidFilesystemPath(path) {
		return "", "", fmt.Errorf("filesystem path contains unsupported characters or exceeds 4096 bytes")
	}
	cleaned := filepath.Clean(path)
	base := filepath.Base(cleaned)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "", "", fmt.Errorf("filesystem write target must name a file")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(cleaned))
	if err != nil {
		return "", "", fmt.Errorf("resolve filesystem parent: %w", err)
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("filesystem parent is not a directory: %s", filepath.Dir(cleaned))
	}
	target := filepath.Join(parent, base)
	if targetInfo, statErr := os.Lstat(target); statErr == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("filesystem write target cannot be a symlink: %s", target)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", "", fmt.Errorf("inspect filesystem target: %w", statErr)
	}
	return target, parent, nil
}

func invalidFilesystemPath(path string) bool {
	return len(path) > 4096 || strings.IndexFunc(path, func(value rune) bool { return value < 0x20 || value == 0x7f }) >= 0
}
