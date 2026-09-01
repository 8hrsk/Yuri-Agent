package desktop

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

// The renderer is untrusted input, exactly like the scheduler payload that
// M-13 closed: every bound below is applied here, in Go, and never assumed to
// have been applied by the caller.
const (
	// defaultConversationPageLimit is what a caller that asks for no page size
	// gets. It is deliberately equal to the clamp below rather than smaller:
	// the sidebar has no paging control of its own yet, so a smaller default
	// would quietly hide conversations from an owner who has more of them. The
	// saving that matters is the per-conversation message window, not the
	// conversation count. Offset is already here for the day the sidebar grows
	// a "load more".
	defaultConversationPageLimit = 200
	// maxConversationPageLimit clamps an explicit page size. A metadata page is
	// cheap, but a page that also asks for transcripts expands every
	// conversation on it into messages, runs and tool calls, so the page size
	// multiplies into a much larger read.
	maxConversationPageLimit = 200
	// defaultConversationMessageLimit is the transcript tail one conversation
	// gets when a caller asks for transcripts without saying how deep. It is
	// deliberately larger than the renderer's own window (40 entries) so the
	// first "show earlier" click is served from memory.
	defaultConversationMessageLimit = 60
	// maxConversationMessageLimit clamps an explicit per-conversation message
	// limit, and also bounds one "show earlier" page.
	maxConversationMessageLimit = 500
)

// ConversationPageOptions is the renderer-facing page request for
// ListConversationsPage. A zero value asks for the defaults.
type ConversationPageOptions struct {
	// Limit is the number of conversations on the page.
	Limit int `json:"limit"`
	// Offset skips that many conversations, newest-updated first.
	Offset int `json:"offset"`
	// MessageLimit is how many of the newest messages each returned
	// conversation carries.
	//
	// Zero — the default — is none at all, and is what the sidebar asks for.
	// The sidebar draws a title, a one-line preview and a timestamp; the
	// preview is a column of its own, read for the whole page in one statement,
	// so dragging a transcript tail (and its runs, and their tool calls) behind
	// every conversation bought the renderer nothing it displays. The one
	// transcript actually opened is fetched by ListMessages instead.
	//
	// A caller that does want transcripts in the list says so with a positive
	// limit, which restores the older shape exactly.
	MessageLimit int `json:"messageLimit"`
}

// normalized rejects a negative bound and clamps an over-large one. Negatives
// are rejected rather than coerced to zero: a negative page size is a caller
// bug, and silently turning it into "the default" would hide it, whereas an
// over-large one is a plausible over-eager renderer and is served, clamped.
func (o ConversationPageOptions) normalized() (ConversationPageOptions, error) {
	if o.Limit < 0 {
		return ConversationPageOptions{}, fmt.Errorf("%w: conversation page limit cannot be negative", domain.ErrInvalidArgument)
	}
	if o.Offset < 0 {
		return ConversationPageOptions{}, fmt.Errorf("%w: conversation page offset cannot be negative", domain.ErrInvalidArgument)
	}
	if o.MessageLimit < 0 {
		return ConversationPageOptions{}, fmt.Errorf("%w: conversation message limit cannot be negative", domain.ErrInvalidArgument)
	}
	if o.Limit == 0 {
		o.Limit = defaultConversationPageLimit
	}
	if o.Limit > maxConversationPageLimit {
		o.Limit = maxConversationPageLimit
	}
	if o.MessageLimit > maxConversationMessageLimit {
		o.MessageLimit = maxConversationMessageLimit
	}
	return o, nil
}

// ChatHistoryPage is one page of transcript older than a cursor, together with
// the execution traces of the runs those messages belong to.
type ChatHistoryPage struct {
	ConversationID string            `json:"conversationId"`
	Messages       []ChatMessageView `json:"messages"`
	Traces         []RunTraceView    `json:"traces"`
	// HasMore is false once the page reached the start of the transcript.
	HasMore bool `json:"hasMore"`
}

