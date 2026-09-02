package desktop

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/config"
	googleaistudio "github.com/OrdoAI/yuri-agent/internal/providers/googleaistudio"
	securitykeyring "github.com/OrdoAI/yuri-agent/internal/security/keyring"
	"github.com/OrdoAI/yuri-agent/internal/slowmode"
)

func TestGoogleSlowModeSharesScopeAndInvalidatesChangedProfile(t *testing.T) {
	bridge := &Bridge{paths: config.Paths{DataDirectory: t.TempDir()}}
	provider := googleSlowModeTestProvider()
	first, err := bridge.googleSlowModeEntry(context.Background(), provider, "gemini-flash")
	if err != nil {
		t.Fatal(err)
	}
	second, err := bridge.googleSlowModeEntry(context.Background(), provider, "gemini-flash")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("same provider+model did not share coordinator entry")
	}
	otherModel, err := bridge.googleSlowModeEntry(context.Background(), provider, "gemini-pro")
	if err != nil {
		t.Fatal(err)
	}
	if otherModel == first {
		t.Fatal("different model unexpectedly shared quota scope")
	}

	provider.QuotaProfile.RPM = 5
	changed, err := bridge.googleSlowModeEntry(context.Background(), provider, "gemini-flash")
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("changed profile reused stale coordinator")
	}
	if len(bridge.googleSlowModes) != 1 {
		t.Fatalf("stale provider scopes were not invalidated: %d", len(bridge.googleSlowModes))
	}
}

func TestGoogleFreeTierUnknownLimitsIsSingleFlight(t *testing.T) {
	bridge := &Bridge{paths: config.Paths{DataDirectory: t.TempDir()}}
	entry, err := bridge.googleSlowModeEntry(context.Background(), googleSlowModeTestProvider(), "gemini-flash")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := entry.coordinator.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Effective.MaxConcurrent != 1 || snapshot.Effective.RPM != 0 || snapshot.Effective.TPM != 0 || snapshot.Effective.RPD != 0 {
		t.Fatalf("unknown Free Tier profile = %+v", snapshot.Effective)
	}
	first, err := entry.coordinator.Admit(context.Background(), slowmode.Request{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, admitErr := entry.coordinator.Admit(ctx, slowmode.Request{})
		result <- admitErr
	}()
	waitForGoogleSlowModeQueue(t, entry.coordinator, 1)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued admission error = %v", err)
	}
	_ = first.Finish(context.Background(), slowmode.Outcome{})
}

func TestGoogleConfiguredLimitsApplySafety(t *testing.T) {
	bridge := &Bridge{paths: config.Paths{DataDirectory: t.TempDir()}}
	provider := googleSlowModeTestProvider()
	provider.QuotaMode = config.ProviderQuotaCustom
	provider.QuotaProfile = config.ProviderQuotaProfile{
		RPM: 10, TPM: 1000, RPD: 100, MaxConcurrent: 3, SafetyPercent: 80, InteractiveReservePercent: 20,
	}
	entry, err := bridge.googleSlowModeEntry(context.Background(), provider, "gemini-flash")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := entry.coordinator.Snapshot(context.Background())
	if snapshot.Effective != (slowmode.Limits{RPM: 8, TPM: 800, RPD: 80, MaxConcurrent: 3}) || entry.effectiveTPM != 800 {
		t.Fatalf("effective profile = %+v threshold=%d", snapshot.Effective, entry.effectiveTPM)
	}
}

func TestGoogleSlowModeOffBypassesWrapper(t *testing.T) {
	bridge := &Bridge{paths: config.Paths{DataDirectory: t.TempDir()}}
	provider := googleSlowModeTestProvider()
	provider.QuotaMode = config.ProviderQuotaOff
	client := &fakeGoogleClient{}
	backend, err := bridge.googleBackendWithSlowMode(context.Background(), provider, "gemini-flash", client)
	if err != nil {
		t.Fatal(err)
	}
	if backend != client || len(bridge.googleSlowModes) != 0 {
		t.Fatalf("off mode returned %#v with %d registry entries", backend, len(bridge.googleSlowModes))
	}
}

