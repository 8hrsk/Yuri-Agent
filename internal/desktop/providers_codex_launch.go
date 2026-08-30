package desktop

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/providers/codexapp"
)

// defaultCodexStartTimeout bounds one Codex app-server launch and its
// JSON-RPC handshake. The launch spawns an external process, so it must never
// run while the bridge mutex is held and must never wait on a context without
// a deadline: a codex binary that starts but never answers "initialize" would
// otherwise freeze every bridge method, including CancelRun.
const defaultCodexStartTimeout = 60 * time.Second

// codexStartFunc is the injection point for the external Codex launch. Tests
// substitute it to exercise the single-flight and timeout behavior without a
// real process.
type codexStartFunc func(context.Context, codexapp.Options) (*codexapp.Client, error)

// codexLaunch is the single-flight record of one in-progress Codex launch.
// Concurrent ensureCodex callers join the record instead of spawning duplicate
// app-server processes. client/err/published are written once, under b.mu,
// before done is closed; readers only touch them after done is observed.
type codexLaunch struct {
	done      chan struct{}
	closeOnce sync.Once
	client    *codexapp.Client
	err       error
	published bool
}

// finish releases every ensureCodex waiter exactly once. The normal path and
// the panic recovery in launchCodex both call it, and a second call after a
// panic that happened past the normal signal must not close a closed channel.
func (l *codexLaunch) finish() {
	l.closeOnce.Do(func() { close(l.done) })
}

// ensureCodex returns the shared Codex client, launching it at most once.
//
// Locking protocol: b.mu is held only to read configuration and to claim or
// publish the launch record. The process spawn and handshake run on a detached
// goroutine under their own deadline, so a hung codex binary can never block
// another bridge method. Callers wait on the launch record and are themselves
// bounded by their context and by the launch timeout.
func (b *Bridge) ensureCodex(ctx context.Context) (*codexapp.Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// A superseded launch (provider re-saved or logged out mid-start) resolves
	// without publishing a client; retry a bounded number of times instead of
	// returning a confusing nil client.
	for attempt := 0; attempt < 3; attempt++ {
		b.mu.Lock()
		if b.shuttingDown {
			b.mu.Unlock()
			return nil, errors.New("desktop bridge is shutting down")
		}
		if b.codex != nil {
			client := b.codex
			b.mu.Unlock()
			return client, nil
		}
		timeout := b.codexStartTimeout
		if timeout <= 0 {
			timeout = defaultCodexStartTimeout
		}
		launch := b.codexLaunch
		if launch == nil {
			options, err := b.codexOptionsLocked()
			if err != nil {
				b.mu.Unlock()
				return nil, err
			}
			start := b.codexStart
			if start == nil {
				start = codexapp.Start
			}
			generation := b.codexGeneration
			launch = &codexLaunch{done: make(chan struct{})}
			b.codexLaunch = launch
			b.mu.Unlock()
			go b.launchCodex(launch, start, options, generation, timeout)
		} else {
			b.mu.Unlock()
		}
		// The waiter is always bounded: the caller context may have no deadline
		// (chat runs pass a cancel-only context) and a start function that
		// ignores its context must still surface as a timeout, not a hang.
		// The grace period lets the launch's own deadline fire first so callers
		// see the real start error rather than this backstop.
		timer := time.NewTimer(timeout + timeout/4)
		select {
		case <-launch.done:
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
			return nil, fmt.Errorf("start Codex app server: %w", context.DeadlineExceeded)
		}
		if launch.err != nil {
			return nil, launch.err
		}
		if launch.published {
			return launch.client, nil
		}
	}
	return nil, errors.New("start Codex app server: configuration changed during launch")
}

// launchCodex performs the slow work outside b.mu and publishes the result.
//
// A panic here is contained rather than fatal, and the recovery must still
// resolve the launch record: every ensureCodex caller is parked on launch.done,
// and a launch that died without publishing an error would hold the shared
// client slot and stall each of them until its own timeout backstop fires.
func (b *Bridge) launchCodex(launch *codexLaunch, start codexStartFunc, options codexapp.Options, generation uint64, timeout time.Duration) {
	defer b.recoverBridgeGoroutine("codex_launch", func(recovered error) {
		b.mu.Lock()
		if b.codexLaunch == launch {
			b.codexLaunch = nil
		}
		if launch.err == nil && !launch.published {
			launch.err = recovered
		}
		b.mu.Unlock()
		launch.finish()
	})
	startCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client, err := start(startCtx, options)

	var stale *codexapp.Client
	b.mu.Lock()
	if b.codexLaunch == launch {
		b.codexLaunch = nil
	}
	switch {
	case err != nil:
		launch.err = err
	case b.codexGeneration != generation || b.shuttingDown:
		// The provider configuration changed (or shutdown started) while the
		// process was coming up: drop the stale client instead of publishing it.
		stale = client
	default:
		b.codex = client
		launch.client = client
		launch.published = true
	}
	b.mu.Unlock()
	launch.finish()
	if stale != nil {
		_ = stale.Close()
	}
}

// codexOptionsLocked reads the enabled Codex provider. Callers must hold b.mu.
func (b *Bridge) codexOptionsLocked() (codexapp.Options, error) {
	for index := range b.config.Providers {
		provider := &b.config.Providers[index]
		if provider.Kind == config.ProviderCodexAppServer && provider.Enabled {
			return codexapp.Options{
				Binary: provider.Binary, WorkingDirectory: b.paths.DataDirectory,
				ClientInfo: codexapp.ClientInfo{Name: "yuri", Title: "Yuri", Version: "0.1.0"},
			}, nil
		}
	}
	return codexapp.Options{}, errors.New("enabled Codex App Server provider is not configured")
}
