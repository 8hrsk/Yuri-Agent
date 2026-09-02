package desktop

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

const chatEventName = "yuri:chat"
const conversationEventName = "yuri:conversation"

type ChatRequest struct {
	ConversationID   string                `json:"conversationId"`
	Text             string                `json:"text"`
	RetryOfMessageID string                `json:"retryOfMessageId,omitempty"`
	VoiceClip        string                `json:"voiceClip,omitempty"`
	Attachments      []ChatAttachmentInput `json:"attachments,omitempty"`
}

type ChatMessageView struct {
	ID          string               `json:"id"`
	Role        string               `json:"role"`
	Content     string               `json:"content"`
	Status      string               `json:"status"`
	CreatedAt   string               `json:"createdAt"`
	RunID       string               `json:"runId,omitempty"`
	Attachments []ChatAttachmentView `json:"attachments,omitempty"`
}

type ConversationView struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	TitleSource string            `json:"titleSource"`
	Preview     string            `json:"preview"`
	UpdatedAt   string            `json:"updatedAt"`
	Messages    []ChatMessageView `json:"messages"`
	Traces      []RunTraceView    `json:"traces"`
	// HasMoreMessages reports that the transcript continues before the first
	// returned message, so the renderer can offer to page further back.
	HasMoreMessages bool `json:"hasMoreMessages"`
}

// RunTraceView is the user-visible execution history for one agent run. It
// intentionally contains lifecycle events and redacted tool data, never
// provider reasoning or hidden chain-of-thought.
type RunTraceView struct {
	ID                 string           `json:"id"`
	Kind               string           `json:"kind"`
	ParentRunID        string           `json:"parentRunId,omitempty"`
	Status             string           `json:"status"`
	CreatedAt          string           `json:"createdAt"`
	StartedAt          string           `json:"startedAt,omitempty"`
	FinishedAt         string           `json:"finishedAt,omitempty"`
	Failure            string           `json:"failure,omitempty"`
	ProviderID         string           `json:"providerId,omitempty"`
	Model              string           `json:"model,omitempty"`
	InputTokens        int64            `json:"inputTokens,omitempty"`
	OutputTokens       int64            `json:"outputTokens,omitempty"`
	TotalTokens        int64            `json:"totalTokens,omitempty"`
	MaxSteps           int              `json:"maxSteps,omitempty"`
	MaxTokens          int64            `json:"maxTokens,omitempty"`
	MaxToolCalls       int              `json:"maxToolCalls,omitempty"`
	MaxDurationSeconds int64            `json:"maxDurationSeconds,omitempty"`
	FailureKind        string           `json:"failureKind,omitempty"`
	Retryable          bool             `json:"retryable,omitempty"`
	RetryAfterSeconds  int64            `json:"retryAfterSeconds,omitempty"`
	Fallback           *RunFallbackView `json:"fallback,omitempty"`
	ToolCalls          []ToolCallView   `json:"toolCalls"`
}

// RunFallbackView contains only non-secret route provenance. Provider errors
// remain in the redacted audit record and are never restored into chat history.
type RunFallbackView struct {
	FromProviderID string `json:"fromProviderId,omitempty"`
	FromModel      string `json:"fromModel,omitempty"`
	ToProviderID   string `json:"toProviderId,omitempty"`
	ToModel        string `json:"toModel,omitempty"`
	Reason         string `json:"reason"`
	CreatedAt      string `json:"createdAt"`
}

type ChatToolDescriptorView struct {
	Name         string   `json:"name"`
	Label        string   `json:"label"`
	Description  string   `json:"description"`
	Risk         string   `json:"risk"`
	Capabilities []string `json:"capabilities"`
}

type ToolCallView struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Label      string         `json:"label"`
	Risk       string         `json:"risk"`
	Status     string         `json:"status"`
	Args       map[string]any `json:"args"`
	Result     string         `json:"result,omitempty"`
	StartedAt  string         `json:"startedAt,omitempty"`
	FinishedAt string         `json:"finishedAt,omitempty"`
}

