package slowmode

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

func TestConfigAndImpossibleRequest(t *testing.T) {
	_, err := NewCoordinator(context.Background(), Config{}, Dependencies{})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewCoordinator() error = %v, want invalid config", err)
	}
	config := testConfig(Limits{TPM: 10, MaxConcurrent: 1})
	config.SafetyPercent = 80
	coordinator, err := NewCoordinator(context.Background(), config, Dependencies{Jitter: noJitter})
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.Admit(context.Background(), Request{InputTokens: 9})
	if !errors.Is(err, ErrImpossibleRequest) {
		t.Fatalf("Admit() error = %v, want impossible request after safety margin", err)
	}
}

func TestRollingRPMWaitsForFullSixtySecondWindow(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))
	coordinator := newTestCoordinator(t, Limits{RPM: 1, MaxConcurrent: 2}, clock)
	lease, err := coordinator.Admit(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Finish(context.Background(), Outcome{Accounting: AccountingCounted}); err != nil {
		t.Fatal(err)
	}

	result := make(chan admissionResult, 1)
	go admitInto(coordinator, context.Background(), Request{}, result)
	waitForWaiting(t, coordinator, 1)
	waitForTimers(t, clock, 1)
	clock.Advance(59 * time.Second)
	assertNoAdmission(t, result)
	clock.Advance(time.Second)
	second := receiveAdmission(t, result)
	if second.err != nil {
		t.Fatal(second.err)
	}
	_ = second.lease.Finish(context.Background(), Outcome{})
}

