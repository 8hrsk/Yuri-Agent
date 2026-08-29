package codexapp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

// Backend adapts the official Codex agent harness to Yuri's normalized model
// event boundary. Stage 1 intentionally allows one active Codex turn per
// process, matching the single-owner desktop interaction model.
type Backend struct {
	Client        *Client
	CWD           string
	ReadableRoots []string

	turns chan struct{}
}

func NewBackend(client *Client, cwd string, readableRoots []string) (*Backend, error) {
	if client == nil {
		return nil, errors.New("Codex backend: client is required")
	}
	return &Backend{
		Client: client, CWD: cwd, ReadableRoots: append([]string(nil), readableRoots...),
		turns: make(chan struct{}, 1),
	}, nil
}

func (backend *Backend) Start(ctx context.Context, request agent.ModelRequest) (agent.ModelStream, error) {
	if err := request.Valid(); err != nil {
		return nil, err
	}
	select {
	case backend.turns <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-backend.turns }
	model := request.Model
	if model == "codex-default" || model == "gpt-5-codex" || model == "gpt-4o-mini" {
		model = ""
	}
	dynamicTools, dynamicToolNames := dynamicToolSpecs(request.Tools)
	thread, err := backend.Client.StartThreadWithOptions(ctx, ThreadOptions{
		Model: model, CWD: backend.CWD, ReadableRoots: backend.ReadableRoots,
		DynamicTools: dynamicTools,
	})
	if err != nil {
		release()
		return nil, fmt.Errorf("start Codex thread: %w", err)
	}
	prompt, err := encodeConversation(request.Messages)
	if err != nil {
		release()
		return nil, err
	}
	turn, err := backend.Client.StartTurnWithOptions(ctx, TurnOptions{
		ThreadID: thread.ID, Text: prompt, CWD: backend.CWD, Model: model,
		ReadableRoots: backend.ReadableRoots,
	})
	if err != nil {
		release()
		return nil, fmt.Errorf("start Codex turn: %w", err)
	}
	return &codexModelStream{
		client: backend.Client, events: backend.Client.Events(),
		threadID: thread.ID, turnID: turn.ID, release: release,
		dynamicToolNames: dynamicToolNames,
	}, nil
}

func encodeConversation(messages []agent.Message) (string, error) {
	type transcriptMessage struct {
		Role       agent.Role       `json:"role"`
		Content    string           `json:"content,omitempty"`
		Name       string           `json:"name,omitempty"`
		ToolCallID string           `json:"tool_call_id,omitempty"`
		ToolCalls  []agent.ToolCall `json:"tool_calls,omitempty"`
	}
	transcript := make([]transcriptMessage, 0, len(messages))
	for _, message := range messages {
		transcript = append(transcript, transcriptMessage{
			Role: message.Role, Content: message.Content, Name: message.Name,
			ToolCallID: message.ToolCallID, ToolCalls: message.ToolCalls,
		})
	}
	encoded, err := json.Marshal(transcript)
	if err != nil {
		return "", fmt.Errorf("encode Codex transcript: %w", err)
	}
	return "Produce the assistant response for the final user request in this structured transcript. " +
		"Preserve role boundaries and do not reveal hidden reasoning. Use only the supplied dynamic Yuri tools for actions; " +
		"never claim that an action succeeded until its tool result confirms it. Writes, deletion, network sends, and other side effects " +
		"must go through a Yuri tool so the local policy layer can request approval or deny them.\n" +
		"<conversation-json>" + string(encoded) + "</conversation-json>", nil
}

type codexModelStream struct {
	client   *Client
	events   <-chan Event
	threadID string
	turnID   string
	release  func()

	mu        sync.Mutex
	completed bool
	closed    bool
	once      sync.Once
	// dynamicRequests maps Yuri's stable tool call id to the opaque JSON-RPC
	// request id that the app server expects in the response. The model-facing
	// event deliberately exposes only the stable call id.
	dynamicRequests map[string]json.RawMessage
	// dynamicToolNames maps Responses-compatible provider aliases back to the
	// stable Yuri tool ids. Yuri ids intentionally use namespaces such as
	// "filesystem.read", while Codex only accepts [a-zA-Z0-9_-]+.
	dynamicToolNames map[string]string
}

