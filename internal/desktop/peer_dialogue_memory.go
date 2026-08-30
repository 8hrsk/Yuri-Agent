package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

const (
	peerDialogueMemoryReconcileLimit = 100
	peerDialogueMemoryMaxBytes       = 1_200
)

// reconcileCompletedPeerDialogueMemories repairs the bounded newest
// completed-dialogue window after a crash between aggregate completion and
// episodic projection. Deterministic memory IDs make every pass idempotent.
func (b *Bridge) reconcileCompletedPeerDialogueMemories(ctx context.Context, limit int) (int, error) {
	if b == nil || b.repositories == nil || b.repositories.PeerDialogues == nil {
		return 0, fmt.Errorf("%w: peer dialogue repositories are unavailable", domain.ErrInvalidArgument)
	}
	if limit <= 0 || limit > peerDialogueMemoryReconcileLimit {
		limit = peerDialogueMemoryReconcileLimit
	}
	dialogues, err := b.repositories.PeerDialogues.ListCompleted(ctx, limit)
	if err != nil {
		return 0, err
	}
	writes := 0
	var failures []error
	for _, dialogue := range dialogues {
		created, deriveErr := b.derivePeerDialogueMemories(ctx, dialogue)
		writes += created
		if deriveErr != nil {
			failures = append(failures, fmt.Errorf("derive dialogue %s: %w", dialogue.ID, deriveErr))
		}
	}
	return writes, errors.Join(failures...)
}

// derivePeerDialogueMemories creates one private episode per participant. It
// records only what happened; opinions, emotions and relationship mutations
// belong to the later social-reflection slice.
func (b *Bridge) derivePeerDialogueMemories(ctx context.Context, dialogue domain.PeerDialogue) (int, error) {
	if dialogue.Status != domain.PeerDialogueCompleted {
		return 0, nil
	}
	messages, err := b.repositories.PeerDialogueMessages.ListByDialogue(ctx, dialogue.ID, dialogue.InitiatorAgentID)
	if err != nil {
		return 0, err
	}
	if len(messages) < 2 {
		return 0, fmt.Errorf("%w: completed peer dialogue has no generated response", domain.ErrInvalidArgument)
	}
	agents, err := b.repositories.Agents.ListByIDs(ctx, []domain.ID{dialogue.InitiatorAgentID, dialogue.PeerAgentID})
	if err != nil {
		return 0, err
	}
	createdAt := dialogue.FinishedAt.UTC()
	if createdAt.IsZero() {
		createdAt = dialogue.UpdatedAt.UTC()
	}
	created := 0
	for _, ownerID := range []domain.ID{dialogue.InitiatorAgentID, dialogue.PeerAgentID} {
		memoryID := peerDialogueMemoryID(dialogue.ID, ownerID)
		if _, getErr := b.repositories.Memories.GetForAgent(ctx, ownerID, memoryID); getErr == nil {
			continue
		} else if !errors.Is(getErr, domain.ErrNotFound) {
			return created, getErr
		}
		_, ownerOK := agents[ownerID]
		peerID := dialogue.InitiatorAgentID
		if ownerID == dialogue.InitiatorAgentID {
			peerID = dialogue.PeerAgentID
		}
		peer, peerOK := agents[peerID]
		if !ownerOK || !peerOK {
			return created, fmt.Errorf("%w: peer dialogue participant profile is missing", domain.ErrNotFound)
		}
		content, redacted := peerDialogueEpisodeContent(dialogue, messages, peer, agents)
		metadata, _ := json.Marshal(map[string]any{
			"peer_dialogue_id": dialogue.ID, "peer_agent_id": peerID,
			"turn_count": dialogue.TurnCount, "redacted": redacted,
		})
		sourceRunID := dialogue.TriggerRunID
		for index := len(messages) - 1; index >= 0; index-- {
			if messages[index].SenderAgentID == ownerID {
				sourceRunID = messages[index].SourceRunID
				break
			}
		}
		item := domain.Memory{
			ID: memoryID, AgentID: ownerID, Scope: domain.MemoryScopeAgentPrivate, Version: 1,
			Kind: domain.MemoryKindEpisodic, Nature: domain.MemoryNatureFact,
			Content: content, ContentJSON: string(metadata), Confidence: 1, Salience: .6,
			Sensitivity: domain.MemorySensitivityPrivate, Retention: domain.MemoryRetentionDecay,
			Lifecycle: domain.MemoryLifecycleActive, CanonicalKey: "peer_dialogue:" + dialogue.ID.String(),
			CreatedAt: createdAt, UpdatedAt: createdAt, Reason: "completed peer dialogue episode",
			SourceRunID: sourceRunID,
		}
		sources := make([]domain.MemorySource, 0, len(messages)+1)
		sources = append(sources, domain.MemorySource{
			MemoryID: memoryID, MemoryVersion: 1, SourceType: "peer_dialogue", SourceID: dialogue.ID,
			RunID: dialogue.TriggerRunID, ExcerptHash: textSHA256(dialogue.Purpose), CreatedAt: dialogue.CreatedAt.UTC(),
		})
		for _, message := range messages {
			sources = append(sources, domain.MemorySource{
				MemoryID: memoryID, MemoryVersion: 1, SourceType: "peer_dialogue_message", SourceID: message.ID,
				RunID: message.SourceRunID, ExcerptHash: textSHA256(message.Content), CreatedAt: message.CreatedAt.UTC(),
			})
		}
		if createErr := b.repositories.Memories.Create(ctx, item, sources); createErr != nil {
			if errors.Is(createErr, domain.ErrConflict) {
				if _, getErr := b.repositories.Memories.GetForAgent(ctx, ownerID, memoryID); getErr == nil {
					continue
				}
			}
			return created, createErr
		}
		created++
	}
	return created, nil
}

func peerDialogueMemoryID(dialogueID, agentID domain.ID) domain.ID {
	digest := sha256.Sum256([]byte(dialogueID.String() + "\x00" + agentID.String()))
	return domain.ID("memory_peer_" + hex.EncodeToString(digest[:12]))
}

func peerDialogueEpisodeContent(dialogue domain.PeerDialogue, messages []domain.PeerDialogueMessage, peer domain.AgentProfile, agents map[domain.ID]domain.AgentProfile) (string, bool) {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Межагентный разговор с %s", peer.Name)
	redacted := looksLikeSecret(dialogue.Purpose)
	if !redacted {
		fmt.Fprintf(&builder, " на тему «%s»", strings.TrimSpace(dialogue.Purpose))
	}
	builder.WriteString(". Разговор завершён.")
	start := max(0, len(messages)-2)
	for _, message := range messages[start:] {
		if looksLikeSecret(message.Content) {
			redacted = true
			continue
		}
		name := message.SenderAgentID.String()
		if profile, ok := agents[message.SenderAgentID]; ok {
			name = profile.Name
		}
		fmt.Fprintf(&builder, " %s: %s", name, strings.TrimSpace(message.Content))
	}
	return boundUTF8Bytes(strings.TrimSpace(builder.String()), peerDialogueMemoryMaxBytes), redacted
}