func TestTPMReconcileAndProvenRefund(t *testing.T) {
	coordinator := newTestCoordinator(t, Limits{TPM: 10, RPD: 10, MaxConcurrent: 2}, nil)
	first, err := coordinator.Admit(context.Background(), Request{InputTokens: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Finish(context.Background(), Outcome{Accounting: AccountingCounted, ActualInputTokens: 3, HasActualInputTokens: true}); err != nil {
		t.Fatal(err)
	}
	second, err := coordinator.Admit(context.Background(), Request{InputTokens: 7})
	if err != nil {
		t.Fatalf("reconciled admission: %v", err)
	}
	if err := second.Finish(context.Background(), Outcome{Accounting: AccountingNotCounted}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := coordinator.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WindowRequests != 1 || snapshot.WindowTokens != 3 || snapshot.DailyRequests != 1 {
		t.Fatalf("snapshot after refund = %+v", snapshot)
	}
}

func TestConcurrencyWaitIsCancellableAndFinishWakesNext(t *testing.T) {
	coordinator := newTestCoordinator(t, Limits{MaxConcurrent: 1}, nil)
	first, err := coordinator.Admit(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan admissionResult, 1)
	go admitInto(coordinator, cancelCtx, Request{}, cancelled)
	waitForWaiting(t, coordinator, 1)
	cancel()
	if result := receiveAdmission(t, cancelled); !errors.Is(result.err, context.Canceled) {
		t.Fatalf("cancelled Admit() error = %v", result.err)
	}

	next := make(chan admissionResult, 1)
	go admitInto(coordinator, context.Background(), Request{}, next)
	waitForWaiting(t, coordinator, 1)
	_ = first.Finish(context.Background(), Outcome{})
	result := receiveAdmission(t, next)
	if result.err != nil {
		t.Fatal(result.err)
	}
	_ = result.lease.Finish(context.Background(), Outcome{})
}

func TestPriorityFIFOAndAging(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))
	coordinator := newTestCoordinator(t, Limits{MaxConcurrent: 1}, clock)
	coordinator.config.AgingInterval = time.Second
	holder, _ := coordinator.Admit(context.Background(), Request{})

	results := make(chan namedAdmission, 2)
	go namedAdmit(coordinator, "maintenance", Request{Priority: PriorityMaintenance}, results)
	waitForWaiting(t, coordinator, 1)
	clock.Advance(3 * time.Second)
	go namedAdmit(coordinator, "foreground", Request{Priority: PriorityForeground}, results)
	waitForWaiting(t, coordinator, 2)
	_ = holder.Finish(context.Background(), Outcome{})
	first := receiveNamedAdmission(t, results)
	if first.name != "maintenance" {
		t.Fatalf("aged admission = %q, want maintenance", first.name)
	}
	_ = first.lease.Finish(context.Background(), Outcome{})
	second := receiveNamedAdmission(t, results)
	if second.name != "foreground" {
		t.Fatalf("second admission = %q", second.name)
	}
	_ = second.lease.Finish(context.Background(), Outcome{})

	// Without aging, a later foreground request jumps an older background one.
	coordinator = newTestCoordinator(t, Limits{MaxConcurrent: 1}, nil)
	holder, _ = coordinator.Admit(context.Background(), Request{})
	results = make(chan namedAdmission, 2)
	go namedAdmit(coordinator, "background", Request{Priority: PriorityBackground}, results)
	waitForWaiting(t, coordinator, 1)
	go namedAdmit(coordinator, "foreground", Request{}, results)
	waitForWaiting(t, coordinator, 2)
	_ = holder.Finish(context.Background(), Outcome{})
	first = receiveNamedAdmission(t, results)
	if first.name != "foreground" {
		t.Fatalf("priority admission = %q, want foreground", first.name)
	}
	_ = first.lease.Finish(context.Background(), Outcome{})
	second = receiveNamedAdmission(t, results)
	_ = second.lease.Finish(context.Background(), Outcome{})
}

func TestInteractiveRPDReserve(t *testing.T) {
	coordinator := newTestCoordinator(t, Limits{RPD: 10, MaxConcurrent: 1}, nil)
	coordinator.config.InteractiveReservePercent = 25
	for index := 0; index < 7; index++ {
		lease, err := coordinator.Admit(context.Background(), Request{Priority: PriorityBackground})
		if err != nil {
			t.Fatalf("background request %d: %v", index, err)
		}
		_ = lease.Finish(context.Background(), Outcome{})
	}
	_, err := coordinator.Admit(context.Background(), Request{Priority: PriorityBackground})
	if !errors.Is(err, ErrInteractiveReserve) {
		t.Fatalf("background error = %v, want reserve", err)
	}
	for index := 0; index < 3; index++ {
		lease, admitErr := coordinator.Admit(context.Background(), Request{Priority: PriorityForeground})
		if admitErr != nil {
			t.Fatalf("foreground request %d: %v", index, admitErr)
		}
		_ = lease.Finish(context.Background(), Outcome{})
	}
	_, err = coordinator.Admit(context.Background(), Request{})
	if !errors.Is(err, ErrDailyQuota) {
		t.Fatalf("foreground error = %v, want daily quota", err)
	}
}

func TestPacificDateRolloverAndDST(t *testing.T) {
	if got := PacificDate(time.Date(2026, 3, 8, 7, 59, 0, 0, time.UTC)); got != "2026-03-07" {
		t.Fatalf("date before DST midnight = %q", got)
	}
	if got := PacificDate(time.Date(2026, 3, 8, 8, 1, 0, 0, time.UTC)); got != "2026-03-08" {
		t.Fatalf("date after DST midnight = %q", got)
	}
	clock := newFakeClock(time.Date(2026, 9, 4, 6, 59, 0, 0, time.UTC))
	coordinator := newTestCoordinator(t, Limits{RPD: 1, MaxConcurrent: 1}, clock)
	lease, _ := coordinator.Admit(context.Background(), Request{})
	_ = lease.Finish(context.Background(), Outcome{})
	if _, err := coordinator.Admit(context.Background(), Request{}); !errors.Is(err, ErrDailyQuota) {
		t.Fatalf("same-day admission = %v", err)
	}
	clock.Advance(2 * time.Minute)
	lease, err := coordinator.Admit(context.Background(), Request{})
	if err != nil {
		t.Fatalf("next Pacific day admission: %v", err)
	}
	_ = lease.Finish(context.Background(), Outcome{})
}

func TestDailyExhaustionPersistsAcrossRestart(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))
	ledger := newMemoryLedger()
	config := testConfig(Limits{RPD: 10, MaxConcurrent: 1})
	first, err := NewCoordinator(context.Background(), config, Dependencies{Clock: clock, Ledger: ledger, Jitter: noJitter})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = first.ApplyFeedback(context.Background(), Feedback{Kind: FeedbackDailyQuota}); err != nil {
		t.Fatal(err)
	}
	second, err := NewCoordinator(context.Background(), config, Dependencies{Clock: clock, Ledger: ledger, Jitter: noJitter})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = second.Admit(context.Background(), Request{}); !errors.Is(err, ErrDailyQuota) {
		t.Fatalf("restarted admission = %v", err)
	}
}

