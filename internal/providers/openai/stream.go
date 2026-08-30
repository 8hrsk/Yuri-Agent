package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
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
	nonStreaming    bool
	bodyConsumed    bool
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

// newJSONStream adapts a non-streamed application/json body — returned by
// gateways that ignore stream=true — to the same event contract the SSE path
// emits. The body is a single JSON document, so it must not be run through the
// SSE framer: that framer only accumulates "event:"/"data:" fields and would
// silently drop the whole payload.
func newJSONStream(response *http.Response, cancel context.CancelFunc, style APIStyle, maxBytes int64, secret string) agent.ModelStream {
	return &stream{
		response: response, cancel: cancel, style: style,
		maxBytes: maxBytes, maxLineBytes: defaultMaxLineBytes,
		secret: secret, nonStreaming: true,
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
	if s.nonStreaming {
		return s.recvJSON()
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

// parseFailure makes a frame-level failure stick. A parse error is not
// recoverable mid-body: leaving the reader open let a caller that kept polling
// receive the frames after the bad one spliced onto the frames before it, which
// is a silently incomplete answer dressed as a complete one. The error is
// returned unchanged so its kind still describes what actually went wrong.
func (s *stream) parseFailure(err error) (agent.ModelEvent, error) {
	_ = s.Close()
	return agent.ModelEvent{}, err
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
			return s.parseFailure(err)
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
			return s.parseFailure(err)
		}
		if len(events) == 0 {
			continue
		}
		s.enqueue(events)
		return s.popPending()
	}
}

// recvJSON drains the whole non-streamed body once, converts it into the
// normalized event sequence, and serves it from the pending queue. Every later
// call returns io.EOF because a JSON body carries exactly one response.
func (s *stream) recvJSON() (agent.ModelEvent, error) {
	s.mu.Lock()
	if s.bodyConsumed {
		s.mu.Unlock()
		return agent.ModelEvent{}, io.EOF
	}
	s.bodyConsumed = true
	s.mu.Unlock()

	body, err := s.readJSONBody()
	if err != nil {
		return agent.ModelEvent{}, err
	}
	var events []agent.ModelEvent
	if s.style == APIStyleChatCompletions {
		events, err = s.parseChatJSON(body)
	} else {
		events, err = s.parseResponsesBody(body)
	}
	if err != nil {
		return agent.ModelEvent{}, err
	}
	s.mu.Lock()
	s.completed = true
	s.pending = append(s.pending, events...)
	s.mu.Unlock()
	return s.popPending()
}

// readJSONBody reads the response body under the configured byte budget. It
// mirrors the SSE limit semantics so an oversized non-streamed body produces
// the same response_limit error kind instead of an unbounded allocation.
func (s *stream) readJSONBody() ([]byte, error) {
	if s.response == nil || s.response.Body == nil {
		return nil, providerError(ErrorKindStream, "json response", 0, "response body is missing", false, 0, s.secret)
	}
	limit := s.maxBytes
	if limit <= 0 {
		limit = defaultMaxResponseBytes
	}
	body, err := io.ReadAll(io.LimitReader(s.response.Body, limit+1))
	s.bytesRead += int64(len(body))
	if int64(len(body)) > limit {
		return nil, providerError(ErrorKindResponseLimit, "json response", 0, "response exceeds configured limit", false, 0, s.secret)
	}
	if err != nil {
		// A typed error raised by the body wrapper (idle timeout, first-byte
		// timeout) already carries the right kind; re-wrapping would flatten it.
		var providerErr *ProviderError
		if errors.As(err, &providerErr) {
			return nil, providerErr
		}
		return nil, providerError(ErrorKindStream, "json response", 0, err.Error(), false, 0, s.secret)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, providerError(ErrorKindDecode, "json response", 0, "provider returned an empty response body", false, 0, s.secret)
	}
	return body, nil
}

// parseResponsesBody converts a non-streamed Responses payload into the same
// ordered events the SSE path emits: started, text deltas, tool calls, and a
// single completion carrying usage.
func (s *stream) parseResponsesBody(body []byte) ([]agent.ModelEvent, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, providerError(ErrorKindDecode, "responses response", 0, err.Error(), false, 0, s.secret)
	}
	if raw, ok := value["response"]; ok && !hasRaw(value["output"]) && !hasRaw(value["output_text"]) {
		var inner map[string]json.RawMessage
		if json.Unmarshal(raw, &inner) == nil && len(inner) > 0 {
			value = inner
		}
	}
	responseID := responseObjectID(value)
	events, err := s.responsesBodyEvents(value, responseID, "responses response")
	if err != nil {
		return nil, err
	}
	return append([]agent.ModelEvent{{Type: agent.ModelEventStarted, ResponseID: responseID}}, events...), nil
}