func TestGoogleEstimatorCountsNearBoundaryAndMultimodalWithBoundedCache(t *testing.T) {
	client := &fakeGoogleClient{tokenCount: 37}
	cache := newGoogleTokenCountCache(2, time.Hour)
	estimator := &googleInputTokenEstimator{client: client, cache: cache, exactBoundary: 1000}
	localRequest := agent.ModelRequest{Model: "gemini", Messages: []agent.Message{{Role: agent.RoleUser, Content: "small"}}}
	local, err := estimator.Estimate(context.Background(), localRequest)
	if err != nil || local <= 0 || client.countCalls != 0 {
		t.Fatalf("local estimate=%d calls=%d err=%v", local, client.countCalls, err)
	}
	near := agent.ModelRequest{Model: "gemini", Messages: []agent.Message{{Role: agent.RoleUser, Content: string(make([]byte, 700))}}}
	if got, err := estimator.Estimate(context.Background(), near); err != nil || got != 37 {
		t.Fatalf("near-boundary estimate=%d err=%v", got, err)
	}
	if got, err := estimator.Estimate(context.Background(), near); err != nil || got != 37 || client.countCalls != 1 {
		t.Fatalf("cached estimate=%d calls=%d err=%v", got, client.countCalls, err)
	}
	multimodal := agent.ModelRequest{Model: "gemini", Messages: []agent.Message{{Role: agent.RoleUser, Content: "image", Parts: []agent.ContentPart{{Type: agent.ContentPartImage, MediaType: "image/png", Data: "AAAA"}}}}}
	if got, err := estimator.Estimate(context.Background(), multimodal); err != nil || got != 37 || client.countCalls != 2 {
		t.Fatalf("multimodal estimate=%d calls=%d err=%v", got, client.countCalls, err)
	}
	third := agent.ModelRequest{Model: "gemini", Messages: []agent.Message{{Role: agent.RoleUser, Parts: []agent.ContentPart{{Type: agent.ContentPartImage, MediaType: "image/jpeg", Data: "BBBB"}}}}}
	_, _ = estimator.Estimate(context.Background(), third)
	if len(cache.entries) > 2 || len(cache.order) > 2 {
		t.Fatalf("cache exceeded bound: entries=%d order=%d", len(cache.entries), len(cache.order))
	}
}

func TestGoogleEstimatorCountsAgainstCurrentTPMWindow(t *testing.T) {
	coordinator, err := slowmode.NewCoordinator(context.Background(), slowmode.Config{
		Scope: "google/model", Limits: slowmode.Limits{TPM: 1000, MaxConcurrent: 1}, SafetyPercent: 100,
	}, slowmode.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := coordinator.Admit(context.Background(), slowmode.Request{InputTokens: 650})
	if err != nil {
		t.Fatal(err)
	}
	_ = lease.Finish(context.Background(), slowmode.Outcome{Accounting: slowmode.AccountingCounted})
	client := &fakeGoogleClient{tokenCount: 41}
	estimator := &googleInputTokenEstimator{
		client: client, cache: newGoogleTokenCountCache(2, time.Hour), coordinator: coordinator,
	}
	request := agent.ModelRequest{Model: "gemini", Messages: []agent.Message{{Role: agent.RoleUser, Content: "small"}}}
	if local := conservativeGoogleInputTokens(request); local >= 700 {
		t.Fatalf("test request local estimate unexpectedly near empty boundary: %d", local)
	}
	if got, err := estimator.Estimate(context.Background(), request); err != nil || got != 41 || client.countCalls != 1 {
		t.Fatalf("window-aware estimate=%d calls=%d err=%v", got, client.countCalls, err)
	}
}

func TestGoogleFeedbackAndPriorityClassification(t *testing.T) {
	tests := []struct {
		reason googleaistudio.ErrorReason
		status int
		kind   slowmode.FeedbackKind
	}{
		{googleaistudio.ErrorReasonRateLimit, 429, slowmode.FeedbackShortWindow},
		{googleaistudio.ErrorReasonQuotaExhausted, 429, slowmode.FeedbackDailyQuota},
		{googleaistudio.ErrorReasonResourceExhausted, 429, slowmode.FeedbackAmbiguous},
		{googleaistudio.ErrorReasonUnknown, 429, slowmode.FeedbackAmbiguous},
		{googleaistudio.ErrorReasonUnknown, 503, slowmode.FeedbackAmbiguous},
	}
	for _, test := range tests {
		feedback, ok := googleSlowModeFeedback(&googleaistudio.Error{Reason: test.reason, StatusCode: test.status, RetryAfter: 2 * time.Second})
		if !ok || feedback.Kind != test.kind || feedback.RetryAfter != 2*time.Second {
			t.Fatalf("feedback for %s = %+v, %v", test.reason, feedback, ok)
		}
	}
	if _, ok := googleSlowModeFeedback(errors.New("not Google")); ok {
		t.Fatal("non-Google error classified as quota feedback")
	}
	if got := googleSlowModePriority(agent.ModelRequest{Metadata: map[string]string{"purpose": "memory_extraction"}}); got != slowmode.PriorityMaintenance {
		t.Fatalf("memory priority = %v", got)
	}
	if got := slowModePriorityForRunKind("background"); got != slowmode.MetadataPriorityBackground {
		t.Fatalf("background run metadata = %q", got)
	}
}

func TestGoogleRPDLedgerSurvivesCoordinatorRestart(t *testing.T) {
	dataDirectory := t.TempDir()
	provider := googleSlowModeTestProvider()
	provider.QuotaProfile = config.ProviderQuotaProfile{RPD: 3, MaxConcurrent: 1, SafetyPercent: 100}
	firstBridge := &Bridge{paths: config.Paths{DataDirectory: dataDirectory}}
	first, err := firstBridge.googleSlowModeEntry(context.Background(), provider, "gemini-flash")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := first.coordinator.Admit(context.Background(), slowmode.Request{})
	if err != nil {
		t.Fatal(err)
	}
	_ = lease.Finish(context.Background(), slowmode.Outcome{})

	secondBridge := &Bridge{paths: config.Paths{DataDirectory: dataDirectory}}
	second, err := secondBridge.googleSlowModeEntry(context.Background(), provider, "gemini-flash")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := second.coordinator.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DailyRequests != 1 {
		t.Fatalf("daily requests after restart = %d", snapshot.DailyRequests)
	}
	if !snapshot.CooldownUntil.After(snapshot.At) {
		t.Fatalf("restart did not preserve a conservative rolling-window cooldown: %+v", snapshot)
	}
	info, err := os.Stat(dataDirectory + "/" + googleQuotaLedgerFile)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("ledger permissions = %o", info.Mode().Perm())
	}
}

