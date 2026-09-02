package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	googleaistudio "github.com/OrdoAI/yuri-agent/internal/providers/googleaistudio"
	"github.com/OrdoAI/yuri-agent/internal/slowmode"
)

const (
	googleExactTokenBoundaryPercent = int64(70)
	googleTokenCacheEntries         = 128
	googleTokenCacheTTL             = 10 * time.Minute
)

type googleAIStudioClient interface {
	agent.ModelBackend
	CountTokens(context.Context, agent.ModelRequest) (googleaistudio.TokenCount, error)
}

type googleAIStudioClientFactory func(googleaistudio.Config) (googleAIStudioClient, error)

type googleSlowModeEntry struct {
	providerID   string
	fingerprint  string
	coordinator  *slowmode.Coordinator
	tokens       *googleTokenCountCache
	countMu      sync.Mutex
	effectiveTPM int64
}

func (b *Bridge) newGoogleAIStudioClient(config googleaistudio.Config) (googleAIStudioClient, error) {
	b.mu.RLock()
	factory := b.googleClientFactory
	b.mu.RUnlock()
	if factory != nil {
		return factory(config)
	}
	return googleaistudio.New(config)
}

func (b *Bridge) googleBackendWithSlowMode(ctx context.Context, provider config.ProviderConfig, model string, client googleAIStudioClient) (agent.ModelBackend, error) {
	if provider.QuotaMode == "" || provider.QuotaMode == config.ProviderQuotaOff {
		return client, nil
	}
	entry, err := b.googleSlowModeEntry(ctx, provider, model)
	if err != nil {
		return nil, err
	}
	estimator := &googleInputTokenEstimator{
		client: client, cache: entry.tokens, coordinator: entry.coordinator, exactMu: &entry.countMu, exactBoundary: entry.effectiveTPM,
	}
	return slowmode.Backend{
		Backend: client, Coordinator: entry.coordinator, Estimate: estimator.Estimate,
		Classify: googleSlowModePriority, Feedback: googleSlowModeFeedback,
	}, nil
}

func (b *Bridge) googleSlowModeEntry(ctx context.Context, provider config.ProviderConfig, model string) (*googleSlowModeEntry, error) {
	coordinatorConfig, effectiveTPM, fingerprint, err := googleSlowModeConfig(provider, model)
	if err != nil {
		return nil, err
	}
	key := googleSlowModeKey(provider.ID, model)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.googleSlowModes == nil {
		b.googleSlowModes = make(map[string]*googleSlowModeEntry)
	}
	if b.googleQuotaLedger == nil {
		b.googleQuotaLedger = newGoogleFileQuotaLedger(b.paths.DataDirectory)
	}
	if existing := b.googleSlowModes[key]; existing != nil && existing.fingerprint == fingerprint {
		return existing, nil
	}
	// A quota-profile change invalidates every model scope of this provider.
	// Existing in-flight wrappers retain their old coordinator; all subsequent
	// backends atomically observe the new pacing envelope.
	for existingKey, existing := range b.googleSlowModes {
		if existing.providerID == provider.ID && existing.fingerprint != fingerprint {
			delete(b.googleSlowModes, existingKey)
		}
	}
	coordinator, err := slowmode.NewCoordinator(ctx, coordinatorConfig, slowmode.Dependencies{Ledger: b.googleQuotaLedger, Warmup: b.googleQuotaLedger})
	if err != nil {
		return nil, err
	}
	entry := &googleSlowModeEntry{
		providerID: provider.ID, fingerprint: fingerprint, coordinator: coordinator,
		tokens: newGoogleTokenCountCache(googleTokenCacheEntries, googleTokenCacheTTL), effectiveTPM: effectiveTPM,
	}
	b.googleSlowModes[key] = entry
	return entry, nil
}

func (b *Bridge) invalidateGoogleSlowModesLocked(providerID string) {
	for key, entry := range b.googleSlowModes {
		if entry.providerID == providerID {
			delete(b.googleSlowModes, key)
		}
	}
}

