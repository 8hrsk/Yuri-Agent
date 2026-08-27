package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

type stream struct {
	response        *http.Response
	cancel          context.CancelFunc
	style           APIStyle
	maxBytes        int64
	maxEvents       int
	maxLineBytes    int
	secret          string
	reader          *bufio.Reader
	bytesRead       int64
	eventsRead      int
	closed          bool
	completed       bool
	mu              sync.Mutex
	pending         []agent.ModelEvent
	responseCallIDs map[string]string
	chatCallIDs     map[int]string
}

func newSSEStream(response *http.Response, cancel context.CancelFunc, style APIStyle, maxBytes int64, maxEvents, maxLineBytes int, secret string) agent.ModelStream {
	return &stream{
		response: response, cancel: cancel, style: style,
		maxBytes: maxBytes, maxEvents: maxEvents, maxLineBytes: maxLineBytes,
		secret: secret, reader: bufio.NewReader(response.Body),
		responseCallIDs: make(map[string]string), chatCallIDs: make(map[int]string),
	}
}

func newJSONStream(response *http.Response, cancel context.CancelFunc, style APIStyle, maxBytes int64, secret string) agent.ModelStream {
	return &stream{
		response: response, cancel: cancel, style: style,
		maxBytes: maxBytes, maxEvents: 100, maxLineBytes: defaultMaxLineBytes,
		secret: secret, reader: bufio.NewReader(response.Body),
		responseCallIDs: make(map[string]string), chatCallIDs: make(map[int]string),
	}
}

func (s *stream) Recv(ctx context.Context) (agent.ModelEvent, error) {
	if ctx == nil {
		return agent.ModelEvent{}, context.Canceled
	}
	if err := contextError(ctx); err != nil {
		_ = s.Close()
		return agent.ModelEvent{}, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return agent.ModelEvent{}, io.EOF
	}
	if len(s.pending) > 0 {
		event := s.pending[0]
		s.pending = s.pending[1:]
		s.eventsRead++
		s.mu.Unlock()
		return event, nil
	}
	s.mu.Unlock()

	if s.response == nil {
		return agent.ModelEvent{}, io.EOF
	}
	if s.maxEvents > 0 && s.eventsRead >= s.maxEvents {
		return agent.ModelEvent{}, providerError(ErrorKindStream, "stream", 0, "stream event limit exceeded", false, 0, s.secret)
	}
	if s.style == APIStyleChatCompletions {
		return s.recvChat(ctx)
	}
	return s.recvResponses(ctx)
}

func (s *stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	if s.response != nil && s.response.Body != nil {
		return s.response.Body.Close()
	}
	return nil
}

func (s *stream) recvResponses(ctx context.Context) (agent.ModelEvent, error) {
	for {
		name, data, err := s.nextFrame(ctx)
		if err != nil {
			return agent.ModelEvent{}, err
		}
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return s.finish()
		}
		events, err := s.parseResponses(name, data)
		if err != nil {
			return agent.ModelEvent{}, err
		}
		if len(events) == 0 {
			continue
		}
		s.enqueue(events)
		return s.popPending()
	}
}

func (s *stream) recvChat(ctx context.Context) (agent.ModelEvent, error) {
	for {
		_, data, err := s.nextFrame(ctx)
		if err != nil {
			return agent.ModelEvent{}, err
		}
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return s.finish()
		}
		events, err := s.parseChat(data)
		if err != nil {
			return agent.ModelEvent{}, err
		}
		if len(events) == 0 {
			continue
		}
		s.enqueue(events)
		return s.popPending()
	}
}

func (s *stream) nextFrame(ctx context.Context) (string, []byte, error) {
	var eventName string
	var data bytes.Buffer
	for {
		if err := contextError(ctx); err != nil {
			_ = s.Close()
			return "", nil, err
		}
		line, err := s.readLine()
		if err != nil && !errors.Is(err, io.EOF) {
			var providerErr *ProviderError
			if errors.As(err, &providerErr) {
				return "", nil, err
			}
			return "", nil, providerError(ErrorKindStream, "stream", 0, err.Error(), false, 0, s.secret)
		}
		line = bytes.TrimSuffix(line, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			if data.Len() == 0 {
				if errors.Is(err, io.EOF) {
					return "", nil, io.EOF
				}
				continue
			}
			return eventName, data.Bytes(), nil
		}
		if line[0] == ':' {
			continue
		}
		field, value, hasValue := bytes.Cut(line, []byte(":"))
		if !hasValue {
			field, value = line, nil
		}
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		switch string(field) {
		case "event":
			eventName = string(value)
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(value)
		}
		if errors.Is(err, io.EOF) {
			return eventName, data.Bytes(), nil
		}
	}
}

func (s *stream) readLine() ([]byte, error) {
	line, err := s.reader.ReadBytes('\n')
	s.bytesRead += int64(len(line))
	if s.maxBytes > 0 && s.bytesRead > s.maxBytes {
		return nil, providerError(ErrorKindResponseLimit, "stream", 0, "stream response exceeds configured limit", false, 0, s.secret)
	}
	if s.maxLineBytes > 0 && len(line) > s.maxLineBytes {
		return nil, providerError(ErrorKindResponseLimit, "stream", 0, "stream line exceeds configured limit", false, 0, s.secret)
	}
	return line, err
}