// ListConversationsPage returns one page of conversations, newest-updated
// first, starting at Offset.
//
// Two shapes, chosen by MessageLimit:
//
//   - Zero (the default, and what the sidebar asks for) returns metadata only:
//     id, title, timestamp and a one-line preview. Two queries — the
//     conversations, then every preview in one set-based read — whatever the
//     page size.
//   - Positive returns that many of each conversation's newest messages plus
//     the traces of its recent runs. Four queries regardless of how many
//     conversations are on the page: conversations, then one set-based read
//     each for messages, runs and tool calls. It used to be 1 + 2C + R — a
//     query per conversation for messages, another for runs, and one per run
//     for its tool calls (the H-16 shape).
//
// Offset is what makes a store of more than one page reachable at all. Paging
// by offset can miss a conversation that another writer moves across the
// boundary while the reader pages, because the order key is updated_at and a
// reply reorders the list; that is a stale view of a list the reader is
// actively changing, not a lost conversation, and the alternative — a keyset
// cursor on (updated_at, id) — would still be reordered underneath the reader.
// The renderer deduplicates by id so a conversation that crosses the boundary
// the other way cannot appear twice.
func (b *Bridge) ListConversationsPage(options ConversationPageOptions) ([]ConversationView, error) {
	bounded, err := options.normalized()
	if err != nil {
		return nil, err
	}
	ctx, cancel := b.context()
	defer cancel()
	conversations, err := b.repositories.Conversations.ListPage(ctx, storage.ConversationListOptions{
		AgentID: b.personaProfileID(), Limit: bounded.Limit, Offset: bounded.Offset,
	})
	if err != nil {
		return nil, err
	}
	ids := make([]domain.ID, 0, len(conversations))
	for _, conversation := range conversations {
		ids = append(ids, conversation.ID)
	}
	if bounded.MessageLimit == 0 {
		return b.conversationMetadataViews(ctx, conversations, ids)
	}
	// One extra message per conversation is read purely to answer "is there
	// more?" without a second counting query; it is trimmed off below.
	messagesByConversation, err := b.repositories.Messages.ListTailByConversations(ctx, ids, bounded.MessageLimit+1)
	if err != nil {
		return nil, err
	}
	// A run produces at least one message, so the newest MessageLimit runs
	// always cover the runs the returned messages belong to.
	runsByConversation, err := b.repositories.Runs.ListRecentByConversations(ctx, ids, bounded.MessageLimit)
	if err != nil {
		return nil, err
	}
	if err := b.includeChildRuns(ctx, runsByConversation); err != nil {
		return nil, err
	}
	runIDs := make([]domain.ID, 0)
	for _, runs := range runsByConversation {
		for _, run := range runs {
			runIDs = append(runIDs, run.ID)
		}
	}
	callsByRun, err := b.repositories.ToolCalls.ListByRuns(ctx, runIDs)
	if err != nil {
		return nil, err
	}
	views := make([]ConversationView, 0, len(conversations))
	for _, conversation := range conversations {
		messages := messagesByConversation[conversation.ID]
		hasMore := len(messages) > bounded.MessageLimit
		if hasMore {
			messages = messages[len(messages)-bounded.MessageLimit:]
		}
		view := ConversationView{
			ID: string(conversation.ID), Title: conversation.Title, TitleSource: conversation.TitleSource,
			UpdatedAt:       conversation.UpdatedAt.UTC().Format(time.RFC3339Nano),
			Messages:        make([]ChatMessageView, 0, len(messages)),
			Traces:          make([]RunTraceView, 0),
			HasMoreMessages: hasMore,
		}
		for _, message := range messages {
			view.Messages = append(view.Messages, chatMessageView(message))
			if message.Content != "" {
				view.Preview = truncateRunes(message.Content, 100)
			}
		}
		for _, run := range runsByConversation[conversation.ID] {
			view.Traces = append(view.Traces, runTraceView(run, callsByRun[run.ID]))
		}
		views = append(views, view)
	}
	return views, nil
}