func (stream *codexModelStream) Recv(ctx context.Context) (agent.ModelEvent, error) {
	stream.mu.Lock()
	if stream.closed || stream.completed {
		stream.mu.Unlock()
		return agent.ModelEvent{}, io.EOF
	}
	stream.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			interruptCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = stream.client.InterruptTurn(interruptCtx, stream.threadID, stream.turnID)
			cancel()
			return agent.ModelEvent{}, ctx.Err()
		case event, ok := <-stream.events:
			if !ok {
				return agent.ModelEvent{}, ErrClosed
			}
			if event.IsServerRequest() {
				if event.Method == "item/tool/call" {
					modelEvent, err := stream.dynamicToolCall(event)
					if err != nil {
						return agent.ModelEvent{}, err
					}
					if modelEvent.Type == "" {
						continue
					}
					return modelEvent, nil
				}
				// App-server requests other than dynamic Yuri tools are not part of
				// the normalized approval boundary yet. Keep the existing fail-closed
				// behavior for harness-owned side effects.
				_ = stream.client.Respond(event.ID, map[string]string{"decision": "decline"}, nil)
				continue
			}
			switch event.Method {
			case "item/agentMessage/delta":
				var params struct {
					ThreadID string `json:"threadId"`
					TurnID   string `json:"turnId"`
					Delta    string `json:"delta"`
				}
				if err := json.Unmarshal(event.Params, &params); err != nil {
					return agent.ModelEvent{}, fmt.Errorf("decode Codex text delta: %w", err)
				}
				if !stream.matches(params.ThreadID, params.TurnID) || params.Delta == "" {
					continue
				}
				return agent.ModelEvent{Type: agent.ModelEventTextDelta, Delta: params.Delta}, nil
			case "turn/completed":
				var params struct {
					ThreadID string `json:"threadId"`
					Turn     struct {
						ID     string          `json:"id"`
						Status string          `json:"status"`
						Error  *codexTurnError `json:"error"`
					} `json:"turn"`
				}
				if err := json.Unmarshal(event.Params, &params); err != nil {
					return agent.ModelEvent{}, fmt.Errorf("decode Codex completion: %w", err)
				}
				if !stream.matches(params.ThreadID, params.Turn.ID) {
					continue
				}
				if params.Turn.Status == "failed" {
					return agent.ModelEvent{}, safeCodexTurnError(params.Turn.Error)
				}
				if params.Turn.Status == "interrupted" {
					return agent.ModelEvent{}, context.Canceled
				}
				stream.mu.Lock()
				stream.completed = true
				stream.mu.Unlock()
				return agent.ModelEvent{Type: agent.ModelEventCompleted, FinishReason: params.Turn.Status}, nil
			case "error":
				var params struct {
					ThreadID  string          `json:"threadId"`
					TurnID    string          `json:"turnId"`
					WillRetry bool            `json:"willRetry"`
					Error     *codexTurnError `json:"error"`
				}
				if err := json.Unmarshal(event.Params, &params); err != nil {
					return agent.ModelEvent{}, errors.New("Codex app server reported a turn error")
				}
				if !stream.matches(params.ThreadID, params.TurnID) || params.WillRetry {
					continue
				}
				return agent.ModelEvent{}, safeCodexTurnError(params.Error)
			}
		}
	}
}

type codexTurnError struct {
	Message        string          `json:"message"`
	CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
}

func safeCodexTurnError(turnError *codexTurnError) error {
	if turnError == nil {
		return errors.New("Codex turn failed")
	}
	message := strings.ToLower(turnError.Message)
	switch {
	case strings.Contains(message, "not supported when using codex with a chatgpt account"):
		return errors.New("Codex: выбранная модель недоступна для ChatGPT OAuth; используйте модель аккаунта по умолчанию")
	case strings.Contains(string(turnError.CodexErrorInfo), "usageLimitExceeded"):
		return errors.New("Codex: лимит использования ChatGPT исчерпан")
	case strings.Contains(string(turnError.CodexErrorInfo), "unauthorized"):
		return errors.New("Codex: сессия ChatGPT OAuth недоступна; выполните вход повторно")
	case strings.Contains(string(turnError.CodexErrorInfo), "contextWindowExceeded"):
		return errors.New("Codex: контекст диалога превышает лимит модели")
	case strings.Contains(string(turnError.CodexErrorInfo), "serverOverloaded"):
		return errors.New("Codex: сервис временно перегружен; повторите запрос позже")
	default:
		// Do not expose arbitrary upstream text: it may contain prompt fragments
		// or provider diagnostics unsuitable for the conversation UI.
		return errors.New("Codex app server reported a turn error")
	}
}

func (stream *codexModelStream) Close() error {
	stream.once.Do(func() {
		stream.mu.Lock()
		stream.closed = true
		pending := stream.dynamicRequests
		stream.dynamicRequests = nil
		stream.mu.Unlock()
		for _, requestID := range pending {
			_ = stream.client.Respond(requestID, dynamicToolResultResponse(agent.ToolResult{
				Content: "tool stream closed before the result was available", IsError: true,
			}), nil)
		}
		if stream.release != nil {
			stream.release()
		}
	})
	return nil
}