// chatPayload decodes both Chat Completions shapes -- the streaming chunk and
// the non-streamed body -- leniently. Every leaf whose upstream JSON type is
// not guaranteed by the wire contract is held as a RawMessage and read through
// rawString or rawInt, so a gateway that serializes an id, a name, a finish
// reason or a token counter with a different scalar type cannot fail the whole
// payload the way a typed decode did.
//
// The enclosing shapes stay typed on purpose. That is where leniency stops: a
// choices value that is not an array, or a delta/message/function that is not
// an object, is not a payload we can read a different way, and inventing one
// would turn a loud decode error into a silently truncated answer.
type chatPayload struct {
	ID      json.RawMessage   `json:"id"`
	Error   json.RawMessage   `json:"error"`
	Choices []chatChoice      `json:"choices"`
	Usage   *chatUsagePayload `json:"usage"`
}

type chatChoice struct {
	Index json.RawMessage `json:"index"`
	// Delta carries the streaming form and Message the completed form; a
	// payload supplies one of them and the reader for that path picks it up.
	Delta        *chatMessageBody `json:"delta"`
	Message      *chatMessageBody `json:"message"`
	FinishReason json.RawMessage  `json:"finish_reason"`
}

type chatMessageBody struct {
	Content   json.RawMessage       `json:"content"`
	ToolCalls []chatToolCallPayload `json:"tool_calls"`
}

