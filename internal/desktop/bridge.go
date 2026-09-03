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

	"github.com/OrdoAI/yuri-agent/internal/buildinfo"
	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/observability"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	"github.com/OrdoAI/yuri-agent/internal/proactivity"
	"github.com/OrdoAI/yuri-agent/internal/providers/codexapp"
	"github.com/OrdoAI/yuri-agent/internal/reflection"
	schedulerpkg "github.com/OrdoAI/yuri-agent/internal/scheduler"
	securitykeyring "github.com/OrdoAI/yuri-agent/internal/security/keyring"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

// Bridge exposes the local single-owner application services to Wails.
type Bridge struct {
	mu                  sync.RWMutex
	logger              *slog.Logger
	database            *sql.DB
	repositories        *storage.Repositories
	paths               config.Paths
	config              config.Config
	keyring             *securitykeyring.Store
	appCtx              context.Context
	codex               *codexapp.Client
	codexLaunch         *codexLaunch
	codexGeneration     uint64
	codexStart          codexStartFunc
	codexStartTimeout   time.Duration
	activeRuns          map[string]context.CancelFunc
	peerDialogueRuns    map[string]context.CancelFunc
	approvals           map[string]*approvalGate
	backgroundCtx       context.Context
	backgroundCancel    context.CancelFunc
	background          sync.WaitGroup
	titleRuns           map[string]struct{}
	modelTurns          chan struct{}
	googleSlowModes     map[string]*googleSlowModeEntry
	googleClientFactory googleAIStudioClientFactory
	googleQuotaLedger   *googleFileQuotaLedger
	reflectionRuns      *reflection.Coordinator
	reflectionGate      chan struct{}
	peerTriggerGate     chan struct{}
	shuttingDown        bool
	pluginSupervisors   map[string]*plugins.Supervisor
	proactivity         *proactivity.Service
	scheduler           *schedulerpkg.Scheduler
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
	for _, directory := range []string{paths.DataDirectory, paths.BlobDirectory, paths.LogDirectory, paths.PluginDirectory} {
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
	if err := repositories.PeerDialogues.RecoverInterrupted(ctx, time.Now().UTC(), "interrupted by application restart"); err != nil {
		database.Close()
		return nil, fmt.Errorf("recover peer dialogues: %w", err)
	}
	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())
	bridge := &Bridge{
		logger: logger, database: database, repositories: repositories, paths: paths,
		config: value, keyring: securitykeyring.New(), activeRuns: make(map[string]context.CancelFunc), peerDialogueRuns: make(map[string]context.CancelFunc),
		approvals: make(map[string]*approvalGate), backgroundCtx: backgroundCtx, backgroundCancel: backgroundCancel,
		titleRuns:  make(map[string]struct{}),
		modelTurns: make(chan struct{}, 1), googleSlowModes: make(map[string]*googleSlowModeEntry), googleQuotaLedger: newGoogleFileQuotaLedger(paths.DataDirectory), reflectionRuns: reflection.NewCoordinator(), reflectionGate: make(chan struct{}, 1), peerTriggerGate: make(chan struct{}, 1), pluginSupervisors: make(map[string]*plugins.Supervisor),
	}
	if err := bridge.reconcileAgentRoster(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("initialize agent roster: %w", err)
	}
	if writes, reconcileErr := bridge.reconcileCompletedPeerDialogueMemories(ctx, peerDialogueMemoryReconcileLimit); reconcileErr != nil {
		logger.WarnContext(ctx, "reconcile peer dialogue episodic memories", "writes", writes, "error", safeError(reconcileErr.Error()))
	} else if writes > 0 {
		logger.InfoContext(ctx, "reconciled peer dialogue episodic memories", "writes", writes)
	}
	service, err := proactivity.NewService(proactivitySettings(value.Proactivity), proactivity.FuncNotifier(bridge.emitNotification))
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("initialize proactivity policy: %w", err)
	}
	bridge.proactivity = service
	if err := bridge.restoreProactivityLedger(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("restore proactivity ledger: %w", err)
	}
	worker, err := schedulerpkg.New(repositories.Scheduler, schedulerpkg.ExecuteFunc(bridge.executeScheduledJob), schedulerpkg.Options{})
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("initialize scheduler: %w", err)
	}
	bridge.scheduler = worker
	return bridge, nil
}

// Startup receives the Wails application context.
func (b *Bridge) Startup(ctx context.Context) {
	b.mu.Lock()
	b.appCtx = ctx
	b.mu.Unlock()
	b.logger.InfoContext(ctx, "desktop runtime started", "platform", runtime.GOOS)
	b.restoreEnabledPlugins()
	if b.scheduler != nil {
		if err := b.scheduler.Start(b.backgroundCtx); err != nil {
			b.logger.ErrorContext(ctx, "start scheduler", "error", err)
		}
	}
}

// Shutdown releases durable resources after background work has stopped.
func (b *Bridge) Shutdown(ctx context.Context) {
	b.mu.Lock()
	client := b.codex
	b.codex = nil
	// A Codex launch may still be in flight; the bump plus shuttingDown makes it
	// close its client instead of publishing it after shutdown.
	b.codexGeneration++
	b.shuttingDown = true
	backgroundCancel := b.backgroundCancel
	supervisors := make([]*plugins.Supervisor, 0, len(b.pluginSupervisors))
	for _, supervisor := range b.pluginSupervisors {
		supervisors = append(supervisors, supervisor)
	}
	b.pluginSupervisors = make(map[string]*plugins.Supervisor)
	for _, cancel := range b.activeRuns {
		cancel()
	}
	b.activeRuns = make(map[string]context.CancelFunc)
	for _, cancel := range b.peerDialogueRuns {
		cancel()
	}
	b.peerDialogueRuns = make(map[string]context.CancelFunc)
	b.mu.Unlock()
	if backgroundCancel != nil {
		backgroundCancel()
	}
	if b.scheduler != nil {
		if err := b.scheduler.Stop(); err != nil {
			b.logger.ErrorContext(ctx, "stop scheduler", "error", err)
		}
	}
	for _, supervisor := range supervisors {
		stopCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_ = supervisor.Stop(stopCtx)
		cancel()
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

// Health is the lightweight bridge smoke endpoint.
func (b *Bridge) Health() Status {
	return Status{State: "ready", Version: buildinfo.Version, Platform: runtime.GOOS + "/" + runtime.GOARCH}
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