// conversationMetadataViews projects a page of conversations without their
// transcripts, reading every preview in one statement.
//
// Messages and Traces are non-nil empty slices rather than nil so the renderer
// receives the same JSON shape either way; HasMoreMessages is deliberately
// false, because whether a transcript continues is a fact about a transcript
// that has been loaded, and this page has loaded none. The renderer learns it
// from ListMessages when it opens the conversation.
func (b *Bridge) conversationMetadataViews(ctx context.Context, conversations []storage.Conversation, ids []domain.ID) ([]ConversationView, error) {
	previews, err := b.repositories.Messages.ListPreviewsByConversations(ctx, ids)
	if err != nil {
		return nil, err
	}
	views := make([]ConversationView, 0, len(conversations))
	for _, conversation := range conversations {
		views = append(views, ConversationView{
			ID: string(conversation.ID), Title: conversation.Title, TitleSource: conversation.TitleSource,
			UpdatedAt: conversation.UpdatedAt.UTC().Format(time.RFC3339Nano),
			Preview:   truncateRunes(previews[conversation.ID], 100),
			Messages:  make([]ChatMessageView, 0),
			Traces:    make([]RunTraceView, 0),
		})
	}
	return views, nil
}

// ListMessages serves the transcript's "show earlier" control: the page of
// messages immediately older than before, newest page first when before is
// empty.
//
// The cursor is the id of the oldest message the renderer already holds, not
// an offset, so a message arriving while the reader pages backwards cannot
// shift the window and make a page skip or repeat an entry.
func (b *Bridge) ListMessages(conversationID string, limit int, before string) (ChatHistoryPage, error) {
	id := domain.ID(strings.TrimSpace(conversationID))
	if id.Empty() {
		return ChatHistoryPage{}, fmt.Errorf("%w: conversation id is required", domain.ErrInvalidArgument)
	}
	if limit < 0 {
		return ChatHistoryPage{}, fmt.Errorf("%w: message limit cannot be negative", domain.ErrInvalidArgument)
	}
	if limit == 0 {
		limit = defaultConversationMessageLimit
	}
	if limit > maxConversationMessageLimit {
		limit = maxConversationMessageLimit
	}
	ctx, cancel := b.context()
	defer cancel()
	// Same ownership check the conversation list applies implicitly by
	// filtering on the active agent: a renderer must not be able to read
	// another agent's transcript by naming its conversation id.
	conversation, err := b.repositories.Conversations.Get(ctx, id)
	if err != nil {
		return ChatHistoryPage{}, err
	}
	if conversation.AgentID != b.personaProfileID() {
		return ChatHistoryPage{}, errors.New("conversation does not belong to the active agent")
	}
	// One extra row answers "is there more?" without a counting query.
	messages, err := b.repositories.Messages.ListBefore(ctx, id, domain.ID(strings.TrimSpace(before)), limit+1)
	if err != nil {
		return ChatHistoryPage{}, err
	}
	page := ChatHistoryPage{ConversationID: string(id), Messages: make([]ChatMessageView, 0, limit), Traces: make([]RunTraceView, 0)}
	if len(messages) > limit {
		page.HasMore = true
		messages = messages[len(messages)-limit:]
	}
	runIDs := make([]domain.ID, 0, len(messages))
	for _, message := range messages {
		view := chatMessageView(message)
		page.Messages = append(page.Messages, view)
		if view.RunID != "" {
			runIDs = append(runIDs, domain.ID(view.RunID))
		}
	}
	if len(runIDs) == 0 {
		return page, nil
	}
	runs, err := b.repositories.Runs.ListByIDs(ctx, runIDs)
	if err != nil {
		return ChatHistoryPage{}, err
	}
	childrenByParent, err := b.repositories.Runs.ListChildrenByParents(ctx, runIDs)
	if err != nil {
		return ChatHistoryPage{}, err
	}
	traceIDs := make([]domain.ID, 0, len(runs)+len(childrenByParent)*delegationMaxPerParent)
	for _, run := range runs {
		traceIDs = append(traceIDs, run.ID)
	}
	for _, children := range childrenByParent {
		for _, child := range children {
			traceIDs = append(traceIDs, child.ID)
		}
	}
	callsByRun, err := b.repositories.ToolCalls.ListByRuns(ctx, traceIDs)
	if err != nil {
		return ChatHistoryPage{}, err
	}
	// Emitted in the order the messages referenced their runs, so the page is
	// stable and free of duplicates even when several messages share a run.
	seen := make(map[domain.ID]struct{}, len(runs))
	for _, runID := range runIDs {
		if _, duplicate := seen[runID]; duplicate {
			continue
		}
		seen[runID] = struct{}{}
		run, ok := runs[runID]
		if !ok {
			continue
		}
		page.Traces = append(page.Traces, runTraceView(run, callsByRun[runID]))
		for _, child := range childrenByParent[runID] {
			page.Traces = append(page.Traces, runTraceView(child, callsByRun[child.ID]))
		}
	}
	return page, nil
}