func TestFeedbackBackoffJitterAndAdaptiveRecovery(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))
	config := testConfig(Limits{RPM: 16, TPM: 16, MaxConcurrent: 1})
	config.BaseBackoff, config.MaxBackoff = time.Second, 10*time.Second
	coordinator, err := NewCoordinator(context.Background(), config, Dependencies{
		Clock: clock, Jitter: func(bound time.Duration) time.Duration { return bound },
	})
	if err != nil {
		t.Fatal(err)
	}
	feedback, err := coordinator.ApplyFeedback(context.Background(), Feedback{Kind: FeedbackShortWindow})
	if err != nil {
		t.Fatal(err)
	}
	if got := feedback.CooldownUntil.Sub(clock.Now()); got != 1250*time.Millisecond {
		t.Fatalf("first cooldown = %v", got)
	}
	feedback, err = coordinator.ApplyFeedback(context.Background(), Feedback{Kind: FeedbackAmbiguous})
	if err != nil {
		t.Fatal(err)
	}
	if got := feedback.CooldownUntil.Sub(clock.Now()); got != 2500*time.Millisecond || feedback.AdaptiveLevel != 1 {
		t.Fatalf("ambiguous feedback = %+v delay=%v", feedback, got)
	}
	if _, err := coordinator.Admit(context.Background(), Request{InputTokens: 9}); !errors.Is(err, ErrImpossibleRequest) {
		t.Fatalf("adaptive impossible error = %v", err)
	}
	coordinator.RecordSuccess()
	snapshot, _ := coordinator.Snapshot(context.Background())
	if snapshot.AdaptiveLevel != 0 || snapshot.Effective.TPM != 16 {
		t.Fatalf("snapshot after success = %+v", snapshot)
	}
}

func TestWarmupReservationsAndCooldown(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))
	warmup := staticWarmup{state: WarmupState{
		Reservations:  []UsagePoint{{At: clock.Now().Add(-10 * time.Second), InputTokens: 2}},
		CooldownUntil: clock.Now().Add(5 * time.Second),
	}}
	config := testConfig(Limits{RPM: 1, TPM: 10, MaxConcurrent: 1})
	coordinator, err := NewCoordinator(context.Background(), config, Dependencies{Clock: clock, Jitter: noJitter, Warmup: warmup})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan admissionResult, 1)
	go admitInto(coordinator, context.Background(), Request{InputTokens: 1}, result)
	waitForWaiting(t, coordinator, 1)
	waitForTimers(t, clock, 1)
	clock.Advance(50 * time.Second)
	admitted := receiveAdmission(t, result)
	if admitted.err != nil {
		t.Fatal(admitted.err)
	}
	_ = admitted.lease.Finish(context.Background(), Outcome{})
}

func TestDefaultPriority(t *testing.T) {
	if got := DefaultPriority(agent.ModelRequest{}); got != PriorityForeground {
		t.Fatalf("default priority = %v", got)
	}
	request := agent.ModelRequest{Metadata: map[string]string{MetadataPriorityKey: MetadataPriorityMaintenance}}
	if got := DefaultPriority(request); got != PriorityMaintenance {
		t.Fatalf("metadata priority = %v", got)
	}
	request.Metadata[MetadataPriorityKey] = "untrusted-value"
	if got := DefaultPriority(request); got != PriorityForeground {
		t.Fatalf("unknown metadata priority = %v", got)
	}
}

type admissionResult struct {
	lease *Lease
	err   error
}

type namedAdmission struct {
	name  string
	lease *Lease
	err   error
}

func admitInto(coordinator *Coordinator, ctx context.Context, request Request, result chan<- admissionResult) {
	lease, err := coordinator.Admit(ctx, request)
	result <- admissionResult{lease: lease, err: err}
}