// RespondToolResult completes one dynamic tool request sent by the Codex app
// server. The request id is opaque and never exposed to the runtime; callers
// use the normalized call id received in ModelEventToolCallDone instead.
func (stream *codexModelStream) RespondToolResult(ctx context.Context, callID string, result agent.ToolResult) error {
	if ctx == nil {
		return errors.New("Codex dynamic tool result: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stream.mu.Lock()
	if stream.closed {
		stream.mu.Unlock()
		return ErrClosed
	}
	requestID, ok := stream.dynamicRequests[callID]
	if ok {
		delete(stream.dynamicRequests, callID)
	}
	stream.mu.Unlock()
	if !ok {
		return fmt.Errorf("Codex dynamic tool result: unknown call %q", callID)
	}
	return stream.client.Respond(requestID, dynamicToolResultResponse(result), nil)
}

func (stream *codexModelStream) dynamicToolCall(event Event) (agent.ModelEvent, error) {
	var params struct {
		ThreadID  string          `json:"threadId"`
		TurnID    string          `json:"turnId"`
		CallID    string          `json:"callId"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(event.Params, &params); err != nil {
		return agent.ModelEvent{}, fmt.Errorf("decode Codex dynamic tool call: %w", err)
	}
	if !stream.matches(params.ThreadID, params.TurnID) {
		return agent.ModelEvent{}, nil
	}
	if strings.TrimSpace(params.CallID) == "" || strings.TrimSpace(params.Tool) == "" {
		return agent.ModelEvent{}, errors.New("Codex dynamic tool call is missing callId or tool")
	}
	arguments, err := normalizeDynamicArguments(params.Arguments)
	if err != nil {
		return agent.ModelEvent{}, err
	}
	if len(event.ID) == 0 || string(event.ID) == "null" {
		return agent.ModelEvent{}, errors.New("Codex dynamic tool call is missing request id")
	}
	stream.mu.Lock()
	if stream.dynamicRequests == nil {
		stream.dynamicRequests = make(map[string]json.RawMessage)
	}
	if _, exists := stream.dynamicRequests[params.CallID]; exists {
		stream.mu.Unlock()
		return agent.ModelEvent{}, fmt.Errorf("Codex dynamic tool call id %q was already received", params.CallID)
	}
	stream.dynamicRequests[params.CallID] = append(json.RawMessage(nil), event.ID...)
	stream.mu.Unlock()
	toolName := params.Tool
	if stream.dynamicToolNames != nil {
		mapped, exists := stream.dynamicToolNames[params.Tool]
		if !exists {
			return agent.ModelEvent{}, fmt.Errorf("Codex requested undeclared dynamic tool %q", params.Tool)
		}
		toolName = mapped
	}
	return agent.ModelEvent{
		Type: agent.ModelEventToolCallDone, ToolCallID: params.CallID,
		ToolName: toolName, Arguments: arguments,
	}, nil
}

func normalizeDynamicArguments(raw json.RawMessage) (string, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return "", errors.New("Codex dynamic tool call has invalid arguments")
	}
	if value[0] == '"' {
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return "", fmt.Errorf("decode Codex dynamic tool arguments: %w", err)
		}
		encoded = strings.TrimSpace(encoded)
		if encoded == "" || !json.Valid([]byte(encoded)) {
			return "", errors.New("Codex dynamic tool call has invalid arguments")
		}
		return encoded, nil
	}
	if !json.Valid(raw) {
		return "", errors.New("Codex dynamic tool call has invalid arguments")
	}
	return value, nil
}

func dynamicToolResultResponse(result agent.ToolResult) map[string]any {
	return map[string]any{
		"success": !result.IsError,
		"contentItems": []map[string]string{{
			"type": "inputText", "text": result.Content,
		}},
	}
}

func dynamicToolSpecs(tools []agent.ToolDescriptor) ([]DynamicToolSpec, map[string]string) {
	if len(tools) == 0 {
		return nil, nil
	}
	specs := make([]DynamicToolSpec, 0, len(tools))
	names := make(map[string]string, len(tools))
	for _, tool := range tools {
		providerName := codexDynamicToolName(tool.Name)
		if existing, collision := names[providerName]; collision && existing != tool.Name {
			// The hash suffix makes this practically unreachable, but keep the
			// provider contract deterministic and fail closed if it ever occurs.
			providerName = codexDynamicToolName(tool.Name + "_collision")
		}
		names[providerName] = tool.Name
		specs = append(specs, DynamicToolSpec{
			Type: "function", Name: providerName, Description: tool.Description,
			InputSchema: append(json.RawMessage(nil), tool.InputSchema...),
		})
	}
	return specs, names
}

func codexDynamicToolName(name string) string {
	if name != "" && len(name) <= 64 && isCodexDynamicToolName(name) {
		return name
	}
	var builder strings.Builder
	for _, character := range name {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '_', character == '-':
			builder.WriteRune(character)
		default:
			builder.WriteByte('_')
		}
	}
	base := strings.Trim(builder.String(), "_-")
	if base == "" {
		base = "yuri_tool"
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))[:10]
	const suffixLength = 11 // underscore plus ten hexadecimal characters
	if len(base) > 64-suffixLength {
		base = base[:64-suffixLength]
	}
	return base + "_" + digest
}

func isCodexDynamicToolName(name string) bool {
	for _, character := range name {
		if !((character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func (stream *codexModelStream) matches(threadID, turnID string) bool {
	return (threadID == "" || threadID == stream.threadID) &&
		(turnID == "" || strings.EqualFold(turnID, stream.turnID))
}

var _ agent.ModelBackend = (*Backend)(nil)
var _ agent.ModelStream = (*codexModelStream)(nil)
var _ agent.InteractiveToolStream = (*codexModelStream)(nil)
