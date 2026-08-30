package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func pluginChecksum(manifest plugins.Manifest) string {
	if manifest.Checksum == nil {
		return ""
	}
	return manifest.Checksum.Value
}

func pluginRepositoryURL(manifest plugins.Manifest) string {
	if manifest.Repository == nil {
		return ""
	}
	return manifest.Repository.URL
}

func pluginReleaseTag(manifest plugins.Manifest) string {
	if manifest.Repository == nil {
		return ""
	}
	return manifest.Repository.ReleaseTag
}

func pluginSourceCommit(manifest plugins.Manifest) string {
	if manifest.Repository == nil {
		return ""
	}
	return manifest.Repository.Commit
}

func installPluginDirectory(source, destination string) error {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create plugin parent directory: %w", err)
	}
	if _, err := os.Stat(destination); err == nil {
		return errors.New("plugin version is already installed")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect plugin destination: %w", err)
	}
	staging, err := os.MkdirTemp(parent, ".installing-*")
	if err != nil {
		return fmt.Errorf("create plugin staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := copyPluginTree(source, staging); err != nil {
		return err
	}
	if err := os.Rename(staging, destination); err != nil {
		return fmt.Errorf("commit plugin installation: %w", err)
	}
	return nil
}

func removeOwnedPluginDirectory(root, pluginID, version string) error {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(pluginID) == "" || strings.TrimSpace(version) == "" {
		return errors.New("plugin removal path is incomplete")
	}
	if filepath.Base(pluginID) != pluginID || filepath.Base(version) != version || pluginID == "." || version == "." {
		return errors.New("plugin removal identifiers are not path-safe")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve plugin root: %w", err)
	}
	rootInfo, err := os.Lstat(rootAbs)
	if err != nil {
		return fmt.Errorf("inspect plugin root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("plugin root must be a real directory")
	}
	pluginRoot := filepath.Join(rootAbs, pluginID)
	pluginInfo, err := os.Lstat(pluginRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect plugin directory: %w", err)
	}
	if !pluginInfo.IsDir() || pluginInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to remove through a symlinked plugin directory")
	}
	target := filepath.Join(pluginRoot, version)
	targetInfo, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect plugin version directory: %w", err)
	}
	if !targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to remove a non-directory plugin installation")
	}
	quarantineID, err := domain.NewID("removing")
	if err != nil {
		return err
	}
	quarantine := filepath.Join(rootAbs, "."+string(quarantineID))
	if err := os.Rename(target, quarantine); err != nil {
		return fmt.Errorf("quarantine plugin directory: %w", err)
	}
	if err := os.RemoveAll(quarantine); err != nil {
		return fmt.Errorf("remove quarantined plugin directory: %w", err)
	}
	return nil
}

func copyPluginTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin package contains forbidden symlink %q", relative)
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin package contains unsupported file %q", relative)
		}
		return copyPluginFile(path, target, info.Mode())
	})
}

func copyPluginFile(source, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	permissions := fs.FileMode(0o600)
	if mode.Perm()&0o111 != 0 {
		permissions = 0o700
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permissions)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

func (b *Bridge) appendPluginAudit(ctx context.Context, action string, pluginID domain.ID, decision domain.PermissionDecision, status string) error {
	id, err := domain.NewID("audit")
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"status": status})
	return b.repositories.Audit.Append(ctx, storage.AuditEvent{
		ID: id, Actor: domain.ActorUser, Action: action, Target: string(pluginID),
		Decision: decision, PayloadRedacted: string(payload), CreatedAt: time.Now().UTC(),
	})
}
