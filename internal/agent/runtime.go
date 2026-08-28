package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

const (
	defaultMaxSteps           = 8
	defaultMaxTokens          = int64(32_000)
	defaultMaxToolCalls       = 32
	defaultMaxToolOutputBytes = int64(256 * 1024)
	defaultMaxDuration        = 10 * time.Minute
)

// Runtime coordinates model output and local tools. It is deliberately
// independent of Wails, SQLite, and any provider implementation.
type Runtime struct {
	Backend      ModelBackend
	Tools        *ToolRegistry
	Authorizer   ToolAuthorizer
	Approvals    ApprovalHandler
	DefaultModel string
	Now          func() time.Time
}

func NewRuntime(backend ModelBackend, tools *ToolRegistry) (*Runtime, error) {
	if backend == nil {
		return nil, fmt.Errorf("%w: model backend is required", ErrInvalidRequest)
	}
	if tools == nil {
		tools = NewToolRegistry()
	}
	return &Runtime{Backend: backend, Tools: tools, Now: time.Now}, nil
}

// Run executes one model/tool loop. A model may return text and several tool
// calls in one turn; all calls are normalized, authorized, and executed before
// the next model request. The returned result contains only the final
// assistant message; intermediate model text is also exposed through Sink.
func (r *Runtime) Run(ctx context.Context, input RunRequest) (RunResult, error) {
	if r == nil || r.Backend == nil {
		return RunResult{}, fmt.Errorf("%w: runtime backend is required", ErrInvalidRequest)
	}
	if ctx == nil {
		return RunResult{}, fmt.Errorf("%w: nil context", ErrInvalidRequest)
	}
	if strings.TrimSpace(input.ModelRequest.Model) == "" && strings.TrimSpace(r.DefaultModel) != "" {
		input.ModelRequest.Model = r.DefaultModel
	}
	if err := input.ModelRequest.Valid(); err != nil {
		return RunResult{}, err
	}
	budget := normalizedBudget(input.Budget)
	if input.ModelRequest.MaxOutputTokens == 0 || input.ModelRequest.MaxOutputTokens > budget.MaxTokens {
		input.ModelRequest.MaxOutputTokens = budget.MaxTokens
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if budget.MaxDurationSeconds > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(budget.MaxDurationSeconds)*time.Second)
	} else {
		runCtx, cancel = context.WithTimeout(ctx, defaultMaxDuration)
	}
	defer cancel()

	if err := emit(runCtx, input.Sink, Event{Type: EventRunStarted, RunID: input.RunID}); err != nil {
		return RunResult{}, err
	}

	messages := append([]Message(nil), input.ModelRequest.Messages...)
	result := RunResult{}
	seenCalls := make(map[string]ToolResult)
	seenCallArgs := make(map[string]string)
	toolCallsUsed := 0

	for step := 1; step <= budget.MaxSteps; step++ {
		if err := contextErr(runCtx); err != nil {
			return r.fail(runCtx, input, result, err)
		}
		request := input.ModelRequest
		request.Messages = append([]Message(nil), messages...)
		request.Tools = r.Tools.Descriptors()
		stream, err := r.Backend.Start(runCtx, request)
		if err != nil {
			return r.fail(runCtx, input, result, fmt.Errorf("%w: %v", ErrBackend, redactRuntimeError(err)))
		}
		turnText, calls, usage, err := r.consumeStream(runCtx, input, stream, step, budget.MaxTokens)
		closeErr := stream.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
		if err != nil {
			return r.fail(runCtx, input, result, err)
		}
		result.Steps = step
		result.Usage = result.Usage.Add(usage)
		if budget.MaxTokens > 0 && result.Usage.TotalTokens > budget.MaxTokens {
			return r.fail(runCtx, input, result, fmt.Errorf("%w: token limit %d exceeded", ErrBudgetExceeded, budget.MaxTokens))
		}

		assistant := Message{Role: RoleAssistant, Content: turnText, ToolCalls: append([]ToolCall(nil), calls...)}
		if !assistant.Valid() {
			return r.fail(runCtx, input, result, fmt.Errorf("%w: backend returned no assistant output", ErrBackend))
		}
		messages = append(messages, assistant)
		if len(calls) == 0 {
			result.Message = assistant
			if err := emit(runCtx, input.Sink, Event{Type: EventRunCompleted, RunID: input.RunID, Step: step, Text: turnText, Usage: result.Usage}); err != nil {
				return RunResult{}, err
			}
			return result, nil
		}
		if toolCallsUsed+len(calls) > budget.MaxToolCalls {
			return r.fail(runCtx, input, result, fmt.Errorf("%w: tool call limit %d exceeded", ErrBudgetExceeded, budget.MaxToolCalls))
		}

		for _, call := range calls {
			toolCallsUsed++
			result.ToolCalls = append(result.ToolCalls, call)
			toolMessage, err := r.executeTool(runCtx, input, call, step, budget.MaxToolOutputBytes, seenCalls, seenCallArgs)
			if err != nil {
				// A tool failure is model-visible data. The run remains alive so
				// the model can recover or explain the failure, but cancellation,
				// approval denial, and budget violations terminate immediately.
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
					errors.Is(err, ErrBudgetExceeded) || errors.Is(err, ErrApprovalRequired) {
					return r.fail(runCtx, input, result, err)
				}
				toolMessage = ToolResult{Content: redactRuntimeError(err), IsError: true}
			}
			messages = append(messages, Message{Role: RoleTool, ToolCallID: call.ID, Content: toolMessage.Content})
		}
	}

	return r.fail(runCtx, input, result, fmt.Errorf("%w: maximum steps %d reached", ErrBudgetExceeded, budget.MaxSteps))
}

