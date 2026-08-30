package memory

import (
	"context"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// CoreSnapshot selects a stable, bounded prefix for a new run. It includes
// only active, non-hidden high-signal records; the transcript remains
// on-demand. The returned Text is safe to append below higher-priority policy
// and identity instructions because records are explicitly marked as data.
func (e *Engine) CoreSnapshot(ctx context.Context, options Budget) (ContextSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return ContextSnapshot{}, err
	}
	if e == nil || e.store == nil {
		return ContextSnapshot{}, ErrNoStore
	}
	now := e.now()
	if options.MaxItems == 0 && options.MaxChars == 0 && options.MaxTokens == 0 {
		options = e.coreBudget
	}
	options = options.normalize(16)
	items, err := e.store.ListMemories(ctx, MemoryFilter{AgentID: e.agentID, States: []LifecycleState{StateActive}, Kinds: []Kind{KindCore, KindUserProfile, KindSemantic, KindProcedural, KindRelationship}, IncludeHidden: false, Limit: 0})
	if err != nil {
		return ContextSnapshot{}, err
	}
	candidates := make([]RankCandidate, 0, len(items))
	for _, item := range items {
		if (!e.agentID.Empty() && item.AgentID != e.agentID) || item.Lifecycle != domain.MemoryLifecycleActive || item.HiddenFromCore || item.Lifecycle == domain.MemoryLifecycleDeleted ||
			item.Sensitivity == domain.MemorySensitivityHighlySensitive {
			continue
		}
		// Pinned memories receive a deterministic salience floor for snapshot
		// selection without changing their stored value.
		copy := item
		if copy.Pinned && copy.Salience < 0.99 {
			copy.Salience = 0.99
		}
		candidates = append(candidates, RankCandidate{Memory: copy, AffectiveRelevance: math.Abs(copy.Valence)})
	}
	ranked := e.ranker.Rank(candidates, now)
	// Provenance is loaded inside the budget loop, for the records that are
	// actually kept. Loading it for everything the ranker returned meant one
	// store round trip per stored memory on every context assembly, to build a
	// snapshot that keeps at most MaxItems of them. Sources take no part in
	// ranking or selection — only in the rendered text — so deferring the load
	// cannot change which memories are chosen or their order.
	entries := make([]ContextEntry, 0, minInt(options.MaxItems, len(ranked)))
	chars := 0
	for _, result := range ranked {
		if len(entries) >= options.MaxItems {
			break
		}
		content := result.Memory.Content
		remaining := options.MaxChars - chars
		if remaining <= 0 {
			break
		}
		content = truncateUTF8(content, remaining)
		if strings.TrimSpace(content) == "" {
			continue
		}
		sources, sourceErr := e.store.ListMemorySources(ctx, result.Memory.ID)
		if sourceErr != nil {
			return ContextSnapshot{}, sourceErr
		}
		result.Evidence.Sources = sources
		result.Memory.Content = content
		entries = append(entries, ContextEntry{Memory: result.Memory, Score: result.Score, Evidence: result.Evidence})
		chars += utf8.RuneCountInString(content)
		if options.MaxTokens > 0 && int(math.Ceil(float64(chars)/4)) >= options.MaxTokens {
			break
		}
	}
	snapshot := ContextSnapshot{CreatedAt: now.UTC(), Entries: entries, Chars: chars, Tokens: int(math.Ceil(float64(chars) / 4))}
	snapshot.Text = FormatContext(entries)
	return snapshot, nil
}

// FormatContext renders only a bounded data section. Provenance is included
// so the model can distinguish a recalled fact from the current conversation.
func FormatContext(entries []ContextEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("<yuri-memory-context provenance=\"bounded-core-snapshot\">\n")
	for _, entry := range entries {
		builder.WriteString("  <memory id=\"")
		builder.WriteString(escapeContext(entry.Memory.ID.String()))
		builder.WriteString("\" kind=\"")
		builder.WriteString(escapeContext(string(entry.Memory.Kind)))
		builder.WriteString("\" nature=\"")
		builder.WriteString(escapeContext(string(entry.Memory.Nature)))
		builder.WriteString("\" source=\"")
		builder.WriteString(escapeContext(sourceLabel(entry.Evidence.Sources)))
		builder.WriteString("\">\n    ")
		builder.WriteString(escapeContext(entry.Memory.Content))
		builder.WriteString("\n  </memory>\n")
	}
	builder.WriteString("</yuri-memory-context>")
	return builder.String()
}

func sourceLabel(sources []domain.MemorySource) string {
	if len(sources) == 0 {
		return "unknown"
	}
	parts := make([]string, 0, minInt(len(sources), 3))
	for _, source := range sources {
		label := source.SourceID.String()
		if label == "" {
			label = source.SourceType
		}
		parts = append(parts, label)
		if len(parts) == 3 {
			break
		}
	}
	return strings.Join(parts, ",")
}

func escapeContext(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, "\"", "&quot;")
	return value
}
