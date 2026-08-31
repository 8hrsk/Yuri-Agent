package desktop

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	memorycore "github.com/OrdoAI/yuri-agent/internal/memory"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

type MemoryListInput struct {
	Lifecycle      string `json:"lifecycle,omitempty"`
	LifecycleState string `json:"lifecycleState,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Scope          string `json:"scope,omitempty"`
	Query          string `json:"query,omitempty"`
	IncludeDormant bool   `json:"includeDormant,omitempty"`
	IncludeDeleted bool   `json:"includeDeleted,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	Offset         int    `json:"offset,omitempty"`
}

type MemorySourceView struct {
	SourceType        string  `json:"sourceType"`
	SourceID          string  `json:"sourceId,omitempty"`
	ConversationID    string  `json:"conversationId,omitempty"`
	ConversationTitle string  `json:"conversationTitle,omitempty"`
	MessageID         string  `json:"messageId,omitempty"`
	Excerpt           string  `json:"excerpt,omitempty"`
	ExcerptHash       string  `json:"excerptHash,omitempty"`
	EvidenceWeight    float64 `json:"evidenceWeight,omitempty"`
	CreatedAt         string  `json:"createdAt,omitempty"`
}

type MemoryView struct {
	ID               string              `json:"id"`
	AgentID          string              `json:"agentId"`
	AgentName        string              `json:"agentName,omitempty"`
	Scope            string              `json:"scope"`
	Version          uint64              `json:"version"`
	Kind             string              `json:"kind"`
	Nature           string              `json:"nature"`
	Content          string              `json:"content"`
	Confidence       float64             `json:"confidence"`
	Salience         float64             `json:"salience"`
	Valence          float64             `json:"valence"`
	Sensitivity      string              `json:"sensitivity"`
	Lifecycle        string              `json:"lifecycle"`
	Pinned           bool                `json:"pinned"`
	AccessCount      int64               `json:"accessCount"`
	LastRecalledAt   string              `json:"lastRecalledAt,omitempty"`
	DecayPolicy      string              `json:"decayPolicy,omitempty"`
	EmbeddingVersion string              `json:"embeddingVersion,omitempty"`
	CreatedAt        string              `json:"createdAt"`
	UpdatedAt        string              `json:"updatedAt"`
	Sources          []MemorySourceView  `json:"sources"`
	Fiction          *FictionMemoryView  `json:"fiction,omitempty"`
	History          []MemoryHistoryView `json:"history,omitempty"`
}

type FictionMemoryView struct {
	Provenance                string `json:"provenance"`
	RecallState               string `json:"recallState,omitempty"`
	EpistemicStatus           string `json:"epistemicStatus"`
	OwnerAuthored             bool   `json:"ownerAuthored"`
	EpisodeID                 string `json:"episodeId,omitempty"`
	PersonalizationRevisionID string `json:"personalizationRevisionId,omitempty"`
	SourceMemoryID            string `json:"sourceMemoryId,omitempty"`
	SourceVersion             uint64 `json:"sourceVersion,omitempty"`
}

type MemoryHistoryView struct {
	Version   uint64 `json:"version"`
	Operation string `json:"operation"`
	Reason    string `json:"reason,omitempty"`
	CreatedAt string `json:"createdAt"`
}

type UpdateMemoryInput struct {
	ID          string   `json:"id"`
	MemoryID    string   `json:"memoryId"`
	Content     *string  `json:"content,omitempty"`
	Kind        *string  `json:"kind,omitempty"`
	Nature      *string  `json:"nature,omitempty"`
	ContentKind *string  `json:"contentKind,omitempty"`
	Confidence  *float64 `json:"confidence,omitempty"`
	Salience    *float64 `json:"salience,omitempty"`
	Valence     *float64 `json:"valence,omitempty"`
	Pinned      *bool    `json:"pinned,omitempty"`
}

