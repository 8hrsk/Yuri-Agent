package slowmode

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sort"
	"sync"
	"time"
	_ "time/tzdata"
)

var pacificLocation = mustPacificLocation()

type reservation struct {
	id     uint64
	at     time.Time
	tokens int64
	date   string
	active bool
}

type waiter struct {
	seq      uint64
	request  Request
	enqueued time.Time
}

// Coordinator owns admission state for one shared provider quota scope.
type Coordinator struct {
	config Config
	clock  Clock
	jitter Jitter
	ledger DailyLedger

	mu            sync.Mutex
	notify        chan struct{}
	nextID        uint64
	reservations  []*reservation
	byID          map[uint64]*reservation
	waiters       []*waiter
	dailyDate     string
	daily         DailyUsage
	active        int
	cooldownUntil time.Time
	shortFailures int
	adaptiveLevel int
}

func NewCoordinator(ctx context.Context, config Config, dependencies Dependencies) (*Coordinator, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrInvalidConfig)
	}
	var err error
	config, err = config.normalized()
	if err != nil {
		return nil, err
	}
	clock := dependencies.Clock
	if clock == nil {
		clock = realClock{}
	}
	jitter := dependencies.Jitter
	if jitter == nil {
		jitter = randomJitter
	}
	now := clock.Now()
	date := PacificDate(now)
	daily := DailyUsage{}
	if dependencies.Ledger != nil {
		daily, err = dependencies.Ledger.Load(ctx, config.Scope, date)
		if err != nil {
			return nil, fmt.Errorf("load daily quota ledger: %w", err)
		}
		if daily.Requests < 0 {
			return nil, fmt.Errorf("%w: ledger returned negative daily requests", ErrInvalidConfig)
		}
	}
	coordinator := &Coordinator{
		config: config, clock: clock, jitter: jitter, ledger: dependencies.Ledger,
		notify: make(chan struct{}), byID: make(map[uint64]*reservation),
		dailyDate: date, daily: daily,
	}
	if dependencies.Warmup != nil {
		state, loadErr := dependencies.Warmup.LoadWarmup(ctx, WarmupQuery{Scope: config.Scope, Since: now.Add(-rollingWindow), Now: now})
		if loadErr != nil {
			return nil, fmt.Errorf("load slow-mode warmup: %w", loadErr)
		}
		if state.CooldownUntil.After(now) {
			coordinator.cooldownUntil = state.CooldownUntil
		}
		for _, point := range state.Reservations {
			if point.InputTokens < 0 || point.At.After(now) {
				return nil, fmt.Errorf("%w: invalid warmup reservation", ErrInvalidConfig)
			}
			if !point.At.After(now.Add(-rollingWindow)) {
				continue
			}
			coordinator.nextID++
			entry := &reservation{id: coordinator.nextID, at: point.At, tokens: point.InputTokens, date: date}
			coordinator.reservations = append(coordinator.reservations, entry)
		}
		sort.Slice(coordinator.reservations, func(left, right int) bool {
			return coordinator.reservations[left].at.Before(coordinator.reservations[right].at)
		})
	}
	return coordinator, nil
}

