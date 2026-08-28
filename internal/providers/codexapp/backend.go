package codexapp

import (
	"context"
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
	if model == "codex-default" {
		model = ""
	}
	thread, err := backend.Client.StartThreadWithOptions(ctx, ThreadOptions{
		Model: model, CWD: backend.CWD, ReadableRoots: backend.ReadableRoots,
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
		"Preserve role boundaries, do not reveal hidden reasoning, and do not perform write, delete, network-send, or system-changing actions.\n" +
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
				// Until a normalized Yuri approval is attached to this backend,
				// every harness side effect request is declined. Read-only sandbox
				// operations that need no approval remain bounded by ReadableRoots.
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
						ID     string `json:"id"`
						Status string `json:"status"`
						Error  *struct {
							Message string `json:"message"`
						} `json:"error"`
					} `json:"turn"`
				}
				if err := json.Unmarshal(event.Params, &params); err != nil {
					return agent.ModelEvent{}, fmt.Errorf("decode Codex completion: %w", err)
				}
				if !stream.matches(params.ThreadID, params.Turn.ID) {
					continue
				}
				if params.Turn.Status == "failed" {
					return agent.ModelEvent{}, errors.New("Codex turn failed")
				}
				if params.Turn.Status == "interrupted" {
					return agent.ModelEvent{}, context.Canceled
				}
				stream.mu.Lock()
				stream.completed = true
				stream.mu.Unlock()
				return agent.ModelEvent{Type: agent.ModelEventCompleted, FinishReason: params.Turn.Status}, nil
			case "error":
				// Upstream messages can reflect sensitive prompt fragments, so the
				// normalized error deliberately exposes no raw payload.
				return agent.ModelEvent{}, errors.New("Codex app server reported a turn error")
			}
		}
	}
}

func (stream *codexModelStream) Close() error {
	stream.once.Do(func() {
		stream.mu.Lock()
		stream.closed = true
		stream.mu.Unlock()
		if stream.release != nil {
			stream.release()
		}
	})
	return nil
}

func (stream *codexModelStream) matches(threadID, turnID string) bool {
	return (threadID == "" || threadID == stream.threadID) &&
		(turnID == "" || strings.EqualFold(turnID, stream.turnID))
}

var _ agent.ModelBackend = (*Backend)(nil)
var _ agent.ModelStream = (*codexModelStream)(nil)