func normalizedBudget(b domain.RunBudget) domain.RunBudget {
	if b.MaxSteps <= 0 {
		b.MaxSteps = defaultMaxSteps
	}
	if b.MaxTokens <= 0 {
		b.MaxTokens = defaultMaxTokens
	}
	if b.MaxToolCalls <= 0 {
		b.MaxToolCalls = defaultMaxToolCalls
	}
	if b.MaxToolOutputBytes <= 0 {
		b.MaxToolOutputBytes = defaultMaxToolOutputBytes
	}
	if b.MaxDurationSeconds <= 0 {
		b.MaxDurationSeconds = int(defaultMaxDuration / time.Second)
	}
	return b
}

func (r *Runtime) consumeStream(ctx context.Context, input RunRequest, stream ModelStream, step int, maxTokens int64) (string, []ToolCall, Usage, error) {
	if stream == nil {
		return "", nil, Usage{}, fmt.Errorf("%w: backend returned nil stream", ErrBackend)
	}
	var textBuilder strings.Builder
	callBuilders := make(map[string]*toolCallBuilder)
	var order []string
	var usage Usage
	for {
		event, err := stream.Recv(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", nil, usage, err
			}
			if errors.Is(err, io.EOF) {
				break
			}
			return "", nil, usage, fmt.Errorf("%w: %v", ErrBackend, redactRuntimeError(err))
		}
		usage = usage.Add(event.Usage)
		if maxTokens > 0 && usage.TotalTokens > maxTokens {
			return "", nil, usage, fmt.Errorf("%w: token limit %d exceeded", ErrBudgetExceeded, maxTokens)
		}
		switch event.Type {
		case ModelEventStarted:
		case ModelEventTextDelta:
			textBuilder.WriteString(event.Delta)
			if err := emit(ctx, input.Sink, Event{Type: EventModelTextDelta, RunID: input.RunID, Step: step, Text: event.Delta, Usage: event.Usage}); err != nil {
				return "", nil, usage, err
			}
		case ModelEventToolCallStarted:
			id := normalizeCallID(event.ToolCallID, len(order)+1)
			builder := callBuilders[id]
			if builder == nil {
				builder = &toolCallBuilder{id: id}
				callBuilders[id] = builder
				order = append(order, id)
			}
			if event.ToolName != "" {
				builder.name = event.ToolName
			}
			if event.Arguments != "" {
				builder.arguments = event.Arguments
			}
			call := builder.call()
			if err := emit(ctx, input.Sink, Event{Type: EventToolCallStarted, RunID: input.RunID, Step: step, ToolCall: &call}); err != nil {
				return "", nil, usage, err
			}
		case ModelEventToolCallDelta:
			id := normalizeCallID(event.ToolCallID, len(order)+1)
			builder := callBuilders[id]
			if builder == nil {
				builder = &toolCallBuilder{id: id}
				callBuilders[id] = builder
				order = append(order, id)
			}
			if event.ToolName != "" {
				builder.name = event.ToolName
			}
			builder.arguments += event.ArgumentsDelta
		case ModelEventToolCallDone:
			wasKnown := false
			id := normalizeCallID(event.ToolCallID, len(order)+1)
			builder := callBuilders[id]
			if builder == nil {
				builder = &toolCallBuilder{id: id}
				callBuilders[id] = builder
				order = append(order, id)
			} else {
				wasKnown = true
			}
			if event.ToolName != "" {
				builder.name = event.ToolName
			}
			if event.Arguments != "" {
				builder.arguments = event.Arguments
			}
			call := builder.call()
			if !wasKnown {
				if err := emit(ctx, input.Sink, Event{Type: EventToolCallStarted, RunID: input.RunID, Step: step, ToolCall: &call}); err != nil {
					return "", nil, usage, err
				}
			}
		case ModelEventCompleted:
			// Completion is a marker; tool calls are flushed below so a
			// provider is free to omit a separate done event.
		default:
			return "", nil, usage, fmt.Errorf("%w: unknown stream event %q", ErrBackend, event.Type)
		}
	}

	calls := make([]ToolCall, 0, len(order))
	for _, id := range order {
		call := callBuilders[id].call()
		if call.Name == "" {
			return "", nil, usage, fmt.Errorf("%w: tool call %q has no name", ErrBackend, id)
		}
		if call.Arguments == nil {
			call.Arguments = json.RawMessage("{}")
		}
		calls = append(calls, call)
	}
	return textBuilder.String(), calls, usage, nil
}

