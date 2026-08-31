package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

const (
	BackstoryMemorySchemaVersion = 1
	BackstorySourceIdentitySeed  = "identity_seed"
	BackstoryEpistemicFictional  = "fictional"
)

// BackstoryMemoryPayload is the typed, inspectable metadata stored beside a
// human-readable backstory episode. It makes the epistemic boundary durable:
// an owner-authored fictional past cannot silently become a user or world fact.
type BackstoryMemoryPayload struct {
	SchemaVersion             int       `json:"schema_version"`
	EpistemicStatus           string    `json:"epistemic_status"`
	Provenance                string    `json:"provenance"`
	OwnerAuthored             bool      `json:"owner_authored"`
	AgentID                   domain.ID `json:"agent_id"`
	EpisodeID                 string    `json:"episode_id"`
	EpisodeDigest             string    `json:"episode_digest"`
	Title                     string    `json:"title,omitempty"`
	EpisodeKind               string    `json:"episode_kind,omitempty"`
	People                    []string  `json:"people,omitempty"`
	Place                     string    `json:"place,omitempty"`
	Sequence                  int       `json:"sequence,omitempty"`
	PersonalizationRevisionID domain.ID `json:"personalization_revision_id"`
	PersonalizationVersion    uint64    `json:"personalization_version"`
}

// ParseBackstoryMemoryPayload only accepts metadata produced by the trusted
// identity-seed hydration path. Callers must still inspect Memory.Nature before
// using a record as fictional identity context.
func ParseBackstoryMemoryPayload(value string) (BackstoryMemoryPayload, error) {
	var payload BackstoryMemoryPayload
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return BackstoryMemoryPayload{}, fmt.Errorf("decode backstory memory metadata: %w", err)
	}
	if payload.SchemaVersion != BackstoryMemorySchemaVersion ||
		payload.EpistemicStatus != BackstoryEpistemicFictional ||
		payload.Provenance != BackstorySourceIdentitySeed || !payload.OwnerAuthored ||
		payload.AgentID.Empty() || strings.TrimSpace(payload.EpisodeID) == "" ||
		strings.TrimSpace(payload.EpisodeDigest) == "" || payload.PersonalizationRevisionID.Empty() ||
		payload.PersonalizationVersion == 0 {
		return BackstoryMemoryPayload{}, fmt.Errorf("%w: invalid backstory memory metadata", domain.ErrInvalidArgument)
	}
	return payload, nil
}