// Admit waits until request can reserve all configured quota dimensions and a
// concurrency slot. Waiting is cancellable through ctx.
func (coordinator *Coordinator) Admit(ctx context.Context, request Request) (*Lease, error) {
	if coordinator == nil {
		return nil, fmt.Errorf("%w: nil coordinator", ErrInvalidRequest)
	}
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrInvalidRequest)
	}
	if request.InputTokens < 0 || !request.Priority.valid() {
		return nil, fmt.Errorf("%w: invalid tokens or priority", ErrInvalidRequest)
	}
	effective := coordinator.effectiveLimits(0)
	if effective.TPM > 0 && request.InputTokens > effective.TPM {
		return nil, &AdmissionError{Kind: ErrImpossibleRequest, Dimension: "TPM", Requested: request.InputTokens, Limit: effective.TPM}
	}

	coordinator.mu.Lock()
	coordinator.nextID++
	wait := &waiter{seq: coordinator.nextID, request: request, enqueued: coordinator.clock.Now()}
	coordinator.waiters = append(coordinator.waiters, wait)
	coordinator.signalLocked()
	coordinator.mu.Unlock()

	for {
		coordinator.mu.Lock()
		now := coordinator.clock.Now()
		if err := coordinator.ensureDayLocked(ctx, now); err != nil {
			coordinator.removeWaiterLocked(wait)
			coordinator.mu.Unlock()
			return nil, err
		}
		coordinator.expireLocked(now)
		selected := coordinator.selectedWaiterLocked(now)
		if selected == wait {
			lease, blockedFor, err := coordinator.tryAdmitLocked(ctx, wait, now)
			if err != nil {
				coordinator.removeWaiterLocked(wait)
				coordinator.mu.Unlock()
				return nil, err
			}
			if lease != nil {
				coordinator.removeWaiterLocked(wait)
				coordinator.mu.Unlock()
				return lease, nil
			}
			notify := coordinator.notify
			coordinator.mu.Unlock()
			if err := coordinator.wait(ctx, notify, blockedFor); err != nil {
				coordinator.cancelWaiter(wait)
				return nil, err
			}
			continue
		}
		notify := coordinator.notify
		agingDelay := coordinator.nextAgingDelayLocked(wait, now)
		coordinator.mu.Unlock()
		if err := coordinator.wait(ctx, notify, agingDelay); err != nil {
			coordinator.cancelWaiter(wait)
			return nil, err
		}
	}
}

func (coordinator *Coordinator) tryAdmitLocked(ctx context.Context, wait *waiter, now time.Time) (*Lease, time.Duration, error) {
	effective := coordinator.effectiveLimits(coordinator.adaptiveLevel)
	if effective.TPM > 0 && wait.request.InputTokens > effective.TPM {
		return nil, 0, &AdmissionError{Kind: ErrImpossibleRequest, Dimension: "adaptive TPM", Requested: wait.request.InputTokens, Limit: effective.TPM}
	}
	resetAt := nextPacificMidnight(now)
	if coordinator.daily.Exhausted || effective.RPD > 0 && coordinator.daily.Requests >= effective.RPD {
		return nil, 0, &AdmissionError{Kind: ErrDailyQuota, ResetAt: resetAt}
	}
	if wait.request.Priority != PriorityForeground && effective.RPD > 0 {
		reserve := reserveRequests(effective.RPD, coordinator.config.InteractiveReservePercent)
		backgroundLimit := effective.RPD - reserve
		if coordinator.daily.Requests >= backgroundLimit {
			return nil, 0, &AdmissionError{Kind: ErrInteractiveReserve, ResetAt: resetAt, Limit: backgroundLimit}
		}
	}
	if now.Before(coordinator.cooldownUntil) {
		return nil, coordinator.cooldownUntil.Sub(now), nil
	}
	if coordinator.active >= effective.MaxConcurrent {
		return nil, 0, nil
	}
	requests, tokens := coordinator.windowUsageLocked()
	waitFor := time.Duration(0)
	if effective.RPM > 0 && requests+1 > effective.RPM {
		waitFor = coordinator.delayUntilRequestsFitLocked(now, effective.RPM)
	}
	if effective.TPM > 0 && tokens+wait.request.InputTokens > effective.TPM {
		delay := coordinator.delayUntilTokensFitLocked(now, effective.TPM, wait.request.InputTokens)
		if waitFor == 0 || delay > waitFor {
			waitFor = delay
		}
	}
	if waitFor > 0 {
		return nil, waitFor, nil
	}

	coordinator.nextID++
	entry := &reservation{id: coordinator.nextID, at: now, tokens: wait.request.InputTokens, date: coordinator.dailyDate, active: true}
	coordinator.daily.Requests++
	if err := coordinator.saveDailyLocked(ctx); err != nil {
		coordinator.daily.Requests--
		return nil, 0, fmt.Errorf("save daily quota ledger: %w", err)
	}
	coordinator.reservations = append(coordinator.reservations, entry)
	coordinator.byID[entry.id] = entry
	coordinator.active++
	coordinator.signalLocked()
	return &Lease{coordinator: coordinator, id: entry.id}, 0, nil
}