func googleSlowModeConfig(provider config.ProviderConfig, model string) (slowmode.Config, int64, string, error) {
	if provider.Kind != config.ProviderGoogleAIStudio {
		return slowmode.Config{}, 0, "", fmt.Errorf("Google slow mode requires a Google AI Studio provider")
	}
	if provider.QuotaMode != config.ProviderQuotaFreeTier && provider.QuotaMode != config.ProviderQuotaCustom {
		return slowmode.Config{}, 0, "", fmt.Errorf("unsupported Google quota mode %q", provider.QuotaMode)
	}
	profile := provider.QuotaProfile
	maxConcurrent := profile.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	// Unknown Free Tier ceilings stay single-flight. Numeric pacing starts only
	// for dimensions the owner copied from the authoritative AI Studio page.
	if provider.QuotaMode == config.ProviderQuotaFreeTier && profile.RPM == 0 && profile.TPM == 0 && profile.RPD == 0 {
		maxConcurrent = 1
	}
	safety := profile.SafetyPercent
	if safety == 0 {
		safety = 80
	}
	reserve := profile.InteractiveReservePercent
	if reserve == 0 {
		reserve = 25
	}
	if profile.RPD == 0 {
		reserve = 0
	}
	limits := slowmode.Limits{
		RPM: int64(profile.RPM), TPM: int64(profile.TPM), RPD: int64(profile.RPD), MaxConcurrent: maxConcurrent,
	}
	value := slowmode.Config{
		Scope: googleSlowModeKey(provider.ID, model), Limits: limits,
		SafetyPercent: safety, InteractiveReservePercent: reserve,
	}
	effectiveTPM := applyGoogleSafety(limits.TPM, safety)
	fingerprint := fmt.Sprintf("%s:%d:%d:%d:%d:%d:%d", provider.QuotaMode, profile.RPM, profile.TPM, profile.RPD, maxConcurrent, safety, reserve)
	return value, effectiveTPM, fingerprint, nil
}

func googleSlowModeKey(providerID, model string) string {
	return strings.TrimSpace(providerID) + "\x00" + strings.TrimSpace(model)
}

func applyGoogleSafety(value int64, percent int) int64 {
	if value <= 0 {
		return 0
	}
	result := value/100*int64(percent) + value%100*int64(percent)/100
	if result < 1 {
		return 1
	}
	return result
}

func googleSlowModePriority(request agent.ModelRequest) slowmode.PriorityClass {
	if request.Metadata != nil {
		if _, explicit := request.Metadata[slowmode.MetadataPriorityKey]; explicit {
			return slowmode.DefaultPriority(request)
		}
	}
	switch strings.ToLower(strings.TrimSpace(request.Metadata["purpose"])) {
	case "personality_preview", "peer_dialogue":
		return slowmode.PriorityOwnerInitiated
	case "anonymous_subagent":
		return slowmode.PriorityBackground
	case "conversation_title", "background_reflection", "peer_social_reflection", "autonomous_peer_trigger", "memory_extraction":
		return slowmode.PriorityMaintenance
	default:
		return slowmode.PriorityForeground
	}
}

func slowModePriorityForRunKind(kind domain.RunKind) string {
	switch kind {
	case domain.RunKindBackground, domain.RunKindSubagent:
		return slowmode.MetadataPriorityBackground
	case domain.RunKindReflection:
		return slowmode.MetadataPriorityMaintenance
	default:
		return slowmode.MetadataPriorityForeground
	}
}

func googleSlowModeFeedback(err error) (slowmode.Feedback, bool) {
	var providerError *googleaistudio.Error
	if !errors.As(err, &providerError) {
		return slowmode.Feedback{}, false
	}
	feedback := slowmode.Feedback{RetryAfter: providerError.RetryAfter}
	switch providerError.Reason {
	case googleaistudio.ErrorReasonQuotaExhausted:
		feedback.Kind = slowmode.FeedbackDailyQuota
	case googleaistudio.ErrorReasonRateLimit:
		feedback.Kind = slowmode.FeedbackShortWindow
	case googleaistudio.ErrorReasonResourceExhausted:
		feedback.Kind = slowmode.FeedbackAmbiguous
	default:
		// Google documents 408, 429 and 5xx as transient conditions that
		// should be backed off. Treat overload/timeouts as an ambiguous short
		// window signal: unlike QUOTA_EXCEEDED this must never exhaust RPD, but
		// it should cool the shared model scope before the owner retries.
		if providerError.StatusCode != 408 && providerError.StatusCode != 429 && providerError.StatusCode < 500 {
			return slowmode.Feedback{}, false
		}
		feedback.Kind = slowmode.FeedbackAmbiguous
	}
	return feedback, true
}

type googleInputTokenEstimator struct {
	client        googleAIStudioClient
	cache         *googleTokenCountCache
	coordinator   *slowmode.Coordinator
	exactMu       *sync.Mutex
	exactBoundary int64
}