// HydrateBackstory projects structured owner-authored episodes into separate,
// private fictional memories. Repeating the same seed is a true no-op: it does
// not append a revision or duplicate provenance. A changed episode appends one
// explicit version, while a deleted record stays deleted until a future owner
// rehydrate action deliberately restores it.
func (e *Engine) HydrateBackstory(ctx context.Context, seed domain.PersonalizationSeed) ([]WriteResult, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if e == nil || e.store == nil {
		return nil, ErrNoStore
	}
	if err := seed.Validate(); err != nil {
		return nil, err
	}
	if !e.agentID.Empty() && seed.AgentID != e.agentID {
		return nil, domain.ErrConflict
	}

	episodes := append([]domain.BackstoryEpisode(nil), seed.Backstory.Episodes...)
	sort.SliceStable(episodes, func(i, j int) bool {
		if episodes[i].Sequence == episodes[j].Sequence {
			return strings.TrimSpace(episodes[i].ID) < strings.TrimSpace(episodes[j].ID)
		}
		return episodes[i].Sequence < episodes[j].Sequence
	})
	results := make([]WriteResult, 0, len(episodes))
	for _, episode := range episodes {
		if err := contextErr(ctx); err != nil {
			return results, err
		}
		result, err := e.hydrateBackstoryEpisode(ctx, seed, normalizeBackstoryEpisode(episode))
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (e *Engine) hydrateBackstoryEpisode(ctx context.Context, seed domain.PersonalizationSeed, episode domain.BackstoryEpisode) (WriteResult, error) {
	now := e.now().UTC()
	desired, source, payload, err := buildBackstoryMemory(seed, episode, now)
	if err != nil {
		return WriteResult{}, err
	}
	existing, err := e.store.GetMemory(ctx, desired.ID)
	if errors.Is(err, domain.ErrNotFound) {
		created, createErr := e.applyCandidate(ctx, Candidate{
			Memory: desired, Operation: CandidateCreate, MatchID: desired.ID,
			DedupKey: desired.CanonicalKey, Reason: "owner-authored fictional backstory episode",
			Sources: []domain.MemorySource{source},
		}, now)
		if createErr == nil {
			return created, nil
		}
		if !errors.Is(createErr, domain.ErrConflict) {
			return WriteResult{}, createErr
		}
		// Another run may have hydrated this deterministic episode between the
		// read and create. Re-read and treat an identical winner as success.
		existing, err = e.store.GetMemory(ctx, desired.ID)
	}
	if err != nil {
		return WriteResult{}, err
	}
	if existing.AgentID != seed.AgentID || existing.ID != desired.ID ||
		existing.CanonicalKey != desired.CanonicalKey || existing.Kind != domain.MemoryKindEpisodic ||
		existing.Nature != domain.MemoryNatureFiction {
		return WriteResult{}, fmt.Errorf("%w: backstory memory identity collision", domain.ErrConflict)
	}
	existingPayload, err := ParseBackstoryMemoryPayload(existing.ContentJSON)
	if err != nil || existingPayload.AgentID != seed.AgentID || existingPayload.EpisodeID != episode.ID {
		return WriteResult{}, fmt.Errorf("%w: backstory memory provenance mismatch", domain.ErrConflict)
	}
	if existing.Lifecycle == domain.MemoryLifecycleDeleted {
		return WriteResult{Memory: existing, Operation: OperationTouch, Changed: false, Reason: "backstory memory is owner-disabled"}, nil
	}
	if existingPayload.EpisodeDigest == payload.EpisodeDigest {
		return WriteResult{Memory: existing, Operation: OperationTouch, Changed: false, Reason: "backstory episode is already hydrated"}, nil
	}

	desired.Version = existing.Version + 1
	desired.CreatedAt = existing.CreatedAt
	desired.UpdatedAt = now
	desired.Scope = existing.Scope
	desired.Lifecycle = existing.Lifecycle
	desired.DormantAt = existing.DormantAt
	desired.Pinned = existing.Pinned
	desired.HiddenFromCore = existing.HiddenFromCore
	desired.AccessCount = existing.AccessCount
	desired.LastAccessedAt = existing.LastAccessedAt
	desired.LastRecalledAt = existing.LastRecalledAt
	updated, err := e.commit(ctx, desired, &existing, []domain.MemorySource{source}, OperationUpdate, "owner updated fictional backstory episode", now, false)
	if err == nil || !errors.Is(err, domain.ErrConflict) {
		return updated, err
	}
	// Concurrent identical owner updates converge without adding a duplicate
	// revision. A genuinely different winner remains a conflict for the caller.
	current, reloadErr := e.store.GetMemory(ctx, desired.ID)
	if reloadErr != nil {
		return WriteResult{}, reloadErr
	}
	currentPayload, payloadErr := ParseBackstoryMemoryPayload(current.ContentJSON)
	if payloadErr == nil && currentPayload.EpisodeDigest == payload.EpisodeDigest {
		return WriteResult{Memory: current, Operation: OperationTouch, Changed: false, Reason: "backstory episode was concurrently hydrated"}, nil
	}
	return WriteResult{}, err
}

func buildBackstoryMemory(seed domain.PersonalizationSeed, episode domain.BackstoryEpisode, now time.Time) (domain.Memory, domain.MemorySource, BackstoryMemoryPayload, error) {
	digest, err := backstoryEpisodeDigest(episode)
	if err != nil {
		return domain.Memory{}, domain.MemorySource{}, BackstoryMemoryPayload{}, err
	}
	payload := BackstoryMemoryPayload{
		SchemaVersion: BackstoryMemorySchemaVersion, EpistemicStatus: BackstoryEpistemicFictional,
		Provenance: BackstorySourceIdentitySeed, OwnerAuthored: true, AgentID: seed.AgentID,
		EpisodeID: episode.ID, EpisodeDigest: digest, Title: episode.Title, EpisodeKind: episode.Kind,
		People: append([]string(nil), episode.People...), Place: episode.Place, Sequence: episode.Sequence,
		PersonalizationRevisionID: seed.RevisionID, PersonalizationVersion: seed.Version,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.Memory{}, domain.MemorySource{}, BackstoryMemoryPayload{}, fmt.Errorf("encode backstory memory metadata: %w", err)
	}
	memoryID := backstoryMemoryID(seed.AgentID, episode.ID)
	canonicalKey := "backstory:" + BackstorySourceIdentitySeed + ":" + seed.AgentID.String() + ":" + episode.ID
	summary := episode.Title
	if summary == "" {
		summary = truncateBackstorySummary(episode.Content, 240)
	}
	salience := 0.65 + absFloat(episode.EmotionalValence)*0.15
	memory := domain.Memory{
		ID: memoryID, AgentID: seed.AgentID, Scope: domain.MemoryScopeAgentPrivate, Version: 1,
		Kind: domain.MemoryKindEpisodic, Nature: domain.MemoryNatureFiction,
		Content: episode.Content, ContentJSON: string(encoded), Summary: summary,
		Confidence: 1, Salience: salience, Valence: episode.EmotionalValence,
		Sensitivity: domain.MemorySensitivityPrivate, Retention: domain.MemoryRetentionPermanent,
		Lifecycle: domain.MemoryLifecycleActive, CanonicalKey: canonicalKey,
		CreatedAt: now, UpdatedAt: now, Reason: "owner-authored fictional identity seed",
	}
	source := domain.MemorySource{
		SourceType: BackstorySourceIdentitySeed, SourceID: seed.RevisionID,
		ExcerptHash: hashExcerpt(episode.Content), CreatedAt: now,
	}
	return memory, source, payload, nil
}

func normalizeBackstoryEpisode(episode domain.BackstoryEpisode) domain.BackstoryEpisode {
	episode.ID = strings.TrimSpace(episode.ID)
	episode.Title = strings.TrimSpace(episode.Title)
	episode.Content = strings.TrimSpace(episode.Content)
	episode.Kind = strings.TrimSpace(episode.Kind)
	episode.Place = strings.TrimSpace(episode.Place)
	for index := range episode.People {
		episode.People[index] = strings.TrimSpace(episode.People[index])
	}
	return episode
}

func backstoryEpisodeDigest(episode domain.BackstoryEpisode) (string, error) {
	encoded, err := json.Marshal(episode)
	if err != nil {
		return "", fmt.Errorf("encode backstory episode digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func backstoryMemoryID(agentID domain.ID, episodeID string) domain.ID {
	digest := sha256.Sum256([]byte(agentID.String() + "\x00" + episodeID))
	return domain.ID("backstory_" + hex.EncodeToString(digest[:16]))
}

func truncateBackstorySummary(value string, maxRunes int) string {
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:maxRunes])) + "…"
}

func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
