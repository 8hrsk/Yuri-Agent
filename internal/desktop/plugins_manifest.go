package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/plugins"
)

func decodeManifest(content []byte) (plugins.Manifest, error) {
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	var manifest plugins.Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return plugins.Manifest{}, fmt.Errorf("decode plugin manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return plugins.Manifest{}, errors.New("plugin manifest contains trailing JSON")
	}
	// The permission block is checked at every decode, not only where
	// Manifest.Validate happens to be called. pluginDTO renders one row per
	// declaration while pluginConsentGrants can keep only one scope per
	// capability, so a manifest that declares a capability twice would let the
	// scope the owner reads and approves differ from the scope enforced. Such
	// a manifest is malformed and is refused here rather than resolved by a
	// silent last-one-wins rule.
	if err := plugins.ValidatePermissionDeclarations(manifest.Permissions); err != nil {
		return plugins.Manifest{}, fmt.Errorf("plugin manifest permissions: %w", err)
	}
	return manifest, nil
}

func loadManifestFromDirectory(directory string) (plugins.Manifest, []byte, error) {
	content, err := readBoundedFile(filepath.Join(directory, plugins.ManifestFileName), maxPluginManifestBytes)
	if err != nil {
		return plugins.Manifest{}, nil, err
	}
	manifest, err := decodeManifest(content)
	return manifest, content, err
}

func canonicalPluginPackage(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("plugin package path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve plugin package: %w", err)
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve plugin package symlinks: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", errors.New("plugin package path must be a directory")
	}
	return real, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return content, nil
}