func (s *stream) enqueue(events []agent.ModelEvent) {
	s.mu.Lock()
	s.pending = append(s.pending, events...)
	s.mu.Unlock()
}

func (s *stream) popPending() (agent.ModelEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return agent.ModelEvent{}, io.EOF
	}
	event := s.pending[0]
	s.pending = s.pending[1:]
	s.eventsRead++
	return event, nil
}

func (s *stream) finish() (agent.ModelEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completed {
		return agent.ModelEvent{}, io.EOF
	}
	s.completed = true
	s.eventsRead++
	return agent.ModelEvent{Type: agent.ModelEventCompleted}, nil
}

func (s *stream) parseResponses(eventName string, data []byte) ([]agent.ModelEvent, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, providerError(ErrorKindDecode, "responses stream", 0, err.Error(), false, 0, s.secret)
	}
	typeName := eventName
	if typeName == "" {
		_ = json.Unmarshal(value["type"], &typeName)
	}
	id := rawString(value["response_id"])
	if id == "" {
		var response map[string]json.RawMessage
		if json.Unmarshal(value["response"], &response) == nil {
			id = rawString(response["id"])
		}
	}
	switch typeName {
	case "response.created", "response.in_progress":
		return []agent.ModelEvent{{Type: agent.ModelEventStarted, ResponseID: id}}, nil
	case "response.output_text.delta":
		return []agent.ModelEvent{{Type: agent.ModelEventTextDelta, ResponseID: id, Delta: rawString(value["delta"])}}, nil
	case "response.output_item.added":
		return s.responsesOutputItem(value["item"], id, false)
	case "response.output_item.done":
		return s.responsesOutputItem(value["item"], id, true)
	case "response.function_call_arguments.delta":
		itemID := rawString(value["item_id"])
		callID := s.responseCallIDs[itemID]
		if callID == "" {
			callID = itemID
		}
		return []agent.ModelEvent{{Type: agent.ModelEventToolCallDelta, ResponseID: id, ToolCallID: callID, ArgumentsDelta: rawString(value["delta"])}}, nil
	case "response.function_call_arguments.done":
		itemID := rawString(value["item_id"])
		callID := s.responseCallIDs[itemID]
		if callID == "" {
			callID = itemID
		}
		return []agent.ModelEvent{{Type: agent.ModelEventToolCallDone, ResponseID: id, ToolCallID: callID, Arguments: rawString(value["arguments"])}}, nil
	case "response.completed":
		usage := responseUsage(value["response"])
		return []agent.ModelEvent{{Type: agent.ModelEventCompleted, ResponseID: id, Usage: usage}}, nil
	case "response.failed", "response.incomplete", "error":
		return nil, s.responseFailure(typeName, value)
	default:
		if typeName == "" {
			return s.parseResponsesJSON(value, id)
		}
		// Response content-part and reasoning lifecycle events do not carry
		// user-visible output for this contract. Ignore them safely.
		return nil, nil
	}
}

// parseResponsesJSON adapts a non-streaming Responses payload returned by a
// gateway that ignored stream=true. Keeping this fallback on the provider
// boundary lets the runtime consume one stable stream contract.
func (s *stream) parseResponsesJSON(value map[string]json.RawMessage, responseID string) ([]agent.ModelEvent, error) {
	var events []agent.ModelEvent
	textOutput := rawString(value["output_text"])
	if textOutput != "" {
		events = append(events, agent.ModelEvent{Type: agent.ModelEventTextDelta, ResponseID: responseID, Delta: textOutput})
	}
	var output []json.RawMessage
	if json.Unmarshal(value["output"], &output) == nil {
		for _, raw := range output {
			var item map[string]json.RawMessage
			if json.Unmarshal(raw, &item) != nil {
				continue
			}
			if rawString(item["type"]) != "function_call" {
				var content []json.RawMessage
				if textOutput == "" && json.Unmarshal(item["content"], &content) == nil {
					for _, part := range content {
						var contentItem map[string]json.RawMessage
						if json.Unmarshal(part, &contentItem) == nil && rawString(contentItem["type"]) == "output_text" {
							if text := rawString(contentItem["text"]); text != "" {
								events = append(events, agent.ModelEvent{Type: agent.ModelEventTextDelta, ResponseID: responseID, Delta: text})
							}
						}
					}
				}
				continue
			}
			callID := rawString(item["call_id"])
			if callID == "" {
				callID = rawString(item["id"])
			}
			events = append(events, agent.ModelEvent{Type: agent.ModelEventToolCallDone, ResponseID: responseID, ToolCallID: callID, ToolName: rawString(item["name"]), Arguments: rawString(item["arguments"])})
		}
	}
	usage := responseUsage(value["usage"])
	events = append(events, agent.ModelEvent{Type: agent.ModelEventCompleted, ResponseID: responseID, Usage: usage})
	return events, nil
}

