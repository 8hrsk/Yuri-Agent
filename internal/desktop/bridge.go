// Package desktop wires foundation services to the Wails lifecycle.
package desktop

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/observability"
	"github.com/OrdoAI/yuri-agent/internal/providers/codexapp"
	securitykeyring "github.com/OrdoAI/yuri-agent/internal/security/keyring"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

// Bridge is the deliberately small API exposed to the Stage 0 frontend.
type Bridge struct {
	mu               sync.RWMutex
	logger           *slog.Logger
	database         *sql.DB
	repositories     *storage.Repositories
	paths            config.Paths
	config           config.Config
	keyring          *securitykeyring.Store
	appCtx           context.Context
	codex            *codexapp.Client
	activeRuns       map[string]context.CancelFunc
	approvals        map[string]chan bool
	backgroundCtx    context.Context
	backgroundCancel context.CancelFunc
	background       sync.WaitGroup
	modelTurns       chan struct{}
	shuttingDown     bool
}

// Status is safe to expose to the local frontend and contains no secrets.
type Status struct {
	State    string `json:"state"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
}

// NewBridge initializes configuration and authoritative storage before the UI
// starts. A failed foundation dependency prevents a partially working process.
func NewBridge(ctx context.Context) (*Bridge, error) {
	paths, err := config.DefaultPaths()
	if err != nil {
		return nil, err
	}
	value, err := config.Load(paths)
	if err != nil {
		return nil, err
	}
	paths = paths.WithDataDirectory(value.DataDirectory)
	for _, directory := range []string{paths.DataDirectory, paths.BlobDirectory, paths.LogDirectory} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create runtime directory: %w", err)
		}
	}
	level := new(slog.LevelVar)
	if err := level.UnmarshalText([]byte(value.LogLevel)); err != nil {
		return nil, fmt.Errorf("parse log level: %w", err)
	}
	logger := observability.NewLogger(observability.LoggerOptions{
		Level:  level.Level(),
		Format: "json",
		Output: os.Stderr,
	})
	database, err := storage.Open(ctx, paths.DatabaseFile)
	if err != nil {
		return nil, err
	}
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		database.Close()
		return nil, err
	}
	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())
	return &Bridge{
		logger: logger, database: database, repositories: repositories, paths: paths,
		config: value, keyring: securitykeyring.New(), activeRuns: make(map[string]context.CancelFunc),
		approvals: make(map[string]chan bool), backgroundCtx: backgroundCtx, backgroundCancel: backgroundCancel,
		modelTurns: make(chan struct{}, 1),
	}, nil
}

// Startup receives the Wails application context.
func (b *Bridge) Startup(ctx context.Context) {
	b.mu.Lock()
	b.appCtx = ctx
	b.mu.Unlock()
	b.logger.InfoContext(ctx, "desktop runtime started", "platform", runtime.GOOS)
}

// Shutdown releases durable resources after background work has stopped.
func (b *Bridge) Shutdown(ctx context.Context) {
	b.mu.Lock()
	client := b.codex
	b.codex = nil
	b.shuttingDown = true
	backgroundCancel := b.backgroundCancel
	for _, cancel := range b.activeRuns {
		cancel()
	}
	b.activeRuns = make(map[string]context.CancelFunc)
	b.mu.Unlock()
	if backgroundCancel != nil {
		backgroundCancel()
	}
	b.background.Wait()
	if client != nil {
		if err := client.Close(); err != nil {
			b.logger.ErrorContext(ctx, "close Codex app server", "error", err)
		}
	}
	if err := b.database.Close(); err != nil {
		b.logger.ErrorContext(ctx, "close sqlite", "error", err)
	}
}

// Health is the Stage 0 bridge smoke endpoint.
func (b *Bridge) Health() Status {
	return Status{State: "ready", Version: "0.3.0-stage2", Platform: runtime.GOOS + "/" + runtime.GOARCH}
}

func (b *Bridge) context() (context.Context, context.CancelFunc) {
	b.mu.RLock()
	appContext := b.appCtx
	b.mu.RUnlock()
	if appContext == nil {
		appContext = context.Background()
	}
	return context.WithTimeout(appContext, 30*time.Second)
}