func (coordinator *Coordinator) finish(ctx context.Context, id uint64, outcome Outcome) error {
	if ctx == nil {
		ctx = context.Background()
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	entry := coordinator.byID[id]
	if entry == nil {
		return nil
	}
	delete(coordinator.byID, id)
	if entry.active {
		entry.active = false
		if coordinator.active > 0 {
			coordinator.active--
		}
	}
	var result error
	if outcome.Accounting == AccountingNotCounted {
		if err := coordinator.refundDailyLocked(ctx, entry.date); err != nil {
			result = fmt.Errorf("refund daily quota ledger: %w", err)
		} else {
			coordinator.removeReservationLocked(entry)
		}
	} else if outcome.HasActualInputTokens && outcome.ActualInputTokens >= 0 {
		entry.tokens = outcome.ActualInputTokens
	}
	coordinator.signalLocked()
	return result
}

// ApplyFeedback updates cooldown or daily-exhaustion state after a provider
// response. Short-window and ambiguous feedback include bounded jitter.
func (coordinator *Coordinator) ApplyFeedback(ctx context.Context, feedback Feedback) (FeedbackResult, error) {
	if coordinator == nil || ctx == nil {
		return FeedbackResult{}, fmt.Errorf("%w: nil coordinator or context", ErrInvalidRequest)
	}
	if feedback.RetryAfter < 0 {
		return FeedbackResult{}, fmt.Errorf("%w: negative retry-after", ErrInvalidRequest)
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	now := coordinator.clock.Now()
	if err := coordinator.ensureDayLocked(ctx, now); err != nil {
		return FeedbackResult{}, err
	}
	switch feedback.Kind {
	case FeedbackDailyQuota:
		previous := coordinator.daily
		coordinator.daily.Exhausted = true
		if err := coordinator.saveDailyLocked(ctx); err != nil {
			coordinator.daily = previous
			return FeedbackResult{}, fmt.Errorf("save daily quota ledger: %w", err)
		}
	case FeedbackShortWindow:
		coordinator.shortFailures++
		coordinator.extendCooldownLocked(now, feedback.RetryAfter, coordinator.shortFailures)
	case FeedbackAmbiguous:
		coordinator.shortFailures++
		if coordinator.adaptiveLevel < 3 {
			coordinator.adaptiveLevel++
		}
		coordinator.extendCooldownLocked(now, feedback.RetryAfter, coordinator.shortFailures)
	default:
		return FeedbackResult{}, fmt.Errorf("%w: invalid feedback kind", ErrInvalidRequest)
	}
	coordinator.signalLocked()
	return FeedbackResult{CooldownUntil: coordinator.cooldownUntil, DailyExhausted: coordinator.daily.Exhausted, AdaptiveLevel: coordinator.adaptiveLevel}, nil
}

// RecordSuccess clears short-window backoff and relaxes one adaptive level.
// Ambiguous feedback therefore requires successful requests to return
// gradually to the configured envelope.
func (coordinator *Coordinator) RecordSuccess() {
	if coordinator == nil {
		return
	}
	coordinator.mu.Lock()
	coordinator.shortFailures = 0
	if coordinator.adaptiveLevel > 0 {
		coordinator.adaptiveLevel--
	}
	coordinator.signalLocked()
	coordinator.mu.Unlock()
}

func (coordinator *Coordinator) Snapshot(ctx context.Context) (Snapshot, error) {
	if coordinator == nil || ctx == nil {
		return Snapshot{}, fmt.Errorf("%w: nil coordinator or context", ErrInvalidRequest)
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	now := coordinator.clock.Now()
	if err := coordinator.ensureDayLocked(ctx, now); err != nil {
		return Snapshot{}, err
	}
	coordinator.expireLocked(now)
	requests, tokens := coordinator.windowUsageLocked()
	return Snapshot{
		Scope: coordinator.config.Scope, At: now, WindowRequests: requests, WindowTokens: tokens,
		DailyRequests: coordinator.daily.Requests, DailyDate: coordinator.dailyDate, DailyExhausted: coordinator.daily.Exhausted,
		Active: coordinator.active, Waiting: len(coordinator.waiters), CooldownUntil: coordinator.cooldownUntil,
		AdaptiveLevel: coordinator.adaptiveLevel, Effective: coordinator.effectiveLimits(coordinator.adaptiveLevel),
	}, nil
}

func (coordinator *Coordinator) effectiveLimits(adaptiveLevel int) Limits {
	limits := coordinator.config.Limits
	limits.RPM = applyPercent(limits.RPM, coordinator.config.SafetyPercent)
	limits.TPM = applyPercent(limits.TPM, coordinator.config.SafetyPercent)
	limits.RPD = applyPercent(limits.RPD, coordinator.config.SafetyPercent)
	for range adaptiveLevel {
		limits.RPM = halvePositive(limits.RPM)
		limits.TPM = halvePositive(limits.TPM)
	}
	return limits
}

func applyPercent(value int64, percent int) int64 {
	if value <= 0 {
		return 0
	}
	// Split quotient and remainder so a valid int64 limit cannot overflow
	// during percentage application.
	result := value/100*int64(percent) + value%100*int64(percent)/100
	if result < 1 {
		return 1
	}
	return result
}

func halvePositive(value int64) int64 {
	if value <= 1 {
		return value
	}
	return value / 2
}

func reserveRequests(limit int64, percent int) int64 {
	if limit <= 0 || percent <= 0 {
		return 0
	}
	return (limit*int64(percent) + 99) / 100
}

func (coordinator *Coordinator) expireLocked(now time.Time) {
	cutoff := now.Add(-rollingWindow)
	kept := coordinator.reservations[:0]
	for _, entry := range coordinator.reservations {
		if entry.at.After(cutoff) {
			kept = append(kept, entry)
		}
	}
	coordinator.reservations = kept
}

func (coordinator *Coordinator) windowUsageLocked() (int64, int64) {
	var tokens int64
	for _, entry := range coordinator.reservations {
		tokens += entry.tokens
	}
	return int64(len(coordinator.reservations)), tokens
}

func (coordinator *Coordinator) selectedWaiterLocked(now time.Time) *waiter {
	var selected *waiter
	selectedPriority := int(^uint(0) >> 1)
	for _, candidate := range coordinator.waiters {
		priority := coordinator.effectivePriority(candidate, now)
		if selected == nil || priority < selectedPriority || priority == selectedPriority && candidate.seq < selected.seq {
			selected, selectedPriority = candidate, priority
		}
	}
	return selected
}

func (coordinator *Coordinator) effectivePriority(wait *waiter, now time.Time) int {
	priority := int(wait.request.Priority)
	if priority == 0 || !now.After(wait.enqueued) {
		return priority
	}
	promotions := int(now.Sub(wait.enqueued) / coordinator.config.AgingInterval)
	if promotions >= priority {
		return 0
	}
	return priority - promotions
}

func (coordinator *Coordinator) nextAgingDelayLocked(wait *waiter, now time.Time) time.Duration {
	priority := int(wait.request.Priority)
	promotions := 0
	if now.After(wait.enqueued) {
		promotions = int(now.Sub(wait.enqueued) / coordinator.config.AgingInterval)
	}
	if promotions >= priority {
		return 0
	}
	next := wait.enqueued.Add(time.Duration(promotions+1) * coordinator.config.AgingInterval)
	if !next.After(now) {
		return time.Nanosecond
	}
	return next.Sub(now)
}

func (coordinator *Coordinator) delayUntilRequestsFitLocked(now time.Time, limit int64) time.Duration {
	remove := int64(len(coordinator.reservations)) + 1 - limit
	if remove < 1 {
		return 0
	}
	return positiveDelay(coordinator.reservations[remove-1].at.Add(rollingWindow).Sub(now))
}

func (coordinator *Coordinator) delayUntilTokensFitLocked(now time.Time, limit, requested int64) time.Duration {
	_, tokens := coordinator.windowUsageLocked()
	for _, entry := range coordinator.reservations {
		tokens -= entry.tokens
		if tokens+requested <= limit {
			return positiveDelay(entry.at.Add(rollingWindow).Sub(now))
		}
	}
	return rollingWindow
}

func positiveDelay(delay time.Duration) time.Duration {
	if delay <= 0 {
		return time.Nanosecond
	}
	return delay
}

func (coordinator *Coordinator) wait(ctx context.Context, notify <-chan struct{}, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-notify:
			return nil
		}
	}
	timer := coordinator.clock.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-notify:
		return nil
	case <-timer.C():
		return nil
	}
}