func (estimator *googleInputTokenEstimator) Estimate(ctx context.Context, request agent.ModelRequest) (int64, error) {
	local := conservativeGoogleInputTokens(request)
	exact := googleRequestHasMultimodalInput(request)
	boundary := estimator.exactBoundary
	windowTokens := int64(0)
	if estimator.coordinator != nil {
		snapshot, err := estimator.coordinator.Snapshot(ctx)
		if err != nil {
			return 0, fmt.Errorf("inspect Google slow-mode token window: %w", err)
		}
		boundary = snapshot.Effective.TPM
		windowTokens = snapshot.WindowTokens
	}
	if boundary > 0 && saturatingTokenAdd(windowTokens, local) >= percentageCeiling(boundary, googleExactTokenBoundaryPercent) {
		exact = true
	}
	if !exact {
		return local, nil
	}
	key, err := googleTokenRequestHash(request)
	if err != nil {
		return 0, fmt.Errorf("hash Google token-count request: %w", err)
	}
	if estimator.cache != nil {
		if tokens, ok := estimator.cache.Get(key, time.Now()); ok {
			return tokens, nil
		}
	}
	if estimator.exactMu != nil {
		estimator.exactMu.Lock()
		defer estimator.exactMu.Unlock()
		// Another waiter may have filled the cache while this request waited.
		if estimator.cache != nil {
			if tokens, ok := estimator.cache.Get(key, time.Now()); ok {
				return tokens, nil
			}
		}
	}
	count, err := estimator.client.CountTokens(ctx, request)
	if err != nil {
		return 0, err
	}
	if count.TotalTokens < 0 {
		return 0, errors.New("Google countTokens returned a negative total")
	}
	if estimator.cache != nil {
		estimator.cache.Put(key, count.TotalTokens, time.Now())
	}
	return count.TotalTokens, nil
}

func percentageCeiling(value, percent int64) int64 {
	if value <= 0 || percent <= 0 {
		return 0
	}
	whole := value / 100 * percent
	remainder := value % 100 * percent
	return whole + (remainder+99)/100
}

func conservativeGoogleInputTokens(request agent.ModelRequest) int64 {
	// One token per UTF-8 byte plus structural overhead intentionally
	// overestimates normal text, including Cyrillic/CJK. Multimodal requests
	// always use countTokens, so base64 size is not used for final admission.
	total := int64(64)
	add := func(size int) {
		if size <= 0 || total == math.MaxInt64 {
			return
		}
		if int64(size) > math.MaxInt64-total {
			total = math.MaxInt64
			return
		}
		total += int64(size)
	}
	add(len(request.Model))
	for _, message := range request.Messages {
		total = saturatingTokenAdd(total, 16)
		add(len(message.Role))
		add(len(message.Content))
		add(len(message.Name))
		add(len(message.ToolCallID))
		for _, part := range message.Parts {
			add(len(part.MediaType))
			add(len(part.Data))
		}
		for _, call := range message.ToolCalls {
			add(len(call.Name))
			add(len(call.Arguments))
		}
	}
	for _, tool := range request.Tools {
		total = saturatingTokenAdd(total, 24)
		add(len(tool.Name))
		add(len(tool.Description))
		add(len(tool.InputSchema))
	}
	return total
}

func saturatingTokenAdd(value, addition int64) int64 {
	if addition > math.MaxInt64-value {
		return math.MaxInt64
	}
	return value + addition
}

func googleRequestHasMultimodalInput(request agent.ModelRequest) bool {
	for _, message := range request.Messages {
		if len(message.Parts) > 0 {
			return true
		}
	}
	return false
}

func googleTokenRequestHash(request agent.ModelRequest) ([sha256.Size]byte, error) {
	hash := sha256.New()
	if err := json.NewEncoder(hash).Encode(request); err != nil {
		return [sha256.Size]byte{}, err
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

type googleTokenCacheValue struct {
	tokens    int64
	expiresAt time.Time
}

type googleTokenCountCache struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	entries map[[sha256.Size]byte]googleTokenCacheValue
	order   [][sha256.Size]byte
	next    int
}

func newGoogleTokenCountCache(maxEntries int, ttl time.Duration) *googleTokenCountCache {
	return &googleTokenCountCache{max: maxEntries, ttl: ttl, entries: make(map[[sha256.Size]byte]googleTokenCacheValue)}
}

func (cache *googleTokenCountCache) Get(key [sha256.Size]byte, now time.Time) (int64, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	value, ok := cache.entries[key]
	if !ok || !value.expiresAt.After(now) {
		return 0, false
	}
	return value.tokens, true
}

func (cache *googleTokenCountCache) Put(key [sha256.Size]byte, tokens int64, now time.Time) {
	if cache == nil || cache.max <= 0 || cache.ttl <= 0 {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	value := googleTokenCacheValue{tokens: tokens, expiresAt: now.Add(cache.ttl)}
	if _, exists := cache.entries[key]; exists {
		cache.entries[key] = value
		return
	}
	if len(cache.order) < cache.max {
		cache.order = append(cache.order, key)
	} else {
		delete(cache.entries, cache.order[cache.next])
		cache.order[cache.next] = key
		cache.next = (cache.next + 1) % cache.max
	}
	cache.entries[key] = value
}
