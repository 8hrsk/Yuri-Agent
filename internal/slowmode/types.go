package slowmode

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const rollingWindow = time.Minute

// PriorityClass orders inference workloads. Lower values have higher base
// priority. Aging can promote an older waiter, with FIFO used for ties.
type PriorityClass uint8

const (
	PriorityForeground PriorityClass = iota
	PriorityOwnerInitiated
	PriorityBackground
	PriorityMaintenance
)

func (class PriorityClass) valid() bool { return class <= PriorityMaintenance }

// Limits are the local pacing envelope before SafetyPercent is applied.
// Zero disables a dimension; see the package documentation for unknown remote
// limits.
type Limits struct {
	RPM           int64
	TPM           int64
	RPD           int64
	MaxConcurrent int
}

// Config defines one shared remote quota scope.
type Config struct {
	Scope  string
	Limits Limits

	// SafetyPercent is in [1,100]. The effective RPM, TPM, and RPD limits are
	// floored after multiplication, with a minimum of one for positive limits.
	SafetyPercent int
	// InteractiveReservePercent reserves this percentage of effective RPD for
	// PriorityForeground requests. It must be in [0,100]. A value of 100
	// intentionally pauses every non-foreground request for that day.
	InteractiveReservePercent int
	// AgingInterval promotes a queued request by one class each interval.
	// Zero selects one minute.
	AgingInterval time.Duration
	// Backoff bounds provider-feedback cooldowns. Zero selects conservative
	// defaults of one second and one minute.
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

func (config Config) normalized() (Config, error) {
	if config.Scope == "" {
		return Config{}, fmt.Errorf("%w: scope is required", ErrInvalidConfig)
	}
	if config.Limits.RPM < 0 || config.Limits.TPM < 0 || config.Limits.RPD < 0 {
		return Config{}, fmt.Errorf("%w: limits must not be negative", ErrInvalidConfig)
	}
	if config.Limits.MaxConcurrent <= 0 {
		return Config{}, fmt.Errorf("%w: maximum concurrency must be positive", ErrInvalidConfig)
	}
	if config.SafetyPercent < 1 || config.SafetyPercent > 100 {
		return Config{}, fmt.Errorf("%w: safety percent must be between 1 and 100", ErrInvalidConfig)
	}
	if config.InteractiveReservePercent < 0 || config.InteractiveReservePercent > 100 {
		return Config{}, fmt.Errorf("%w: interactive reserve percent must be between 0 and 100", ErrInvalidConfig)
	}
	if config.InteractiveReservePercent > 0 && config.Limits.RPD == 0 {
		return Config{}, fmt.Errorf("%w: interactive reserve requires an RPD limit", ErrInvalidConfig)
	}
	if config.AgingInterval < 0 || config.BaseBackoff < 0 || config.MaxBackoff < 0 {
		return Config{}, fmt.Errorf("%w: durations must not be negative", ErrInvalidConfig)
	}
	if config.AgingInterval == 0 {
		config.AgingInterval = time.Minute
	}
	if config.BaseBackoff == 0 {
		config.BaseBackoff = time.Second
	}
	if config.MaxBackoff == 0 {
		config.MaxBackoff = time.Minute
	}
	if config.MaxBackoff < config.BaseBackoff {
		return Config{}, fmt.Errorf("%w: maximum backoff is below base backoff", ErrInvalidConfig)
	}
	return config, nil
}

// Request is one inference attempt. InputTokens must describe the final
// request that will be passed to the provider.
type Request struct {
	InputTokens int64
	Priority    PriorityClass
}

// Accounting describes whether the provider counted a finished attempt.
type Accounting uint8

const (
	// AccountingUnknown conservatively retains all quota reservations.
	AccountingUnknown Accounting = iota
	AccountingCounted
	// AccountingNotCounted refunds RPM, TPM, and RPD. Callers must use this only
	// when provider evidence proves that the attempt was not counted.
	AccountingNotCounted
)

// Outcome reconciles a lease when an inference attempt ends. ActualInputTokens
// is used only when HasActualInputTokens is true and Accounting is not
// AccountingNotCounted.
type Outcome struct {
	Accounting           Accounting
	ActualInputTokens    int64
	HasActualInputTokens bool
}

// FeedbackKind is a provider-neutral rate-limit classification.
type FeedbackKind uint8

const (
	FeedbackShortWindow FeedbackKind = iota + 1
	FeedbackDailyQuota
	FeedbackAmbiguous
)

// Feedback describes a provider rejection. RetryAfter is honored when
// positive; otherwise bounded exponential backoff is used.
type Feedback struct {
	Kind       FeedbackKind
	RetryAfter time.Duration
}

// FeedbackResult reports the local action taken for observability.
type FeedbackResult struct {
	CooldownUntil  time.Time
	DailyExhausted bool
	AdaptiveLevel  int
}

// DailyUsage is persisted per scope and Pacific calendar date.
type DailyUsage struct {
	Requests  int64
	Exhausted bool
}

// DailyLedger prevents process restart from resetting local RPD accounting.
// Implementations must treat Save as replacement of the complete bucket.
type DailyLedger interface {
	Load(context.Context, string, string) (DailyUsage, error)
	Save(context.Context, string, string, DailyUsage) error
}

// WarmupQuery asks a restart hook for still-live rolling reservations.
type WarmupQuery struct {
	Scope string
	Since time.Time
	Now   time.Time
}

// UsagePoint is one prior request reservation.
type UsagePoint struct {
	At          time.Time
	InputTokens int64
}

// WarmupState lets an integration restore recent reservations or impose a
// conservative initial cooldown after restart.
type WarmupState struct {
	Reservations  []UsagePoint
	CooldownUntil time.Time
}

// WarmupLoader is optional. Its values are validated and clipped to the
// rolling window by NewCoordinator.
type WarmupLoader interface {
	LoadWarmup(context.Context, WarmupQuery) (WarmupState, error)
}

// Dependencies are injectable process and persistence boundaries.
type Dependencies struct {
	Clock  Clock
	Jitter Jitter
	Ledger DailyLedger
	Warmup WarmupLoader
}

// Snapshot is advisory local state; it does not represent usage by other
// applications sharing the remote provider project.
type Snapshot struct {
	Scope          string
	At             time.Time
	WindowRequests int64
	WindowTokens   int64
	DailyRequests  int64
	DailyDate      string
	DailyExhausted bool
	Active         int
	Waiting        int
	CooldownUntil  time.Time
	AdaptiveLevel  int
	Effective      Limits
}

// Lease holds one active concurrency slot and one set of quota reservations.
// Finish is idempotent. If it is never called, reservations and concurrency
// remain held, so callers should normally defer a conservative Finish.
type Lease struct {
	coordinator *Coordinator
	id          uint64
	once        sync.Once
	err         error
}

func (lease *Lease) Finish(ctx context.Context, outcome Outcome) error {
	if lease == nil || lease.coordinator == nil {
		return nil
	}
	lease.once.Do(func() { lease.err = lease.coordinator.finish(ctx, lease.id, outcome) })
	return lease.err
}