type ApprovalView struct {
	ID             string `json:"id"`
	ToolCallID     string `json:"toolCallId"`
	Title          string `json:"title"`
	Explanation    string `json:"explanation"`
	Risk           string `json:"risk"`
	Scope          string `json:"scope"`
	ExpiresAt      string `json:"expiresAt,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Path           string `json:"path,omitempty"`
	PermissionRoot string `json:"permissionRoot,omitempty"`
	CanRemember    bool   `json:"canRemember,omitempty"`
}

// ChatEvent is the stable Wails boundary. Payload fields are intentionally
// explicit so hidden model reasoning and provider-specific responses cannot
// leak into the UI event stream.
type ChatEvent struct {
	Type               string        `json:"type"`
	ConversationID     string        `json:"conversationId,omitempty"`
	RunID              string        `json:"runId"`
	RunKind            string        `json:"runKind,omitempty"`
	ParentRunID        string        `json:"parentRunId,omitempty"`
	ProviderID         string        `json:"providerId,omitempty"`
	Model              string        `json:"model,omitempty"`
	InputTokens        int64         `json:"inputTokens,omitempty"`
	OutputTokens       int64         `json:"outputTokens,omitempty"`
	TotalTokens        int64         `json:"totalTokens,omitempty"`
	MaxSteps           int           `json:"maxSteps,omitempty"`
	MaxTokens          int64         `json:"maxTokens,omitempty"`
	MaxToolCalls       int           `json:"maxToolCalls,omitempty"`
	MaxDurationSeconds int           `json:"maxDurationSeconds,omitempty"`
	FailureKind        string        `json:"failureKind,omitempty"`
	Retryable          bool          `json:"retryable,omitempty"`
	RetryAfterSeconds  int64         `json:"retryAfterSeconds,omitempty"`
	CreatedAt          string        `json:"createdAt"`
	MessageID          string        `json:"messageId,omitempty"`
	Delta              string        `json:"delta,omitempty"`
	Status             string        `json:"status,omitempty"`
	Label              string        `json:"label,omitempty"`
	Error              string        `json:"error,omitempty"`
	FromProviderID     string        `json:"fromProviderId,omitempty"`
	FromModel          string        `json:"fromModel,omitempty"`
	ToProviderID       string        `json:"toProviderId,omitempty"`
	ToModel            string        `json:"toModel,omitempty"`
	Reason             string        `json:"reason,omitempty"`
	ToolCall           *ToolCallView `json:"toolCall,omitempty"`
	Approval           *ApprovalView `json:"approval,omitempty"`
}

type ChatRunResult struct {
	RunID  string      `json:"runId"`
	Status string      `json:"status"`
	Events []ChatEvent `json:"events,omitempty"`
}

type approvalDecisionInput struct {
	ApprovalID string `json:"approvalId"`
	Decision   string `json:"decision"`
}

// RenameConversation is an explicit owner action. The storage operation is a
// scoped atomic write and marks the title as user-authored, which makes it
// impossible for a title request already in flight to replace it later.
func (b *Bridge) RenameConversation(input RenameConversationInput) (ConversationView, error) {
	conversationID := domain.ID(strings.TrimSpace(input.ConversationID))
	if conversationID.Empty() {
		return ConversationView{}, fmt.Errorf("%w: conversation id is required", domain.ErrInvalidArgument)
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return ConversationView{}, fmt.Errorf("%w: conversation title is required", domain.ErrInvalidArgument)
	}
	ctx, cancel := b.context()
	defer cancel()
	if err := b.repositories.Conversations.Rename(ctx, conversationID, b.personaProfileID(), title, time.Now().UTC()); err != nil {
		return ConversationView{}, err
	}
	conversation, err := b.repositories.Conversations.Get(ctx, conversationID)
	if err != nil {
		return ConversationView{}, err
	}
	b.emitConversationUpdated(conversationID)
	return ConversationView{
		ID: string(conversation.ID), Title: conversation.Title, TitleSource: conversation.TitleSource,
		UpdatedAt: conversation.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Messages:  []ChatMessageView{}, Traces: []RunTraceView{},
	}, nil
}

type RenameConversationInput struct {
	ConversationID string `json:"conversationId"`
	Title          string `json:"title"`
}

type DeleteConversationInput struct {
	ConversationID string `json:"conversationId"`
}

func (b *Bridge) DeleteConversation(input DeleteConversationInput) error {
	conversationID := domain.ID(strings.TrimSpace(input.ConversationID))
	if conversationID.Empty() {
		return fmt.Errorf("%w: conversation id is required", domain.ErrInvalidArgument)
	}
	ctx, cancel := b.context()
	defer cancel()
	if err := b.repositories.Conversations.Delete(ctx, conversationID, b.personaProfileID()); err != nil {
		return err
	}
	b.emitConversationUpdated(conversationID)
	return nil
}

// ListConversations is the no-argument form kept for callers bound before
// paging existed — the Wails-generated renderer bindings of an older build and
// the Go smoke tests. It is the default page rather than the whole store: the
// unbounded read is the bug being fixed, so there is no caller left that wants
// it.
//
// It asks for transcripts explicitly. A caller bound before paging existed has
// no way to fetch a transcript separately, so the metadata-only page that
// ListConversationsPage now returns by default would leave it with a list of
// titles and no way to read any of them.
func (b *Bridge) ListConversations() ([]ConversationView, error) {
	return b.ListConversationsPage(ConversationPageOptions{MessageLimit: defaultConversationMessageLimit})
}

func (b *Bridge) NewConversation(title string) (ConversationView, error) {
	ctx, cancel := b.context()
	defer cancel()
	id, err := domain.NewID("conversation")
	if err != nil {
		return ConversationView{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = storage.DefaultConversationTitle
	}
	titleSource := storage.ConversationTitleSourceUser
	if title == storage.DefaultConversationTitle {
		titleSource = storage.ConversationTitleSourceDefault
	}
	now := time.Now().UTC()
	if err := b.repositories.Conversations.Create(ctx, storage.Conversation{
		ID: id, AgentID: b.personaProfileID(), Title: truncateRunes(title, 80), TitleSource: titleSource, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return ConversationView{}, err
	}
	return ConversationView{
		ID: string(id), Title: truncateRunes(title, 80), TitleSource: titleSource, Preview: "Пока нет сообщений",
		UpdatedAt: now.Format(time.RFC3339Nano), Messages: []ChatMessageView{}, Traces: []RunTraceView{},
	}, nil
}

func storedToolCallView(call storage.ToolCall) ToolCallView {
	args := make(map[string]any)
	_ = json.Unmarshal([]byte(call.ArgsRedacted), &args)
	view := ToolCallView{
		ID: string(call.ID), Name: call.ToolID, Label: toolLabel(call.ToolID),
		Risk: string(call.Risk), Status: toolCallStatus(call.Status), Args: args,
		Result: call.ResultRef, StartedAt: call.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if call.Status != storage.ToolCallRunning && call.Status != storage.ToolCallPending {
		view.FinishedAt = call.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return view
}

func toolCallStatus(status string) string {
	switch status {
	case storage.ToolCallSucceeded:
		return "completed"
	case storage.ToolCallFailed:
		return "failed"
	default:
		return status
	}
}

func messageRunID(providerMeta string) string {
	var metadata struct {
		RunID string `json:"run_id"`
	}
	if json.Unmarshal([]byte(providerMeta), &metadata) != nil {
		return ""
	}
	return strings.TrimSpace(metadata.RunID)
}

// ListChatTools reports only tools currently available to a new foreground
// run. In particular, filesystem tools are absent until the owner grants at
// least one directory.
func (b *Bridge) ListChatTools() ([]ChatToolDescriptorView, error) {
	registry, err := b.chatTools(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	descriptors := registry.Descriptors()
	views := make([]ChatToolDescriptorView, 0, len(descriptors)+2)
	for _, descriptor := range descriptors {
		capabilities := make([]string, 0, len(descriptor.Capabilities))
		for _, capability := range descriptor.Capabilities {
			capabilities = append(capabilities, string(capability))
		}
		views = append(views, ChatToolDescriptorView{
			Name: descriptor.Name, Label: toolLabel(descriptor.Name), Description: descriptor.Description,
			Risk: string(descriptor.Risk), Capabilities: capabilities,
		})
	}
	// These two normalized agent tools are added per run because they carry the
	// active agent and parent run IDs, but they are always available in a normal
	// foreground conversation.
	views = append(views,
		ChatToolDescriptorView{Name: delegationToolID, Label: toolLabel(delegationToolID), Description: "Делегировать ограниченную задачу обезличенному субагенту.", Risk: string(domain.RiskLow)},
		ChatToolDescriptorView{Name: peerDialogueToolID, Label: toolLabel(peerDialogueToolID), Description: "Запустить ограниченный фоновый диалог с другим агентом.", Risk: string(domain.RiskLow)},
	)
	return views, nil
}