func (coordinator *Coordinator) cancelWaiter(wait *waiter) {
	coordinator.mu.Lock()
	coordinator.removeWaiterLocked(wait)
	coordinator.mu.Unlock()
}

func (coordinator *Coordinator) removeWaiterLocked(target *waiter) {
	for index, candidate := range coordinator.waiters {
		if candidate == target {
			coordinator.waiters = append(coordinator.waiters[:index], coordinator.waiters[index+1:]...)
			coordinator.signalLocked()
			return
		}
	}
}

func (coordinator *Coordinator) removeReservationLocked(target *reservation) {
	for index, candidate := range coordinator.reservations {
		if candidate == target {
			coordinator.reservations = append(coordinator.reservations[:index], coordinator.reservations[index+1:]...)
			return
		}
	}
}

func (coordinator *Coordinator) signalLocked() {
	close(coordinator.notify)
	coordinator.notify = make(chan struct{})
}

func (coordinator *Coordinator) ensureDayLocked(ctx context.Context, now time.Time) error {
	date := PacificDate(now)
	if date == coordinator.dailyDate {
		return nil
	}
	usage := DailyUsage{}
	if coordinator.ledger != nil {
		loaded, err := coordinator.ledger.Load(ctx, coordinator.config.Scope, date)
		if err != nil {
			return fmt.Errorf("load daily quota ledger: %w", err)
		}
		if loaded.Requests < 0 {
			return fmt.Errorf("%w: ledger returned negative daily requests", ErrInvalidConfig)
		}
		usage = loaded
	}
	coordinator.dailyDate, coordinator.daily = date, usage
	coordinator.signalLocked()
	return nil
}