type toolCallBuilder struct {
	id        string
	name      string
	arguments string
}

func (b *toolCallBuilder) call() ToolCall {
	arguments := strings.TrimSpace(b.arguments)
	if arguments == "" {
		arguments = "{}"
	}
	return ToolCall{ID: b.id, Name: b.name, Arguments: json.RawMessage(arguments)}
}

func normalizeCallID(id string, ordinal int) string {
	if strings.TrimSpace(id) != "" {
		return id
	}
	return fmt.Sprintf("call_%d", ordinal)
}

func (r *Runtime) executeTool(ctx context.Context, input RunRequest, call ToolCall, step int, maxBytes int64, seen map[string]ToolResult, seenArgs map[string]string) (ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return ToolResult{}, err
	}
	if !call.Valid() {
		return ToolResult{}, fmt.Errorf("%w: incomplete tool call", ErrToolArguments)
	}
	if !json.Valid(call.Arguments) || !jsonObject(call.Arguments) {
		return ToolResult{}, fmt.Errorf("%w: tool %q arguments must be a JSON object", ErrToolArguments, call.Name)
	}
	tool, ok := r.Tools.Get(call.Name)
	if !ok {
		return ToolResult{}, fmt.Errorf("%w: %s", ErrToolNotFound, call.Name)
	}
	descriptor := tool.Descriptor()
	if !descriptor.Valid() {
		return ToolResult{}, fmt.Errorf("%w: registered tool %q has invalid descriptor", ErrInvalidRequest, call.Name)
	}
	argsHash := hashArgs(call.Arguments)
	key := call.ID
	if key == "" {
		key = call.Name + ":" + argsHash
	}
	if previous, exists := seen[key]; exists {
		if seenArgs[key] != argsHash {
			return ToolResult{}, fmt.Errorf("%w: idempotency key reused with different arguments", ErrInvalidRequest)
		}
		return previous, nil
	}

	authorization := ToolAuthorizationResult{Decision: domain.PermissionAllow}
	if r.Authorizer != nil {
		var err error
		authorization, err = r.Authorizer.Authorize(ctx, ToolAuthorizationRequest{
			RunID: input.RunID, Tool: descriptor, Call: call,
			Action: fmt.Sprintf("execute tool %s", descriptor.Name),
		})
		if err != nil {
			return ToolResult{}, err
		}
	}
	switch authorization.Decision {
	case domain.PermissionDeny:
		return ToolResult{Content: authorization.Reason, IsError: true}, nil
	case domain.PermissionNeedsApproval:
		if err := emit(ctx, input.Sink, Event{Type: EventToolApprovalNeeded, RunID: input.RunID, Step: step, ToolCall: &call, Error: authorization.Reason}); err != nil {
			return ToolResult{}, err
		}
		if r.Approvals == nil {
			return ToolResult{}, fmt.Errorf("%w: %s", ErrApprovalRequired, authorization.Reason)
		}
		approved, err := r.Approvals.Approve(ctx, ApprovalRequest{RunID: input.RunID, Tool: descriptor, Call: call, Action: fmt.Sprintf("execute tool %s", descriptor.Name), Reason: authorization.Reason})
		if err != nil {
			return ToolResult{}, err
		}
		if !approved {
			return ToolResult{Content: "tool execution denied by user", IsError: true}, nil
		}
	case domain.PermissionAllow:
	default:
		return ToolResult{}, fmt.Errorf("%w: unknown authorization decision %q", ErrInvalidRequest, authorization.Decision)
	}

	if err := emit(ctx, input.Sink, Event{Type: EventToolStarted, RunID: input.RunID, Step: step, ToolCall: &call}); err != nil {
		return ToolResult{}, err
	}
	toolCtx := ctx
	result, err := tool.Execute(toolCtx, call)
	if err != nil {
		return ToolResult{}, err
	}
	if maxBytes > 0 && int64(len(result.Content)) > maxBytes {
		result.Content = truncateUTF8(result.Content, int(maxBytes))
		result.IsError = true
		result.Metadata = cloneMetadata(result.Metadata)
		result.Metadata["truncated"] = true
	}
	seen[key] = result
	seenArgs[key] = argsHash
	resultCopy := result
	if err := emit(ctx, input.Sink, Event{Type: EventToolCompleted, RunID: input.RunID, Step: step, ToolCall: &call, ToolResult: &resultCopy}); err != nil {
		return ToolResult{}, err
	}
	return result, nil
}

