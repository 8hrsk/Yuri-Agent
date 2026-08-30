package desktop

import (
	"context"
	"errors"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func (request PluginIDRequest) pluginID() domain.ID {
	value := strings.TrimSpace(request.PluginID)
	if value == "" {
		value = strings.TrimSpace(request.ID)
	}
	return domain.ID(value)
}

func (b *Bridge) installedPlugin(ctx context.Context, id domain.ID) (storage.PluginRecord, plugins.Manifest, error) {
	if id.Empty() {
		return storage.PluginRecord{}, plugins.Manifest{}, errors.New("plugin id is required")
	}
	record, err := b.repositories.Plugins.Get(ctx, id)
	if err != nil {
		return storage.PluginRecord{}, plugins.Manifest{}, err
	}
	manifest, err := decodeManifest([]byte(record.ManifestJSON))
	if err != nil {
		return storage.PluginRecord{}, plugins.Manifest{}, err
	}
	return record, manifest, nil
}

func (b *Bridge) PluginDevMode() bool { return b.pluginDevMode() }

func (b *Bridge) SetPluginDevMode(enabled bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	candidate := b.config
	candidate.PluginDevMode = enabled
	if err := config.Save(b.paths, candidate); err != nil {
		return err
	}
	b.config = candidate
	return nil
}

func (b *Bridge) pluginDevMode() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.config.PluginDevMode
}