func TestChatBackendBuildsGoogleSlowModeWrapper(t *testing.T) {
	keyringBackend := &providerTestKeyring{values: map[string]string{"google-route:provider.google.api-key": "AIza-test"}}
	store, err := securitykeyring.NewWithBackend("google-route", keyringBackend)
	if err != nil {
		t.Fatal(err)
	}
	provider := googleSlowModeTestProvider()
	provider.CredentialRef = "provider.google.api-key"
	provider.Enabled = true
	client := &fakeGoogleClient{}
	bridge := &Bridge{
		paths: config.Paths{DataDirectory: t.TempDir()}, keyring: store,
		config: config.Config{Providers: []config.ProviderConfig{provider}},
		googleClientFactory: func(got googleaistudio.Config) (googleAIStudioClient, error) {
			if got.APIKey != "AIza-test" || got.Model != "gemini-flash" {
				t.Fatalf("client config = %#v", got)
			}
			return client, nil
		},
	}
	backend, model, err := bridge.chatBackend(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.(slowmode.Backend); !ok || model != "gemini-flash" {
		t.Fatalf("backend=%T model=%q", backend, model)
	}
}

func googleSlowModeTestProvider() config.ProviderConfig {
	return config.ProviderConfig{
		ID: "google", Kind: config.ProviderGoogleAIStudio, Model: "gemini-flash",
		QuotaMode: config.ProviderQuotaFreeTier,
	}
}

func waitForGoogleSlowModeQueue(t *testing.T, coordinator *slowmode.Coordinator, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := coordinator.Snapshot(context.Background())
		if err == nil && snapshot.Waiting == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue did not reach %d", count)
}

type fakeGoogleClient struct {
	mu         sync.Mutex
	tokenCount int64
	countCalls int
	stream     agent.ModelStream
	startErr   error
}

func (client *fakeGoogleClient) CountTokens(context.Context, agent.ModelRequest) (googleaistudio.TokenCount, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.countCalls++
	return googleaistudio.TokenCount{TotalTokens: client.tokenCount}, nil
}

func (client *fakeGoogleClient) Start(context.Context, agent.ModelRequest) (agent.ModelStream, error) {
	if client.startErr != nil {
		return nil, client.startErr
	}
	if client.stream != nil {
		return client.stream, nil
	}
	return &emptyGoogleStream{}, nil
}

type emptyGoogleStream struct{}

func (*emptyGoogleStream) Recv(context.Context) (agent.ModelEvent, error) {
	return agent.ModelEvent{}, io.EOF
}
func (*emptyGoogleStream) Close() error { return nil }