type chatToolCallPayload struct {
	Index    json.RawMessage `json:"index"`
	ID       json.RawMessage `json:"id"`
	Function struct {
		Name      json.RawMessage `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

func decodeChatPayload(data []byte, operation, secret string) (chatPayload, error) {
	var payload chatPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return chatPayload{}, providerError(ErrorKindDecode, operation, 0, err.Error(), false, 0, secret)
	}
	return payload, nil
}

// chatToolCallEvents normalizes the tool-call array shared by the streaming
// delta and the completed message. complete marks the non-streamed form, where
// every call arrives whole: the started event is emitted unconditionally and
// the index defaults to the array position. In the streaming form a call is
// assembled across chunks, so the started event waits for a name and the index
// defaults to the choice index that keyed the earlier chunks.
func (s *stream) chatToolCallEvents(calls []chatToolCallPayload, responseID string, defaultIndex int, complete bool) []agent.ModelEvent {
	var events []agent.ModelEvent
	for offset, call := range calls {
		index := defaultIndex
		if complete {
			index = offset
		}
		if value, ok := rawInt(call.Index); ok {
			index = int(value)
		}
		callID := rawString(call.ID)
		if callID == "" && !complete {
			callID = s.chatCallIDs[index]
		}
		if callID == "" {
			callID = ordinalID(index)
		}
		s.chatCallIDs[index] = callID
		name := rawString(call.Function.Name)
		if complete || name != "" {
			events = append(events, agent.ModelEvent{Type: agent.ModelEventToolCallStarted, ResponseID: responseID, ToolCallID: callID, ToolName: name})
		}
		// An object-valued arguments field -- emitted by gateways that inline
		// the decoded object instead of the JSON-encoded string the contract
		// asks for -- reaches the consumer as its raw JSON text. That is the
		// form internal/agent's toolCallBuilder wraps in a json.RawMessage and
		// internal/desktop's redactedToolArguments unmarshals, so re-encoding
		// preserves the tool call where rejecting it would lose a request that
		// is otherwise fully specified.
		if arguments := rawString(call.Function.Arguments); arguments != "" {
			events = append(events, agent.ModelEvent{Type: agent.ModelEventToolCallDelta, ResponseID: responseID, ToolCallID: callID, ArgumentsDelta: arguments})
		}
	}
	return events
}

// parseChatJSON converts a non-streamed Chat Completions payload. The delta
// shape of the streaming chunks is replaced by a single message object, so the
// message content and tool calls are replayed as the equivalent events.
func (s *stream) parseChatJSON(body []byte) ([]agent.ModelEvent, error) {
	payload, err := decodeChatPayload(body, "chat response", s.secret)
	if err != nil {
		return nil, err
	}
	if hasRaw(payload.Error) {
		return nil, providerError(ErrorKindStream, "chat response", 0, parseErrorBody(payload.Error, s.secret), false, 0, s.secret)
	}
	responseID := rawString(payload.ID)
	var events []agent.ModelEvent
	finishReason := ""
	for _, choice := range payload.Choices {
		if choice.Message != nil {
			if text := chatMessageText(choice.Message.Content); text != "" {
				events = append(events, agent.ModelEvent{Type: agent.ModelEventTextDelta, ResponseID: responseID, Delta: text})
			}
			events = append(events, s.chatToolCallEvents(choice.Message.ToolCalls, responseID, 0, true)...)
		}
		if finishReason == "" && hasRaw(choice.FinishReason) {
			finishReason = rawString(choice.FinishReason)
		}
	}
	// Usage is attached to a single completion event so a consumer that sums
	// per-event usage cannot double count a multi-choice response.
	return append(events, agent.ModelEvent{Type: agent.ModelEventCompleted, ResponseID: responseID, FinishReason: finishReason, Usage: chatUsage(payload.Usage)}), nil
}

// chatMessageText accepts the plain string content, the content-part array
// some OpenAI-compatible gateways return, and -- as a last resort -- any other
// non-null value rendered as its raw JSON text. Rendering beats dropping: a
// silently discarded delta is an answer that is wrong without saying so, and
// the Responses path already surfaces a non-string delta exactly this way.
func chatMessageText(raw json.RawMessage) string {
	if !hasRaw(raw) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return rawString(raw)
	}
	var builder strings.Builder
	for _, part := range parts {
		switch rawString(part["type"]) {
		case "text", "output_text", "":
			builder.WriteString(rawString(part["text"]))
		}
	}
	return builder.String()
}

func responseObjectID(value map[string]json.RawMessage) string {
	if id := rawString(value["response_id"]); id != "" {
		return id
	}
	if id := rawString(value["id"]); id != "" {
		return id
	}
	var response map[string]json.RawMessage
	if json.Unmarshal(value["response"], &response) == nil {
		return rawString(response["id"])
	}
	return ""
}

func hasRaw(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
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
		// A "type" the frame does supply must be a string. Swallowing this
		// error left the name empty, which routed the frame to the whole-body
		// fallback -- and that fallback appends a completion unconditionally.
		// A malformed frame therefore told the runtime the model had finished,
		// and the consumers that stop reading at a completion committed a
		// truncated answer as a whole one. Fail the stream instead: an aborted
		// stream is honest where a fabricated completion is not.
		if raw := value["type"]; hasRaw(raw) {
			if err := json.Unmarshal(raw, &typeName); err != nil {
				return nil, providerError(ErrorKindDecode, "responses stream", 0, "event type is not a string: "+err.Error(), false, 0, s.secret)
			}
		}
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
			// No event name and no "type" at all. Gateways that answer a
			// streaming request with SSE framing but a single frame holding
			// the entire non-streamed body land here, so a frame that really
			// does look like a Responses body is read as one. Anything else is
			// skipped rather than run through the whole-body fallback:
			// appending a completion to an unrecognized frame is exactly the
			// fabrication this guards against.
			if isResponsesBody(value) {
				if id == "" {
					id = responseObjectID(value)
				}
				return s.responsesBodyEvents(value, id, "responses stream")
			}
			return nil, nil
		}
		// A well-formed but unrecognized type is a forward-compatibility case,
		// not a corruption case: providers add event types over time, and
		// failing on a name this build has not heard of would break the client
		// against a newer API. Response content-part and reasoning lifecycle
		// events also land here and carry no user-visible output for this
		// contract. Ignore them safely -- ignoring emits nothing, so it cannot
		// claim the response finished.
		return nil, nil
	}
}

// isResponsesBody reports whether a frame with no event name and no "type"
// still identifies itself as a whole Responses payload. Only the fields that
// mark the body shape count: an output list, the flattened output text, the
// object discriminator, or an error envelope. Requiring positive evidence is
// what keeps an unrelated typeless frame -- a heartbeat, a keepalive, a shape
// from some other product -- out of a fallback that always completes.
func isResponsesBody(value map[string]json.RawMessage) bool {
	if hasRaw(value["output"]) || hasRaw(value["output_text"]) || hasRaw(value["error"]) {
		return true
	}
	return rawString(value["object"]) == "response"
}

// responsesBodyEvents converts a whole Responses payload into events, refusing
// first for the payloads that are not an answer. A body that carries an error
// envelope, or reports a failed or incomplete status, must surface that failure:
// running it through parseResponsesJSON would append a completion on top of a
// response the provider itself says did not finish.
func (s *stream) responsesBodyEvents(value map[string]json.RawMessage, responseID, operation string) ([]agent.ModelEvent, error) {
	if hasRaw(value["error"]) {
		return nil, providerError(ErrorKindStream, operation, 0, parseErrorBody(value["error"], s.secret), false, 0, s.secret)
	}
	if status := rawString(value["status"]); status == "failed" || status == "incomplete" {
		return nil, s.responseFailure("response."+status, value)
	}
	return s.parseResponsesJSON(value, responseID)
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
			name := rawString(item["name"])
			arguments := rawString(item["arguments"])
			events = append(events,
				agent.ModelEvent{Type: agent.ModelEventToolCallStarted, ResponseID: responseID, ToolCallID: callID, ToolName: name},
				agent.ModelEvent{Type: agent.ModelEventToolCallDone, ResponseID: responseID, ToolCallID: callID, ToolName: name, Arguments: arguments},
			)
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
	chunk, err := decodeChatPayload(data, "chat stream", s.secret)
	if err != nil {
		return nil, err
	}
	// A mid-stream error frame previously decoded to zero events and was
	// skipped, so an upstream failure ended the stream as a short answer with
	// no diagnosis. The non-streamed reader already fails closed here.
	if hasRaw(chunk.Error) {
		return nil, providerError(ErrorKindStream, "chat stream", 0, parseErrorBody(chunk.Error, s.secret), false, 0, s.secret)
	}
	responseID := rawString(chunk.ID)
	var events []agent.ModelEvent
	for _, choice := range chunk.Choices {
		// An unreadable or absent index keeps the zero the typed decode used,
		// so tool-call correlation across chunks is unchanged.
		choiceIndex := 0
		if value, ok := rawInt(choice.Index); ok {
			choiceIndex = int(value)
		}
		if choice.Delta != nil {
			if text := chatMessageText(choice.Delta.Content); text != "" {
				events = append(events, agent.ModelEvent{Type: agent.ModelEventTextDelta, ResponseID: responseID, Delta: text})
			}
			events = append(events, s.chatToolCallEvents(choice.Delta.ToolCalls, responseID, choiceIndex, false)...)
		}
		if hasRaw(choice.FinishReason) {
			events = append(events, agent.ModelEvent{Type: agent.ModelEventCompleted, ResponseID: responseID, FinishReason: rawString(choice.FinishReason), Usage: chatUsage(chunk.Usage)})
		}
	}
	if len(chunk.Choices) == 0 && chunk.Usage != nil {
		events = append(events, agent.ModelEvent{Type: agent.ModelEventCompleted, ResponseID: responseID, Usage: chatUsage(chunk.Usage)})
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

// rawInt reads an integer a gateway may have serialized as a JSON number, as a
// quoted number, or as a float ("2.0" out of a float encoder). It reports false
// when no integer can be read so the caller keeps its own default instead of
// silently substituting zero.
func rawInt(raw json.RawMessage) (int64, bool) {
	text := strings.TrimSpace(rawString(raw))
	if text == "" {
		return 0, false
	}
	if value, err := strconv.ParseInt(text, 10, 64); err == nil {
		return value, true
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || value != math.Trunc(value) || math.Abs(value) > math.MaxInt64 {
		return 0, false
	}
	return int64(value), true
}

// rawTokens reads a usage counter. An unreadable or negative count reports zero
// rather than an error: usage is metadata, and refusing a complete answer over
// a malformed counter would trade a whole response for a statistic.
func rawTokens(raw json.RawMessage) int64 {
	value, ok := rawInt(raw)
	if !ok || value < 0 {
		return 0
	}
	return value
}

// responseUsage reads usage from a Responses payload, accepting it either
// nested under "usage" or flat on the object. The counters go through the same
// lenient reader as the Chat path: the typed int64 decode this replaced
// returned an all-zero Usage for a float-encoded count, which silently
// disarmed the runtime token budget rather than reporting a smaller number.
func responseUsage(raw json.RawMessage) agent.Usage {
	var wrapper struct {
		Usage *chatUsagePayload `json:"usage"`
	}
	if json.Unmarshal(raw, &wrapper) == nil && wrapper.Usage != nil {
		return chatUsage(wrapper.Usage)
	}
	var flat chatUsagePayload
	if json.Unmarshal(raw, &flat) != nil {
		return agent.Usage{}
	}
	return chatUsage(&flat)
}

// chatUsagePayload is shared by the streaming chunk decoder and the
// non-streamed body decoder so both report usage identically.
type chatUsagePayload struct {
	PromptTokens     json.RawMessage `json:"prompt_tokens"`
	CompletionTokens json.RawMessage `json:"completion_tokens"`
	TotalTokens      json.RawMessage `json:"total_tokens"`
	InputTokens      json.RawMessage `json:"input_tokens"`
	OutputTokens     json.RawMessage `json:"output_tokens"`
}

func chatUsage(raw *chatUsagePayload) agent.Usage {
	if raw == nil {
		return agent.Usage{}
	}
	return normalizeUsage(rawTokens(raw.InputTokens), rawTokens(raw.OutputTokens), rawTokens(raw.TotalTokens), rawTokens(raw.PromptTokens), rawTokens(raw.CompletionTokens))
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