// includeChildRuns attaches anonymous delegation traces to the conversation of
// their root run. Child runs intentionally have no conversation_id of their
// own, but their operational trace belongs next to the agent.delegate call
// that created them and must survive a renderer reload.
func (b *Bridge) includeChildRuns(ctx context.Context, runsByConversation map[domain.ID][]domain.AgentRun) error {
	parentIDs := make([]domain.ID, 0)
	conversationByParent := make(map[domain.ID]domain.ID)
	for conversationID, runs := range runsByConversation {
		for _, run := range runs {
			parentIDs = append(parentIDs, run.ID)
			conversationByParent[run.ID] = conversationID
		}
	}
	childrenByParent, err := b.repositories.Runs.ListChildrenByParents(ctx, parentIDs)
	if err != nil {
		return err
	}
	for parentID, children := range childrenByParent {
		conversationID, exists := conversationByParent[parentID]
		if !exists {
			continue
		}
		runsByConversation[conversationID] = append(runsByConversation[conversationID], children...)
	}
	for conversationID, runs := range runsByConversation {
		sort.SliceStable(runs, func(left, right int) bool {
			if runs[left].CreatedAt.Equal(runs[right].CreatedAt) {
				return runs[left].ID < runs[right].ID
			}
			return runs[left].CreatedAt.Before(runs[right].CreatedAt)
		})
		runsByConversation[conversationID] = runs
	}
	return nil
}

func chatMessageView(message storage.Message) ChatMessageView {
	return ChatMessageView{
		ID: string(message.ID), Role: message.Role, Content: message.Content,
		Status: message.Status, CreatedAt: message.CreatedAt.UTC().Format(time.RFC3339Nano),
		RunID: messageRunID(message.ProviderMeta), Attachments: attachmentViews(message.ProviderMeta),
	}
}

// runTraceView projects one run and its already-read tool calls. It takes the
// calls rather than reading them so the caller can fetch every run's calls in
// one set-based query instead of one query per run.
func runTraceView(run domain.AgentRun, calls []storage.ToolCall) RunTraceView {
	view := RunTraceView{
		ID: string(run.ID), Kind: string(run.Kind), ParentRunID: string(run.ParentRunID), Status: string(run.State),
		CreatedAt: run.CreatedAt.UTC().Format(time.RFC3339Nano), Failure: safeError(run.Failure),
		ProviderID: run.Inference.ProviderID, Model: run.Inference.Model,
		InputTokens: run.Usage.InputTokens, OutputTokens: run.Usage.OutputTokens, TotalTokens: run.Usage.TotalTokens,
		MaxSteps: run.Budget.MaxSteps, MaxTokens: run.Budget.MaxTokens, MaxToolCalls: run.Budget.MaxToolCalls,
		MaxDurationSeconds: int64(run.Budget.MaxDurationSeconds),
		FailureKind:        string(run.FailureInfo.Kind), Retryable: run.FailureInfo.Retryable, RetryAfterSeconds: run.FailureInfo.RetryAfterSeconds,
		ToolCalls: make([]ToolCallView, 0, len(calls)),
	}
	if !run.StartedAt.IsZero() {
		view.StartedAt = run.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if !run.FinishedAt.IsZero() {
		view.FinishedAt = run.FinishedAt.UTC().Format(time.RFC3339Nano)
	}
	if run.InferenceRouteSwitches > 0 && run.InitialInference != run.Inference {
		fallbackAt := run.StartedAt
		if fallbackAt.IsZero() {
			fallbackAt = run.CreatedAt
		}
		view.Fallback = &RunFallbackView{
			FromProviderID: run.InitialInference.ProviderID,
			FromModel:      run.InitialInference.Model,
			ToProviderID:   run.Inference.ProviderID,
			ToModel:        run.Inference.Model,
			Reason:         "Основной маршрут завершился provider-ошибкой",
			CreatedAt:      fallbackAt.UTC().Format(time.RFC3339Nano),
		}
	}
	for _, call := range calls {
		view.ToolCalls = append(view.ToolCalls, storedToolCallView(call))
	}
	return view
}