func namedAdmit(coordinator *Coordinator, name string, request Request, result chan<- namedAdmission) {
	lease, err := coordinator.Admit(context.Background(), request)
	result <- namedAdmission{name: name, lease: lease, err: err}
}

func receiveAdmission(t *testing.T, result <-chan admissionResult) admissionResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for admission")
		return admissionResult{}
	}
}

func receiveNamedAdmission(t *testing.T, result <-chan namedAdmission) namedAdmission {
	t.Helper()
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for named admission")
		return namedAdmission{}
	}
}

func assertNoAdmission(t *testing.T, result <-chan admissionResult) {
	t.Helper()
	select {
	case got := <-result:
		t.Fatalf("unexpected admission: %+v", got)
	case <-time.After(10 * time.Millisecond):
	}
}

func waitForWaiting(t *testing.T, coordinator *Coordinator, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := coordinator.Snapshot(context.Background())
		if err == nil && snapshot.Waiting == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("waiting count did not reach %d", count)
}

func waitForTimers(t *testing.T, clock *fakeClock, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if clock.TimerCount() >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timer count did not reach %d", count)
}

func testConfig(limits Limits) Config {
	return Config{Scope: "provider/model", Limits: limits, SafetyPercent: 100, AgingInterval: time.Hour}
}

func newTestCoordinator(t *testing.T, limits Limits, clock Clock) *Coordinator {
	t.Helper()
	coordinator, err := NewCoordinator(context.Background(), testConfig(limits), Dependencies{Clock: clock, Jitter: noJitter})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func noJitter(time.Duration) time.Duration { return 0 }

type memoryLedger struct {
	mu      sync.Mutex
	buckets map[string]DailyUsage
}

func newMemoryLedger() *memoryLedger { return &memoryLedger{buckets: make(map[string]DailyUsage)} }

func (ledger *memoryLedger) key(scope, date string) string { return scope + "/" + date }

func (ledger *memoryLedger) Load(_ context.Context, scope, date string) (DailyUsage, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.buckets[ledger.key(scope, date)], nil
}

func (ledger *memoryLedger) Save(_ context.Context, scope, date string, usage DailyUsage) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	ledger.buckets[ledger.key(scope, date)] = usage
	return nil
}

type staticWarmup struct{ state WarmupState }

func (warmup staticWarmup) LoadWarmup(context.Context, WarmupQuery) (WarmupState, error) {
	return warmup.state, nil
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	clock    *fakeClock
	deadline time.Time
	channel  chan time.Time
	stopped  bool
	fired    bool
}

func newFakeClock(now time.Time) *fakeClock { return &fakeClock{now: now} }

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) NewTimer(delay time.Duration) Timer {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	timer := &fakeTimer{clock: clock, deadline: clock.now.Add(delay), channel: make(chan time.Time, 1)}
	clock.timers = append(clock.timers, timer)
	clock.fireLocked(timer)
	return timer
}

func (clock *fakeClock) Advance(delay time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(delay)
	for _, timer := range clock.timers {
		clock.fireLocked(timer)
	}
	clock.mu.Unlock()
}

func (clock *fakeClock) fireLocked(timer *fakeTimer) {
	if !timer.stopped && !timer.fired && !timer.deadline.After(clock.now) {
		timer.fired = true
		timer.channel <- clock.now
	}
}

func (clock *fakeClock) TimerCount() int {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	count := 0
	for _, timer := range clock.timers {
		if !timer.stopped && !timer.fired {
			count++
		}
	}
	return count
}

func (timer *fakeTimer) C() <-chan time.Time { return timer.channel }

func (timer *fakeTimer) Stop() bool {
	timer.clock.mu.Lock()
	defer timer.clock.mu.Unlock()
	wasActive := !timer.stopped && !timer.fired
	timer.stopped = true
	return wasActive
}

func ExampleBackend() {
	_ = Backend{
		Estimate: func(context.Context, agent.ModelRequest) (int64, error) { return 42, nil },
		Classify: DefaultPriority,
	}
	fmt.Println("provider-neutral wrapper")
	// Output: provider-neutral wrapper
}
