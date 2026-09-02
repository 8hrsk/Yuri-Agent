package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/slowmode"
)

const (
	googleQuotaLedgerFile     = "google-ai-studio-quota.json"
	googleQuotaLedgerVersion  = 1
	googleQuotaLedgerMaxBytes = 1024 * 1024
)

type googleQuotaLedgerDocument struct {
	Version int                            `json:"version"`
	Buckets map[string]slowmode.DailyUsage `json:"buckets"`
}

// googleFileQuotaLedger is deliberately separate from user-editable config.
// It contains only local counters and uses replacement writes so a crash
// cannot leave a partially encoded RPD bucket.
type googleFileQuotaLedger struct {
	mu      sync.Mutex
	path    string
	loaded  bool
	buckets map[string]slowmode.DailyUsage
}

func newGoogleFileQuotaLedger(dataDirectory string) *googleFileQuotaLedger {
	return &googleFileQuotaLedger{
		path:    filepath.Join(dataDirectory, googleQuotaLedgerFile),
		buckets: make(map[string]slowmode.DailyUsage),
	}
}

func (ledger *googleFileQuotaLedger) Load(ctx context.Context, scope, date string) (slowmode.DailyUsage, error) {
	if err := googleLedgerContextError(ctx); err != nil {
		return slowmode.DailyUsage{}, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if err := ledger.loadLocked(); err != nil {
		return slowmode.DailyUsage{}, err
	}
	return ledger.buckets[googleQuotaBucketKey(scope, date)], nil
}

func (ledger *googleFileQuotaLedger) Save(ctx context.Context, scope, date string, usage slowmode.DailyUsage) error {
	if err := googleLedgerContextError(ctx); err != nil {
		return err
	}
	if usage.Requests < 0 {
		return errors.New("Google quota ledger request count must not be negative")
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if err := ledger.loadLocked(); err != nil {
		return err
	}
	key := googleQuotaBucketKey(scope, date)
	previous, existed := ledger.buckets[key]
	ledger.buckets[key] = usage
	if err := ledger.writeLocked(ctx); err != nil {
		if existed {
			ledger.buckets[key] = previous
		} else {
			delete(ledger.buckets, key)
		}
		return err
	}
	return nil
}

// LoadWarmup conservatively preserves the tail of the previous process's
// rolling minute. Each admitted request updates this ledger before the
// provider call, so the atomic file mtime is a safe upper-bound signal for a
// restart that happened less than one minute later.
func (ledger *googleFileQuotaLedger) LoadWarmup(ctx context.Context, query slowmode.WarmupQuery) (slowmode.WarmupState, error) {
	if err := googleLedgerContextError(ctx); err != nil {
		return slowmode.WarmupState{}, err
	}
	info, err := os.Stat(ledger.path)
	if errors.Is(err, os.ErrNotExist) {
		return slowmode.WarmupState{}, nil
	}
	if err != nil {
		return slowmode.WarmupState{}, fmt.Errorf("stat Google quota ledger: %w", err)
	}
	lastWrite := info.ModTime()
	if !lastWrite.After(query.Since) {
		return slowmode.WarmupState{}, nil
	}
	until := lastWrite.Add(time.Minute)
	if !until.After(query.Now) {
		return slowmode.WarmupState{}, nil
	}
	return slowmode.WarmupState{CooldownUntil: until}, nil
}

func (ledger *googleFileQuotaLedger) loadLocked() error {
	if ledger.loaded {
		return nil
	}
	if ledger.path == "" || filepath.Dir(ledger.path) == "." {
		return errors.New("Google quota ledger data directory is unavailable")
	}
	content, err := os.ReadFile(ledger.path)
	if errors.Is(err, os.ErrNotExist) {
		ledger.loaded = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Google quota ledger: %w", err)
	}
	if len(content) > googleQuotaLedgerMaxBytes {
		return errors.New("Google quota ledger exceeds size limit")
	}
	var document googleQuotaLedgerDocument
	if err := json.Unmarshal(content, &document); err != nil {
		return fmt.Errorf("decode Google quota ledger: %w", err)
	}
	if document.Version != googleQuotaLedgerVersion {
		return fmt.Errorf("unsupported Google quota ledger version %d", document.Version)
	}
	if document.Buckets == nil {
		document.Buckets = make(map[string]slowmode.DailyUsage)
	}
	for key, usage := range document.Buckets {
		if key == "" || usage.Requests < 0 {
			return errors.New("Google quota ledger contains an invalid bucket")
		}
	}
	ledger.buckets = document.Buckets
	ledger.loaded = true
	return nil
}

func (ledger *googleFileQuotaLedger) writeLocked(ctx context.Context) error {
	if err := googleLedgerContextError(ctx); err != nil {
		return err
	}
	directory := filepath.Dir(ledger.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create Google quota ledger directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".google-quota-*.tmp")
	if err != nil {
		return fmt.Errorf("create Google quota ledger temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure Google quota ledger temporary file: %w", err)
	}
	document := googleQuotaLedgerDocument{Version: googleQuotaLedgerVersion, Buckets: ledger.buckets}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("encode Google quota ledger: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync Google quota ledger: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Google quota ledger: %w", err)
	}
	if err := googleLedgerContextError(ctx); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, ledger.path); err != nil {
		return fmt.Errorf("replace Google quota ledger: %w", err)
	}
	keep = true
	return nil
}

func googleQuotaBucketKey(scope, date string) string { return scope + "\n" + date }

func googleLedgerContextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

var _ slowmode.DailyLedger = (*googleFileQuotaLedger)(nil)
var _ slowmode.WarmupLoader = (*googleFileQuotaLedger)(nil)