func jsonObject(raw json.RawMessage) bool {
	var object map[string]any
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func hashArgs(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) == nil {
		if canonical, err := json.Marshal(value); err == nil {
			raw = canonical
		}
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func truncateUTF8(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	const marker = "\n[… tool output truncated …]"
	if max <= len(marker) {
		return value[:max]
	}
	limit := max - len(marker)
	for limit > 0 && limit < len(value) && (value[limit]&0xc0) == 0x80 {
		limit--
	}
	return value[:limit] + marker
}

func cloneMetadata(value map[string]any) map[string]any {
	if value == nil {
		return make(map[string]any)
	}
	copyValue := make(map[string]any, len(value)+1)
	for key, item := range value {
		copyValue[key] = item
	}
	return copyValue
}

func emit(ctx context.Context, sink EventSink, event Event) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if sink == nil {
		return nil
	}
	return sink(ctx, event)
}

func contextErr(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (r *Runtime) fail(ctx context.Context, input RunRequest, result RunResult, err error) (RunResult, error) {
	if err == nil {
		err = ErrBackend
	}
	message := redactRuntimeError(err)
	if emitErr := emit(ctx, input.Sink, Event{Type: EventRunFailed, RunID: input.RunID, Step: result.Steps, Error: message, Usage: result.Usage}); emitErr != nil {
		return result, emitErr
	}
	return result, err
}

func redactRuntimeError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	for _, marker := range []string{"sk-", "api_key", "apikey", "authorization", "bearer", "token", "secret"} {
		if strings.Contains(strings.ToLower(message), marker) {
			return "provider or tool operation failed"
		}
	}
	if len(message) > 512 {
		message = message[:512] + "…"
	}
	return message
}