func (s *stream) responsesOutputItem(raw json.RawMessage, responseID string, done bool) ([]agent.ModelEvent, error) {
	var item map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &item) != nil {
		return nil, nil
	}
	if rawString(item["type"]) != "function_call" {
		return nil, nil
	}
	itemID := rawString(item["id"])
	callID := rawString(item["call_id"])
	if callID == "" {
		callID = itemID
	}
	if itemID != "" {
		s.responseCallIDs[itemID] = callID
	}
	name := rawString(item["name"])
	arguments := rawString(item["arguments"])
	if done {
		return []agent.ModelEvent{{Type: agent.ModelEventToolCallDone, ResponseID: responseID, ToolCallID: callID, ToolName: name, Arguments: arguments}}, nil
	}
	return []agent.ModelEvent{{Type: agent.ModelEventToolCallStarted, ResponseID: responseID, ToolCallID: callID, ToolName: name, Arguments: arguments}}, nil
}

func (s *stream) responseFailure(typeName string, value map[string]json.RawMessage) error {
	message := rawString(value["message"])
	if message == "" {
		if errorObject, ok := value["error"]; ok {
			message = parseErrorBody(errorObject, s.secret)
		}
	}
	if message == "" {
		if response, ok := value["response"]; ok {
			var object map[string]json.RawMessage
			if json.Unmarshal(response, &object) == nil {
				message = rawString(object["error"])
			}
		}
	}
	return providerError(ErrorKindStream, "responses stream "+typeName, 0, message, false, 0, s.secret)
}

func (s *stream) parseChat(data []byte) ([]agent.ModelEvent, error) {
	var chunk struct {
		ID      string `json:"id"`
		Choices []struct {
			Index int `json:"index"`
			Delta struct {
				Content   *string `json:"content"`
				ToolCalls []struct {
					Index    *int   `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
			InputTokens      int64 `json:"input_tokens"`
			OutputTokens     int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return nil, providerError(ErrorKindDecode, "chat stream", 0, err.Error(), false, 0, s.secret)
	}
	var events []agent.ModelEvent
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			events = append(events, agent.ModelEvent{Type: agent.ModelEventTextDelta, ResponseID: chunk.ID, Delta: *choice.Delta.Content})
		}
		for _, call := range choice.Delta.ToolCalls {
			index := choice.Index
			if call.Index != nil {
				index = *call.Index
			}
			callID := call.ID
			if callID == "" {
				callID = s.chatCallIDs[index]
			}
			if callID == "" {
				callID = ordinalID(index)
			}
			s.chatCallIDs[index] = callID
			if call.Function.Name != "" {
				events = append(events, agent.ModelEvent{Type: agent.ModelEventToolCallStarted, ResponseID: chunk.ID, ToolCallID: callID, ToolName: call.Function.Name})
			}
			if call.Function.Arguments != "" {
				events = append(events, agent.ModelEvent{Type: agent.ModelEventToolCallDelta, ResponseID: chunk.ID, ToolCallID: callID, ArgumentsDelta: call.Function.Arguments})
			}
		}
		if choice.FinishReason != nil {
			usage := chatUsage(chunk.Usage)
			events = append(events, agent.ModelEvent{Type: agent.ModelEventCompleted, ResponseID: chunk.ID, FinishReason: *choice.FinishReason, Usage: usage})
		}
	}
	if len(chunk.Choices) == 0 && chunk.Usage != nil {
		events = append(events, agent.ModelEvent{Type: agent.ModelEventCompleted, ResponseID: chunk.ID, Usage: chatUsage(chunk.Usage)})
	}
	return events, nil
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return strings.Trim(string(raw), `"`)
}

func responseUsage(raw json.RawMessage) agent.Usage {
	var wrapper struct {
		Usage *struct {
			InputTokens      int64 `json:"input_tokens"`
			OutputTokens     int64 `json:"output_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
		InputTokens      int64 `json:"input_tokens"`
		OutputTokens     int64 `json:"output_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return agent.Usage{}
	}
	if wrapper.Usage != nil {
		return normalizeUsage(wrapper.Usage.InputTokens, wrapper.Usage.OutputTokens, wrapper.Usage.TotalTokens, wrapper.Usage.PromptTokens, wrapper.Usage.CompletionTokens)
	}
	return normalizeUsage(wrapper.InputTokens, wrapper.OutputTokens, wrapper.TotalTokens, wrapper.PromptTokens, wrapper.CompletionTokens)
}

func chatUsage(raw *struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
}) agent.Usage {
	if raw == nil {
		return agent.Usage{}
	}
	return normalizeUsage(raw.InputTokens, raw.OutputTokens, raw.TotalTokens, raw.PromptTokens, raw.CompletionTokens)
}

func normalizeUsage(input, output, total, prompt, completion int64) agent.Usage {
	if input == 0 {
		input = prompt
	}
	if output == 0 {
		output = completion
	}
	if total == 0 {
		total = input + output
	}
	return agent.Usage{InputTokens: input, OutputTokens: output, TotalTokens: total}
}