func (coordinator *Coordinator) saveDailyLocked(ctx context.Context) error {
	if coordinator.ledger == nil {
		return nil
	}
	return coordinator.ledger.Save(ctx, coordinator.config.Scope, coordinator.dailyDate, coordinator.daily)
}

func (coordinator *Coordinator) refundDailyLocked(ctx context.Context, date string) error {
	if date == coordinator.dailyDate {
		previous := coordinator.daily
		if coordinator.daily.Requests > 0 {
			coordinator.daily.Requests--
		}
		if err := coordinator.saveDailyLocked(ctx); err != nil {
			coordinator.daily = previous
			return err
		}
		return nil
	}
	if coordinator.ledger == nil {
		return nil
	}
	usage, err := coordinator.ledger.Load(ctx, coordinator.config.Scope, date)
	if err != nil {
		return err
	}
	if usage.Requests > 0 {
		usage.Requests--
	}
	return coordinator.ledger.Save(ctx, coordinator.config.Scope, date, usage)
}

func (coordinator *Coordinator) extendCooldownLocked(now time.Time, retryAfter time.Duration, failures int) {
	delay := retryAfter
	if delay <= 0 {
		delay = coordinator.config.BaseBackoff
		for step := 1; step < failures && delay < coordinator.config.MaxBackoff; step++ {
			if delay > coordinator.config.MaxBackoff/2 {
				delay = coordinator.config.MaxBackoff
				break
			}
			delay *= 2
		}
	}
	if delay > coordinator.config.MaxBackoff {
		delay = coordinator.config.MaxBackoff
	}
	room := coordinator.config.MaxBackoff - delay
	jitterBound := delay / 4
	if jitterBound > room {
		jitterBound = room
	}
	if jitterBound > 0 {
		jitter := coordinator.jitter(jitterBound)
		if jitter < 0 {
			jitter = 0
		}
		if jitter > jitterBound {
			jitter = jitterBound
		}
		delay += jitter
	}
	until := now.Add(delay)
	if until.After(coordinator.cooldownUntil) {
		coordinator.cooldownUntil = until
	}
}

// PacificDate returns the RPD ledger key for an instant.
func PacificDate(now time.Time) string { return now.In(pacificLocation).Format("2006-01-02") }

func nextPacificMidnight(now time.Time) time.Time {
	local := now.In(pacificLocation)
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, pacificLocation)
}

func mustPacificLocation() *time.Location {
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		panic(err)
	}
	return location
}

func randomJitter(upperBound time.Duration) time.Duration {
	if upperBound <= 0 {
		return 0
	}
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return 0
	}
	return time.Duration(binary.LittleEndian.Uint64(value[:]) % (uint64(upperBound) + 1))
}