type SetMemoryLifecycleInput struct {
	ID             string `json:"id"`
	MemoryID       string `json:"memoryId"`
	State          string `json:"state"`
	Lifecycle      string `json:"lifecycle"`
	LifecycleState string `json:"lifecycleState"`
}

type DeleteMemoryInput struct {
	ID       string `json:"id"`
	MemoryID string `json:"memoryId"`
}

type SetMemoryScopeInput struct {
	ID       string `json:"id"`
	MemoryID string `json:"memoryId"`
	Scope    string `json:"scope"`
}

type BackstoryMemoryInput struct {
	ID       string `json:"id"`
	MemoryID string `json:"memoryId"`
	Content  string `json:"content,omitempty"`
}

type ArchiveSearchInput struct {
	Query          string `json:"query"`
	IncludeDormant bool   `json:"includeDormant,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	Offset         int    `json:"offset,omitempty"`
}

type ArchiveSearchResultView struct {
	ID                string  `json:"id"`
	ConversationID    string  `json:"conversationId,omitempty"`
	ConversationTitle string  `json:"conversationTitle,omitempty"`
	MessageID         string  `json:"messageId,omitempty"`
	Role              string  `json:"role,omitempty"`
	Content           string  `json:"content"`
	Snippet           string  `json:"snippet,omitempty"`
	CreatedAt         string  `json:"createdAt,omitempty"`
	Score             float64 `json:"score,omitempty"`
	MatchType         string  `json:"matchType,omitempty"`
}

type ArchiveSearchResponseView struct {
	Results []ArchiveSearchResultView `json:"results"`
	Total   int                       `json:"total"`
	Query   string                    `json:"query"`
}

func (b *Bridge) ListMemories(input MemoryListInput) ([]MemoryView, error) {
	ctx, cancel := b.context()
	defer cancel()
	lifecycle := firstNonEmpty(input.LifecycleState, input.Lifecycle)
	includeDormant := input.IncludeDormant || lifecycle == "dormant" || lifecycle == "all"
	includeDeleted := input.IncludeDeleted || lifecycle == "deleted" || lifecycle == "all"
	options := storage.MemoryListOptions{
		AgentID:        b.personaProfileID(),
		IncludeDormant: includeDormant, IncludeDeleted: includeDeleted,
		Limit: input.Limit, Offset: input.Offset,
	}
	if input.Scope != "" && input.Scope != "all" {
		options.Scope = domain.MemoryScope(input.Scope)
		if !options.Scope.Valid() {
			return nil, fmt.Errorf("%w: invalid memory scope %q", domain.ErrInvalidArgument, input.Scope)
		}
	}
	// Push an exact lifecycle filter into SQL so it is applied before
	// LIMIT/OFFSET. Filtering after pagination returned short pages and made
	// records past the first page unreachable entirely.
	exactLifecycle := domain.MemoryLifecycle("")
	if lifecycle != "" && lifecycle != "all" {
		exactLifecycle = domain.MemoryLifecycle(lifecycle)
		if !exactLifecycle.Valid() {
			return nil, fmt.Errorf("%w: invalid memory lifecycle %q", domain.ErrInvalidArgument, lifecycle)
		}
		options.Lifecycle = exactLifecycle
	}
	if input.Kind != "" && input.Kind != "all" {
		options.Kind = domain.MemoryKind(input.Kind)
	}

	var memories []domain.Memory
	query := strings.TrimSpace(input.Query)
	if query == "" {
		var err error
		memories, err = b.repositories.Memories.List(ctx, options)
		if err != nil {
			return nil, err
		}
	} else {
		hits, err := b.repositories.Memories.Search(ctx, query, storage.MemorySearchOptions{
			AgentID: b.personaProfileID(), Scope: options.Scope,
			IncludeDormant: includeDormant, IncludeDeleted: includeDeleted, Kind: options.Kind,
			Lifecycle: exactLifecycle,
			Limit:     input.Limit, Offset: input.Offset,
		})
		if err != nil {
			return nil, err
		}
		memories = make([]domain.Memory, 0, len(hits))
		for _, hit := range hits {
			memories = append(memories, hit.Memory)
		}
	}
	views := make([]MemoryView, 0, len(memories))
	for _, item := range memories {
		view, err := b.memoryView(ctx, item)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

// SetMemoryScope publishes an owned memory to the local roster or revokes a
// prior publication. It is exposed only as an explicit owner UI action; the
// model-facing memory extractor always writes agent_private records.
func (b *Bridge) SetMemoryScope(input SetMemoryScopeInput) (MemoryView, error) {
	ctx, cancel := b.context()
	defer cancel()
	id := memoryInputID(input.ID, input.MemoryID)
	current, err := b.repositories.Memories.GetForAgent(ctx, b.personaProfileID(), id)
	if err != nil {
		return MemoryView{}, err
	}
	scope := domain.MemoryScope(strings.TrimSpace(input.Scope))
	if !scope.Valid() || scope == "" {
		return MemoryView{}, fmt.Errorf("%w: invalid memory scope %q", domain.ErrInvalidArgument, input.Scope)
	}
	if scope.Shared() && current.Sensitivity == domain.MemorySensitivityHighlySensitive {
		return MemoryView{}, fmt.Errorf("%w: highly sensitive memory cannot be shared", domain.ErrInvalidArgument)
	}
	if scope.Shared() && current.Nature == domain.MemoryNatureFiction {
		return MemoryView{}, fmt.Errorf("%w: fictional identity memory cannot be shared", domain.ErrInvalidArgument)
	}
	if current.Scope == scope {
		return b.memoryView(ctx, current)
	}
	previousVersion := current.Version
	current.Version++
	current.Scope = scope
	current.UpdatedAt = time.Now().UTC()
	operation := "publish"
	reason := "owner published memory to " + string(scope)
	if scope == domain.MemoryScopeAgentPrivate {
		operation = "revoke"
		reason = "owner revoked shared memory"
	}
	current.Reason = reason
	current, err = b.repositories.Memories.AppendVersionWithMetadata(ctx, current, previousVersion, storage.MemoryVersionMetadata{
		Operation: operation, ParentVersion: previousVersion, Reason: reason,
	})
	if err != nil {
		return MemoryView{}, err
	}
	return b.memoryView(ctx, current)
}

func (b *Bridge) UpdateMemory(input UpdateMemoryInput) (MemoryView, error) {
	ctx, cancel := b.context()
	defer cancel()
	id := memoryInputID(input.ID, input.MemoryID)
	current, err := b.repositories.Memories.GetForAgent(ctx, b.personaProfileID(), id)
	if err != nil {
		return MemoryView{}, err
	}
	if _, parseErr := memorycore.ParseBackstoryMemoryPayload(current.ContentJSON); current.Nature == domain.MemoryNatureFiction && parseErr == nil {
		return MemoryView{}, fmt.Errorf("%w: use backstory curation actions for fictional identity memory", domain.ErrNotPermitted)
	}
	if current.Nature == domain.MemoryNatureFiction && (input.Kind != nil || input.Nature != nil || input.ContentKind != nil) {
		return MemoryView{}, fmt.Errorf("%w: fictional memory provenance fields are immutable", domain.ErrNotPermitted)
	}
	if input.Content != nil {
		current.Content = strings.TrimSpace(*input.Content)
	}
	if input.Kind != nil {
		current.Kind = domain.MemoryKind(strings.TrimSpace(*input.Kind))
	}
	nature := input.Nature
	if nature == nil {
		nature = input.ContentKind
	}
	if nature != nil {
		current.Nature = domain.MemoryNature(strings.TrimSpace(*nature))
	}
	if input.Confidence != nil {
		current.Confidence = *input.Confidence
	}
	if input.Salience != nil {
		current.Salience = *input.Salience
	}
	if input.Valence != nil {
		current.Valence = *input.Valence
	}
	if input.Pinned != nil {
		current.Pinned = *input.Pinned
	}
	current.Version++
	current.UpdatedAt = time.Now().UTC()
	current.Reason = "user edited memory"
	if err := current.Validate(); err != nil {
		return MemoryView{}, err
	}
	if err := b.repositories.Memories.Save(ctx, current); err != nil {
		return MemoryView{}, err
	}
	return b.memoryView(ctx, current)
}

func (b *Bridge) UpdateBackstoryMemory(input BackstoryMemoryInput) (MemoryView, error) {
	ctx, cancel := b.context()
	defer cancel()
	content := strings.TrimSpace(input.Content)
	if content == "" {
		return MemoryView{}, fmt.Errorf("%w: backstory episode content is required", domain.ErrInvalidArgument)
	}
	id := memoryInputID(input.ID, input.MemoryID)
	current, err := b.repositories.Memories.GetForAgent(ctx, b.personaProfileID(), id)
	if err != nil {
		return MemoryView{}, err
	}
	payload, err := memorycore.ParseBackstoryMemoryPayload(current.ContentJSON)
	if err != nil || current.Nature != domain.MemoryNatureFiction || payload.AgentID != current.AgentID {
		return MemoryView{}, fmt.Errorf("%w: memory is not an owner-authored backstory episode", domain.ErrNotPermitted)
	}
	seed, _, err := b.repositories.Personalization.MigrateLegacyBackstory(ctx, current.AgentID, time.Now().UTC())
	if err != nil {
		return MemoryView{}, err
	}
	found := false
	for index := range seed.Backstory.Episodes {
		if strings.TrimSpace(seed.Backstory.Episodes[index].ID) == payload.EpisodeID {
			if strings.TrimSpace(seed.Backstory.Episodes[index].Content) == content {
				return b.memoryView(ctx, current)
			}
			seed.Backstory.Episodes[index].Content = content
			found = true
			break
		}
	}
	if !found {
		return MemoryView{}, fmt.Errorf("%w: backstory episode is absent from current owner seed", domain.ErrNotFound)
	}
	now := time.Now().UTC()
	if !now.After(seed.UpdatedAt) {
		now = seed.UpdatedAt.Add(time.Nanosecond)
	}
	previousVersion := seed.Version
	previousRevisionID := seed.RevisionID
	seed.Version++
	seed.RevisionID = domain.ID(fmt.Sprintf("%s:personalization:v%d", seed.AgentID, seed.Version))
	seed.ParentID = previousRevisionID
	seed.ParentVersion = previousVersion
	seed.Operation = domain.PersonalizationOperationUpdate
	seed.Reason = "owner edited fictional backstory episode " + payload.EpisodeID
	seed.UpdatedAt = now
	seed, err = b.repositories.Personalization.AppendVersion(ctx, seed, previousVersion)
	if err != nil {
		return MemoryView{}, err
	}
	engine, err := memorycore.NewEngine(memorycore.Config{AgentID: current.AgentID, Store: sqliteMemoryAdapter{repositories: b.repositories, agentID: current.AgentID}})
	if err != nil {
		return MemoryView{}, err
	}
	results, err := engine.HydrateBackstory(ctx, seed)
	if err != nil {
		return MemoryView{}, err
	}
	result := memorycore.WriteResult{}
	for _, candidate := range results {
		if candidate.Memory.ID == current.ID {
			result = candidate
			break
		}
	}
	if result.Memory.ID.Empty() {
		return MemoryView{}, fmt.Errorf("%w: updated backstory episode was not hydrated", domain.ErrNotFound)
	}
	b.emitMemoryUpdated(1)
	return b.memoryView(ctx, result.Memory)
}

func (b *Bridge) DisableBackstoryMemory(input BackstoryMemoryInput) (MemoryView, error) {
	ctx, cancel := b.context()
	defer cancel()
	id := memoryInputID(input.ID, input.MemoryID)
	agentID := b.personaProfileID()
	engine, err := memorycore.NewEngine(memorycore.Config{AgentID: agentID, Store: sqliteMemoryAdapter{repositories: b.repositories, agentID: agentID}})
	if err != nil {
		return MemoryView{}, err
	}
	result, err := engine.DisableBackstoryMemory(ctx, id)
	if err != nil {
		return MemoryView{}, err
	}
	if result.Changed {
		b.emitMemoryUpdated(1)
	}
	return b.memoryView(ctx, result.Memory)
}

func (b *Bridge) RehydrateBackstoryMemory(input BackstoryMemoryInput) (MemoryView, error) {
	ctx, cancel := b.context()
	defer cancel()
	id := memoryInputID(input.ID, input.MemoryID)
	agentID := b.personaProfileID()
	current, err := b.repositories.Memories.GetForAgent(ctx, agentID, id)
	if err != nil {
		return MemoryView{}, err
	}
	payload, err := memorycore.ParseBackstoryMemoryPayload(current.ContentJSON)
	if err != nil || current.Nature != domain.MemoryNatureFiction || payload.AgentID != current.AgentID {
		return MemoryView{}, fmt.Errorf("%w: only owner-seed backstory memories can be rehydrated", domain.ErrNotPermitted)
	}
	seed, _, err := b.repositories.Personalization.MigrateLegacyBackstory(ctx, agentID, time.Now().UTC())
	if err != nil {
		return MemoryView{}, err
	}
	engine, err := memorycore.NewEngine(memorycore.Config{AgentID: agentID, Store: sqliteMemoryAdapter{repositories: b.repositories, agentID: agentID}})
	if err != nil {
		return MemoryView{}, err
	}
	result, err := engine.RehydrateBackstoryEpisode(ctx, seed, payload.EpisodeID)
	if err != nil {
		return MemoryView{}, err
	}
	if result.Changed {
		b.emitMemoryUpdated(1)
	}
	return b.memoryView(ctx, result.Memory)
}

func (b *Bridge) SetMemoryLifecycle(input SetMemoryLifecycleInput) (MemoryView, error) {
	ctx, cancel := b.context()
	defer cancel()
	id := memoryInputID(input.ID, input.MemoryID)
	current, err := b.repositories.Memories.GetForAgent(ctx, b.personaProfileID(), id)
	if err != nil {
		return MemoryView{}, err
	}
	if _, parseErr := memorycore.ParseBackstoryMemoryPayload(current.ContentJSON); current.Nature == domain.MemoryNatureFiction && parseErr == nil {
		return MemoryView{}, fmt.Errorf("%w: use backstory disable or rehydrate for owner-seed memory", domain.ErrNotPermitted)
	}
	state := domain.MemoryLifecycle(firstNonEmpty(input.State, input.LifecycleState, input.Lifecycle))
	now := time.Now().UTC()
	var next domain.Memory
	switch state {
	case domain.MemoryLifecycleActive:
		next, err = b.repositories.Memories.RestoreForAgent(ctx, b.personaProfileID(), id, current.Version, now, "user restored memory")
	case domain.MemoryLifecycleDormant:
		next, err = b.repositories.Memories.MarkDormantForAgent(ctx, b.personaProfileID(), id, current.Version, now, "user hid memory from active recall")
	case domain.MemoryLifecycleDeleted:
		next, err = b.repositories.Memories.SoftDeleteForAgent(ctx, b.personaProfileID(), id, current.Version, now, "user deleted memory")
	default:
		return MemoryView{}, fmt.Errorf("invalid memory lifecycle %q", state)
	}
	if err != nil {
		return MemoryView{}, err
	}
	return b.memoryView(ctx, next)
}

// DeleteMemory creates a recoverable tombstone. It never deletes source
// transcript messages; irreversible transcript deletion remains user-owned.
func (b *Bridge) DeleteMemory(input DeleteMemoryInput) error {
	ctx, cancel := b.context()
	defer cancel()
	id := memoryInputID(input.ID, input.MemoryID)
	current, err := b.repositories.Memories.GetForAgent(ctx, b.personaProfileID(), id)
	if err != nil {
		return err
	}
	if _, parseErr := memorycore.ParseBackstoryMemoryPayload(current.ContentJSON); current.Nature == domain.MemoryNatureFiction && parseErr == nil {
		return fmt.Errorf("%w: use backstory disable for owner-seed memory", domain.ErrNotPermitted)
	}
	if current.IsDeleted() {
		return nil
	}
	_, err = b.repositories.Memories.SoftDeleteForAgent(ctx, b.personaProfileID(), id, current.Version, time.Now().UTC(), "user deleted memory")
	return err
}

func (b *Bridge) SearchArchive(input ArchiveSearchInput) (ArchiveSearchResponseView, error) {
	ctx, cancel := b.context()
	defer cancel()
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return ArchiveSearchResponseView{}, errors.New("archive search query is required")
	}
	limit := input.Limit
	if limit <= 0 || limit > 100 {
		limit = 40
	}
	hits, err := b.repositories.Archive.Search(ctx, query, storage.ArchiveSearchOptions{
		AgentID: b.personaProfileID(), ConversationID: domain.ID(strings.TrimSpace(input.ConversationID)), Limit: limit,
		Offset: input.Offset, MaxTokens: 8_000,
	})
	if err != nil {
		return ArchiveSearchResponseView{}, err
	}
	results := make([]ArchiveSearchResultView, 0, len(hits))
	seen := make(map[domain.ID]struct{}, len(hits))
	for _, hit := range hits {
		seen[hit.Message.ID] = struct{}{}
		results = append(results, archiveView(hit.Message, hit.ConversationTitle, hit.Snippet, boundedScore(hit.Score), "lexical"))
	}
	// Deliberate recall may surface a dormant derived memory, but the UI gets
	// the original evidence message whenever provenance is available.
	if input.IncludeDormant && len(results) < limit {
		memoryHits, searchErr := b.repositories.Memories.Search(ctx, query, storage.MemorySearchOptions{
			AgentID: b.personaProfileID(), Scope: domain.MemoryScopeAgentPrivate,
			IncludeDormant: true, Deliberate: true, Limit: limit - len(results), MaxTokens: 4_000,
		})
		if searchErr != nil {
			return ArchiveSearchResponseView{}, searchErr
		}
		for _, memoryHit := range memoryHits {
			for _, source := range memoryHit.Sources {
				if source.MessageID.Empty() {
					continue
				}
				if _, exists := seen[source.MessageID]; exists {
					continue
				}
				message, getErr := b.repositories.Messages.Get(ctx, source.MessageID)
				if getErr != nil {
					if errors.Is(getErr, domain.ErrNotFound) {
						continue
					}
					return ArchiveSearchResponseView{}, getErr
				}
				title := ""
				if conversation, getErr := b.repositories.Conversations.Get(ctx, message.ConversationID); getErr == nil {
					title = conversation.Title
				}
				results = append(results, archiveView(message, title, memoryHit.Snippet, boundedScore(memoryHit.Score), "hybrid"))
				seen[source.MessageID] = struct{}{}
				if len(results) >= limit {
					break
				}
			}
			if len(results) >= limit {
				break
			}
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	return ArchiveSearchResponseView{Results: results, Total: len(results), Query: query}, nil
}

func (b *Bridge) memoryView(ctx context.Context, item domain.Memory) (MemoryView, error) {
	sources, err := b.repositories.Memories.ListSourcesForAgent(ctx, item.AgentID, item.ID, item.Version)
	if err != nil {
		return MemoryView{}, err
	}
	views := make([]MemorySourceView, 0, len(sources))
	for _, source := range sources {
		view := MemorySourceView{
			SourceType: source.SourceType, SourceID: string(source.SourceID),
			ConversationID: string(source.ConversationID), MessageID: string(source.MessageID),
			ExcerptHash: source.ExcerptHash, EvidenceWeight: 1,
			CreatedAt: source.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if !source.ConversationID.Empty() {
			if conversation, getErr := b.repositories.Conversations.Get(ctx, source.ConversationID); getErr == nil {
				view.ConversationTitle = conversation.Title
			}
		}
		if !source.MessageID.Empty() {
			if message, getErr := b.repositories.Messages.Get(ctx, source.MessageID); getErr == nil {
				view.Excerpt = truncateRunes(message.Content, 240)
			}
		} else if source.SourceType == "peer_dialogue_message" && !source.SourceID.Empty() {
			if message, getErr := b.repositories.PeerDialogueMessages.Get(ctx, source.SourceID); getErr == nil {
				view.Excerpt = truncateRunes(message.Content, 240)
			}
		}
		views = append(views, view)
	}
	result := MemoryView{
		ID: string(item.ID), AgentID: string(item.AgentID), Scope: string(item.Scope), Version: item.Version, Kind: string(item.Kind), Nature: string(item.Nature),
		Content: item.Content, Confidence: item.Confidence, Salience: item.Salience, Valence: item.Valence,
		Sensitivity: string(item.Sensitivity), Lifecycle: string(item.Lifecycle), Pinned: item.Pinned,
		AccessCount: item.AccessCount, DecayPolicy: string(item.Retention), EmbeddingVersion: item.EmbeddingVersion,
		CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: item.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Sources: views,
	}
	if profile, getErr := b.repositories.Agents.Get(ctx, item.AgentID); getErr == nil {
		result.AgentName = profile.Name
	}
	if !item.LastRecalledAt.IsZero() {
		result.LastRecalledAt = item.LastRecalledAt.UTC().Format(time.RFC3339Nano)
	}
	if item.Nature == domain.MemoryNatureFiction {
		if payload, parseErr := memorycore.ParseBackstoryMemoryPayload(item.ContentJSON); parseErr == nil && payload.AgentID == item.AgentID {
			result.Fiction = &FictionMemoryView{
				Provenance: memorycore.FictionProvenanceOwnerSeed, EpistemicStatus: payload.EpistemicStatus,
				OwnerAuthored: true, EpisodeID: payload.EpisodeID,
				PersonalizationRevisionID: string(payload.PersonalizationRevisionID),
			}
		} else if payload, parseErr := memorycore.ParseBackstoryInterpretationPayload(item.ContentJSON); parseErr == nil && payload.AgentID == item.AgentID {
			result.Fiction = &FictionMemoryView{
				Provenance: payload.Provenance, EpistemicStatus: payload.EpistemicStatus,
				SourceMemoryID: string(payload.SourceMemoryID), SourceVersion: payload.SourceVersion,
			}
		}
		if result.Fiction != nil && item.AccessCount > 0 {
			result.Fiction.RecallState = memorycore.FictionProvenanceRemembered
		}
		versions, versionErr := b.repositories.Memories.ListVersionsForAgent(ctx, item.AgentID, item.ID, 12)
		if versionErr != nil {
			return MemoryView{}, versionErr
		}
		result.History = make([]MemoryHistoryView, 0, len(versions))
		for _, version := range versions {
			result.History = append(result.History, MemoryHistoryView{
				Version: version.Memory.Version, Operation: version.Operation, Reason: version.Reason,
				CreatedAt: version.Memory.UpdatedAt.UTC().Format(time.RFC3339Nano),
			})
		}
	}
	return result, nil
}

func archiveView(message storage.Message, title, snippet string, score float64, matchType string) ArchiveSearchResultView {
	return ArchiveSearchResultView{
		ID: string(message.ID), ConversationID: string(message.ConversationID), ConversationTitle: title,
		MessageID: string(message.ID), Role: message.Role, Content: message.Content, Snippet: snippet,
		CreatedAt: message.CreatedAt.UTC().Format(time.RFC3339Nano), Score: score, MatchType: matchType,
	}
}

func memoryInputID(values ...string) domain.ID {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return domain.ID(strings.TrimSpace(value))
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func boundedScore(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	return value / (1 + value)
}
