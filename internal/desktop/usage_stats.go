package desktop

import (
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

const (
	// The default is intentionally finite: the activity screen should remain
	// responsive on a long-lived local installation without requiring a caller
	// to know anything about the storage layout.
	defaultRunUsageStatsWindow = 30 * 24 * time.Hour
)

// RunUsageStatsInput selects a bounded usage report. From and To are RFC3339
// timestamps and describe a half-open [from, to) interval. If either bound is
// omitted, the bridge fills it from the default 30-day window.
type RunUsageStatsInput struct {
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	AgentID string `json:"agentId,omitempty"`
}

// RunUsageStatsView is a secret-free projection of durable run accounting.
// Provider/model values are historical route attribution; credentials and
// provider configuration never cross the bridge.
type RunUsageStatsView struct {
	From   string                   `json:"from"`
	To     string                   `json:"to"`
	Groups []RunUsageStatsGroupView `json:"groups"`
}

type RunUsageStatsGroupView struct {
	AgentID      string           `json:"agentId"`
	AgentName    string           `json:"agentName,omitempty"`
	ProviderID   string           `json:"providerId,omitempty"`
	Model        string           `json:"model,omitempty"`
	RunCount     int64            `json:"runCount"`
	StatusCounts map[string]int64 `json:"statusCounts"`
	FailureKinds map[string]int64 `json:"failureKinds"`
	InputTokens  int64            `json:"inputTokens"`
	OutputTokens int64            `json:"outputTokens"`
	TotalTokens  int64            `json:"totalTokens"`
}

// GetRunUsageStats aggregates provider/model usage over durable agent runs.
// The method does not estimate cost from mutable provider catalogs: historic
// runs have no guaranteed price snapshot, so a cost value here would be
// misleading. Cost reporting can be added once price attribution is durable.
func (b *Bridge) GetRunUsageStats(input RunUsageStatsInput) (RunUsageStatsView, error) {
	if b == nil || b.repositories == nil || b.repositories.Runs == nil {
		return RunUsageStatsView{}, fmt.Errorf("run usage statistics are unavailable")
	}
	from, to, err := usageStatsWindow(input)
	if err != nil {
		return RunUsageStatsView{}, err
	}
	ctx, cancel := b.context()
	defer cancel()
	groups, err := b.repositories.Runs.AggregateUsageStats(ctx, storage.RunUsageStatsOptions{
		From: from, To: to, AgentID: domain.ID(strings.TrimSpace(input.AgentID)),
	})
	if err != nil {
		return RunUsageStatsView{}, err
	}
	views := make([]RunUsageStatsGroupView, 0, len(groups))
	for _, group := range groups {
		views = append(views, RunUsageStatsGroupView{
			AgentID:      string(group.AgentID),
			AgentName:    group.AgentName,
			ProviderID:   group.ProviderID,
			Model:        group.Model,
			RunCount:     group.RunCount,
			StatusCounts: cloneCountMap(group.StatusCounts),
			FailureKinds: cloneCountMap(group.FailureKinds),
			InputTokens:  group.Usage.InputTokens,
			OutputTokens: group.Usage.OutputTokens,
			TotalTokens:  group.Usage.TotalTokens,
		})
	}
	return RunUsageStatsView{
		From:   from.UTC().Format(time.RFC3339Nano),
		To:     to.UTC().Format(time.RFC3339Nano),
		Groups: views,
	}, nil
}

func usageStatsWindow(input RunUsageStatsInput) (time.Time, time.Time, error) {
	from, err := parseUsageStatsTime("from", input.From)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := parseUsageStatsTime("to", input.To)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	now := time.Now().UTC()
	switch {
	case from.IsZero() && to.IsZero():
		to = now
		from = to.Add(-defaultRunUsageStatsWindow)
	case from.IsZero():
		from = to.Add(-defaultRunUsageStatsWindow)
	case to.IsZero():
		to = now
	}
	return validateDesktopUsageStatsWindow(from, to)
}

func parseUsageStatsTime(name, value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s must be an RFC3339 timestamp", domain.ErrInvalidArgument, name)
	}
	return parsed.UTC(), nil
}

func validateDesktopUsageStatsWindow(from, to time.Time) (time.Time, time.Time, error) {
	if from.IsZero() || to.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: usage stats require both window bounds", domain.ErrInvalidArgument)
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: usage stats window must end after it starts", domain.ErrInvalidArgument)
	}
	if to.Sub(from) > storage.MaxRunUsageStatsWindow {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: usage stats window exceeds %s", domain.ErrInvalidArgument, storage.MaxRunUsageStatsWindow)
	}
	return from.UTC(), to.UTC(), nil
}

func cloneCountMap(values map[string]int64) map[string]int64 {
	clone := make(map[string]int64, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}
