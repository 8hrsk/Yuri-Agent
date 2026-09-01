package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// MaxRunUsageStatsWindow is the largest interval accepted by the aggregate
// query. Usage reports are intentionally bounded so a desktop request cannot
// turn into an unbounded scan as the local history grows.
const MaxRunUsageStatsWindow = 366 * 24 * time.Hour

// RunUsageStatsOptions describes a bounded report over runs started in the
// half-open [From, To) interval. Provider credentials and other provider
// configuration are deliberately not part of this query.
type RunUsageStatsOptions struct {
	From    time.Time
	To      time.Time
	AgentID domain.ID
}

// RunUsageStatsGroup is one provider/model and agent bucket. StatusCounts and
// FailureKinds are maps instead of a fixed enum projection so the bridge can
// expose newly added lifecycle values without changing this storage query.
// Usage is the sum of provider-reported usage for all runs in the bucket.
type RunUsageStatsGroup struct {
	AgentID      domain.ID
	AgentName    string
	ProviderID   string
	Model        string
	RunCount     int64
	StatusCounts map[string]int64
	FailureKinds map[string]int64
	Usage        domain.RunUsage
}

// AggregateUsageStats returns usage grouped by the owning agent and the
// historical provider/model route captured on each durable run. A run is
// counted exactly once: SQLite groups by state and failure kind, then this
// method folds those rows into the corresponding bucket.
func (r *RunRepository) AggregateUsageStats(ctx context.Context, options RunUsageStatsOptions) ([]RunUsageStatsGroup, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	from, to, err := validateRunUsageStatsWindow(options.From, options.To)
	if err != nil {
		return nil, err
	}

	query := `
		SELECT r.agent_id, COALESCE(p.name, ''), r.provider_id, r.model,
		       r.state, r.failure_kind, COUNT(*),
		       COALESCE(SUM(r.input_tokens), 0),
		       COALESCE(SUM(r.output_tokens), 0),
		       COALESCE(SUM(r.total_tokens), 0)
		FROM agent_runs AS r
		LEFT JOIN agent_profiles AS p ON p.id = r.agent_id
		WHERE r.created_at >= ? AND r.created_at < ?`
	args := []any{formatTime(from), formatTime(to)}
	if !options.AgentID.Empty() {
		query += ` AND r.agent_id = ?`
		args = append(args, strings.TrimSpace(string(options.AgentID)))
	}
	query += `
		GROUP BY r.agent_id, COALESCE(p.name, ''), r.provider_id, r.model, r.state, r.failure_kind
		ORDER BY r.agent_id ASC, r.provider_id ASC, r.model ASC, r.state ASC, r.failure_kind ASC`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("aggregate run usage", err)
	}
	defer rows.Close()

	type groupKey struct {
		agentID    string
		agentName  string
		providerID string
		model      string
	}
	groups := make([]RunUsageStatsGroup, 0)
	indexes := make(map[groupKey]int)
	for rows.Next() {
		var (
			agentID, agentName, providerID, model, state, failureKind string
			runCount                                                  int64
			inputTokens, outputTokens, totalTokens                    int64
		)
		if err := rows.Scan(&agentID, &agentName, &providerID, &model, &state, &failureKind,
			&runCount, &inputTokens, &outputTokens, &totalTokens); err != nil {
			return nil, wrappedSQLError("scan aggregated run usage", err)
		}
		key := groupKey{agentID: agentID, agentName: agentName, providerID: providerID, model: model}
		index, ok := indexes[key]
		if !ok {
			index = len(groups)
			indexes[key] = index
			groups = append(groups, RunUsageStatsGroup{
				AgentID:      domain.ID(agentID),
				AgentName:    agentName,
				ProviderID:   providerID,
				Model:        model,
				StatusCounts: make(map[string]int64),
				FailureKinds: make(map[string]int64),
			})
		}
		group := &groups[index]
		group.RunCount += runCount
		group.StatusCounts[strings.TrimSpace(state)] += runCount
		group.Usage.InputTokens += inputTokens
		group.Usage.OutputTokens += outputTokens
		group.Usage.TotalTokens += totalTokens

		// Failure metadata was introduced after the first durable run schema.
		// Failed legacy rows have no kind, but should still be visible rather
		// than silently disappearing from the failure breakdown.
		if strings.TrimSpace(state) == string(domain.RunStateFailed) {
			failureKind = strings.TrimSpace(failureKind)
			if failureKind == "" {
				failureKind = string(domain.RunFailureUnknown)
			}
			group.FailureKinds[failureKind] += runCount
		}
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate aggregated run usage", err)
	}
	return groups, nil
}

func validateRunUsageStatsWindow(from, to time.Time) (time.Time, time.Time, error) {
	if from.IsZero() || to.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: usage stats require both window bounds", domain.ErrInvalidArgument)
	}
	from, to = from.UTC(), to.UTC()
	if !to.After(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: usage stats window must end after it starts", domain.ErrInvalidArgument)
	}
	if to.Sub(from) > MaxRunUsageStatsWindow {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: usage stats window exceeds %s", domain.ErrInvalidArgument, MaxRunUsageStatsWindow)
	}
	return from, to, nil
}
