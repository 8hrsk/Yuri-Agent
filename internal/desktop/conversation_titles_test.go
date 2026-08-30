package desktop

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

type conversationTitleBackend struct {
	mu       sync.Mutex
	requests []agent.ModelRequest
	events   []agent.ModelEvent
	started  chan struct{}
	release  chan struct{}
}

func (backend *conversationTitleBackend) Start(ctx context.Context, request agent.ModelRequest) (agent.ModelStream, error) {
	backend.mu.Lock()
	backend.requests = append(backend.requests, request)
	backend.mu.Unlock()
	if backend.started != nil {
		select {
		case <-backend.started:
		default:
			close(backend.started)
		}
	}
	if backend.release != nil {
		select {
		case <-backend.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &conversationTitleStream{ctx: ctx, events: append([]agent.ModelEvent(nil), backend.events...)}, nil
}

func (backend *conversationTitleBackend) Requests() []agent.ModelRequest {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return append([]agent.ModelRequest(nil), backend.requests...)
}

type conversationTitleStream struct {
	ctx    context.Context
	events []agent.ModelEvent
	index  int
}

func (stream *conversationTitleStream) Recv(ctx context.Context) (agent.ModelEvent, error) {
	if stream.index >= len(stream.events) {
		return agent.ModelEvent{}, io.EOF
	}
	select {
	case <-ctx.Done():
		return agent.ModelEvent{}, ctx.Err()
	default:
	}
	event := stream.events[stream.index]
	stream.index++
	return event, nil
}

func (*conversationTitleStream) Close() error { return nil }

func TestGenerateConversationTitleUsesBoundedToollessRequest(t *testing.T) {
	backend := &conversationTitleBackend{events: []agent.ModelEvent{
		{Type: agent.ModelEventTextDelta, Delta: `{"title":"Сводка проекта"}`},
		{Type: agent.ModelEventCompleted},
	}}
	longRequest := strings.Repeat("я", conversationTitleMaxInputRunes+100)
	title, err := generateConversationTitle(context.Background(), backend, "test-model", longRequest)
	if err != nil || title != "Сводка проекта" {
		t.Fatalf("generated title = %q, err=%v", title, err)
	}
	requests := backend.Requests()
	if len(requests) != 1 {
		t.Fatalf("backend requests = %d, want 1", len(requests))
	}
	request := requests[0]
	if len(request.Tools) != 0 || request.MaxOutputTokens != conversationTitleMaxTokens || request.Metadata["purpose"] != conversationTitlePurpose || len(request.Messages) != 2 {
		t.Fatalf("unsafe title request = %#v", request)
	}
	if got := len([]rune(request.Messages[1].Content)); got > conversationTitleMaxInputRunes+200 {
		t.Fatalf("title request message has %d runes, input bound was not applied", got)
	}
	if !strings.Contains(request.Messages[1].Content, `"request"`) {
		t.Fatalf("title request did not carry JSON data envelope: %q", request.Messages[1].Content)
	}
}

func TestGenerateConversationTitleRejectsToolCallsAndSanitizesOutput(t *testing.T) {
	backend := &conversationTitleBackend{events: []agent.ModelEvent{
		{Type: agent.ModelEventToolCallStarted, ToolCallID: "unexpected", ToolName: "filesystem.read"},
	}}
	if _, err := generateConversationTitle(context.Background(), backend, "test-model", "прочитай файл"); err == nil {
		t.Fatal("title generation accepted a tool call")
	}
	for _, test := range []struct {
		raw, want string
	}{
		{raw: "```json\n{\"title\":\"  Разобрать   заметки  \"}\n```", want: "Разобрать заметки"},
		{raw: "\"Проверить отчёт\"", want: "Проверить отчёт"},
		{raw: "без названия", want: ""},
		{raw: `{"foo":"bar"}`, want: ""},
		{raw: `{"title":"План","extra":true}`, want: ""},
	} {
		got := sanitizeConversationTitle(test.raw)
		if test.want == "" {
			if got != "" && !isGenericConversationTitle(got) {
				t.Fatalf("sanitize(%q) = %q, want empty or generic title", test.raw, got)
			}
			continue
		}
		if got != test.want {
			t.Errorf("sanitize(%q) = %q, want %q", test.raw, got, test.want)
		}
	}
}

func TestConversationTitleEligibilitySkipsOwnerNamedConversation(t *testing.T) {
	request := ChatRequest{Text: "Первый запрос"}
	transcript := []agent.Message{{Role: agent.RoleUser, Content: request.Text}}
	defaultConversation := storage.Conversation{Title: storage.DefaultConversationTitle, TitleSource: storage.ConversationTitleSourceDefault}
	if !conversationTitleEligible(defaultConversation, request, domain.RunKindInteractive, transcript) {
		t.Fatal("default first conversation is not title-eligible")
	}
	for _, source := range []string{storage.ConversationTitleSourceUser, storage.ConversationTitleSourceGenerated} {
		conversation := defaultConversation
		conversation.TitleSource = source
		if conversationTitleEligible(conversation, request, domain.RunKindInteractive, transcript) {
			t.Fatalf("%s conversation remained title-eligible", source)
		}
	}
	if conversationTitleEligible(defaultConversation, request, domain.RunKindBackground, transcript) {
		t.Fatal("background turn became title-eligible")
	}
	if conversationTitleEligible(defaultConversation, ChatRequest{Text: request.Text, RetryOfMessageID: "message-1"}, domain.RunKindInteractive, transcript) {
		t.Fatal("retry became title-eligible")
	}
}

func TestScheduleConversationTitlePersistsFallbackWithoutBlockingChat(t *testing.T) {
	bridge := newAgentTestBridge(t)
	agentID := bridge.personaProfileID()
	now := time.Now().UTC()
	conversationID := domain.ID("conversation-title-fallback")
	if err := bridge.repositories.Conversations.Create(context.Background(), storage.Conversation{
		ID: conversationID, AgentID: agentID, Title: storage.DefaultConversationTitle,
		TitleSource: storage.ConversationTitleSourceDefault, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	backgroundCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bridge.backgroundCtx = backgroundCtx
	backend := &conversationTitleBackend{events: []agent.ModelEvent{{Type: agent.ModelEventCompleted}}}
	start := time.Now()
	bridge.scheduleConversationTitle(backend, "test-model", agentID, conversationID, "Найди и объясни отчёт")
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("scheduling title blocked for %s", elapsed)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		conversation, err := bridge.repositories.Conversations.Get(context.Background(), conversationID)
		if err != nil {
			t.Fatal(err)
		}
		if conversation.TitleSource == storage.ConversationTitleSourceGenerated {
			if conversation.Title != "Найди и объясни отчёт" {
				t.Fatalf("fallback title = %q", conversation.Title)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background title fallback was not persisted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	bridge.background.Wait()
}

func TestRenameConversationReturnsUserOwnedView(t *testing.T) {
	bridge := newAgentTestBridge(t)
	created, err := bridge.NewConversation(storage.DefaultConversationTitle)
	if err != nil {
		t.Fatal(err)
	}
	view, err := bridge.RenameConversation(RenameConversationInput{ConversationID: created.ID, Title: "Моё имя"})
	if err != nil {
		t.Fatal(err)
	}
	if view.ID != created.ID || view.Title != "Моё имя" || view.TitleSource != storage.ConversationTitleSourceUser {
		t.Fatalf("rename view = %#v", view)
	}
	if _, err := bridge.repositories.Conversations.UpdateTitleIfDefault(context.Background(), domain.ID(created.ID), bridge.personaProfileID(), "Поздняя генерация", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	conversation, err := bridge.repositories.Conversations.Get(context.Background(), domain.ID(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Title != "Моё имя" {
		t.Fatal("owner rename was overwritten")
	}
}

func TestFallbackConversationTitleIsBoundedAndDeterministic(t *testing.T) {
	input := "  Первая строка запроса. Вторая часть не должна попасть в fallback\nещё текст"
	if got, want := fallbackConversationTitle(input), "Первая строка запроса"; got != want {
		t.Fatalf("fallback title = %q, want %q", got, want)
	}
	if len([]rune(fallbackConversationTitle(strings.Repeat("x", 500)))) > conversationTitleMaxRunes {
		t.Fatal("fallback title exceeded title bound")
	}
}
