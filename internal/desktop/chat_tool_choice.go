package desktop

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

const recentPeerRetryRunWindow = 6

// chatToolChoice turns only high-confidence owner intent into a provider-level
// requirement. Most turns keep the provider default (auto); personality and
// roleplay therefore remain free-form while explicit actions cannot be
// replaced by a promise to act later.
func (b *Bridge) chatToolChoice(ctx context.Context, conversationID, activeAgentID domain.ID, text string, roster []domain.AgentProfile) (agent.ToolChoice, error) {
	if directPeerAction(text, activeAgentID, roster) {
		return agent.ToolChoice{Mode: agent.ToolChoiceRequired, Name: peerDialogueToolID}, nil
	}
	if !peerRetryCue(text) {
		return agent.ToolChoice{}, nil
	}
	failed, err := b.recentPeerToolFailed(ctx, conversationID)
	if err != nil {
		return agent.ToolChoice{}, err
	}
	if failed {
		return agent.ToolChoice{Mode: agent.ToolChoiceRequired, Name: peerDialogueToolID}, nil
	}
	return agent.ToolChoice{}, nil
}

func directPeerAction(text string, activeAgentID domain.ID, roster []domain.AgentProfile) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return false
	}
	action := containsAny(normalized,
		"напиши", "передай", "скажи", "спроси", "поговори", "свяжись", "обратись", "позови",
		"message ", "tell ", "ask ", "talk to ", "contact ")
	if !action {
		return false
	}
	for _, peer := range roster {
		if peer.ID == activeAgentID {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(peer.Name))
		if utf8.RuneCountInString(name) >= 2 && strings.Contains(normalized, name) {
			return true
		}
	}
	return false
}

func peerRetryCue(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" || utf8.RuneCountInString(normalized) > 160 {
		return false
	}
	return containsAny(normalized,
		"попроб", "ещё раз", "еще раз", "повтори", "давай", "не стесняй", "продолж",
		"try again", "retry", "do it", "go ahead", "continue")
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func (b *Bridge) recentPeerToolFailed(ctx context.Context, conversationID domain.ID) (bool, error) {
	runs, err := b.repositories.Runs.ListByConversation(ctx, conversationID)
	if err != nil {
		return false, err
	}
	if len(runs) > recentPeerRetryRunWindow {
		runs = runs[len(runs)-recentPeerRetryRunWindow:]
	}
	runIDs := make([]domain.ID, 0, len(runs))
	for _, run := range runs {
		if run.Kind == domain.RunKindInteractive {
			runIDs = append(runIDs, run.ID)
		}
	}
	callsByRun, err := b.repositories.ToolCalls.ListByRuns(ctx, runIDs)
	if err != nil {
		return false, err
	}
	for runIndex := len(runIDs) - 1; runIndex >= 0; runIndex-- {
		calls := callsByRun[runIDs[runIndex]]
		for callIndex := len(calls) - 1; callIndex >= 0; callIndex-- {
			call := calls[callIndex]
			if call.ToolID != peerDialogueToolID {
				continue
			}
			return call.Status == storage.ToolCallFailed, nil
		}
	}
	return false, nil
}
