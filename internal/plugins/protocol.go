package plugins

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// ProtocolVersion is the public protocol version in plugin.json and in the
	// handshake payload. ProtocolName is the envelope-level discriminator.
	ProtocolVersion = "1.0"
	ProtocolName    = "yuri.plugin.v1"
)

type MessageType string

const (
	MessageHandshake       MessageType = "handshake"
	MessageHandshakeResult MessageType = "handshake_result"
	MessageHealth          MessageType = "health"
	MessageHealthResult    MessageType = "health_result"
	MessageToolInvoke      MessageType = "tool_invoke"
	MessageToolResult      MessageType = "tool_result"
	MessageEvent           MessageType = "event"
	MessageShutdown        MessageType = "shutdown"
	MessageShutdownResult  MessageType = "shutdown_result"
	MessageError           MessageType = "error"
	// MessageCancel is a notification: the host asks the plugin to abandon one
	// in-flight request. It is never answered and never carries reply_to.
	MessageCancel MessageType = "request.cancel"
)

// Method names are retained for the host API while mapping to the SDK's
// message type vocabulary on the wire.
const (
	MethodHandshake = string(MessageHandshake)
	MethodHealth    = string(MessageHealth)
	MethodToolCall  = string(MessageToolInvoke)
	MethodShutdown  = string(MessageShutdown)
	MethodCancel    = string(MessageCancel)
)

func (t MessageType) Valid() bool {
	switch t {
	case MessageHandshake, MessageHandshakeResult,
		MessageHealth, MessageHealthResult,
		MessageToolInvoke, MessageToolResult,
		MessageEvent, MessageShutdown, MessageShutdownResult,
		MessageError, MessageCancel:
		return true
	default:
		return false
	}
}

// Envelope is the versioned JSON Lines wire format shared with sdk/plugin-go.
// Requests carry an id; responses carry their own id and reply_to containing
// the request id. Events are unsolicited and are routed to Client.Events.
type Envelope struct {
	Protocol string          `json:"protocol"`
	Type     MessageType     `json:"type"`
	ID       string          `json:"id"`
	ReplyTo  string          `json:"reply_to,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Error    *RPCError       `json:"error,omitempty"`

	// Deprecated aliases make the low-level API easier to migrate from the
	// initial internal design. They are never serialized and are normalized by
	// NewRequest/NewEvent only.
	Kind   MessageKind     `json:"-"`
	Method string          `json:"-"`
	Params json.RawMessage `json:"-"`
	Result json.RawMessage `json:"-"`
}

type RPCError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

func (e RPCError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

type MessageKind string

const (
	KindRequest  MessageKind = "request"
	KindResponse MessageKind = "response"
	KindEvent    MessageKind = "event"
)

func NewRequest(id, method string, params any) (Envelope, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(method) == "" {
		return Envelope{}, fmt.Errorf("%w: request id and method are required", ErrInvalidProtocol)
	}
	typeValue, ok := requestType(method)
	if !ok {
		return Envelope{}, fmt.Errorf("%w: unknown request method %q", ErrInvalidProtocol, method)
	}
	raw, err := marshalPayload(params)
	if err != nil {
		return Envelope{}, fmt.Errorf("%w: request payload: %v", ErrInvalidProtocol, err)
	}
	envelope := Envelope{Protocol: ProtocolName, Type: typeValue, ID: id, Payload: raw, Method: method, Params: raw, Kind: KindRequest}
	return envelope, envelope.Validate()
}

func NewEvent(method string, params any) (Envelope, error) {
	if strings.TrimSpace(method) == "" {
		return Envelope{}, fmt.Errorf("%w: event method is required", ErrInvalidProtocol)
	}
	raw, err := marshalPayload(params)
	if err != nil {
		return Envelope{}, fmt.Errorf("%w: event payload: %v", ErrInvalidProtocol, err)
	}
	id := fmt.Sprintf("event-%d", eventSequence.Add(1))
	envelope := Envelope{Protocol: ProtocolName, Type: MessageEvent, ID: id, Payload: raw, Method: method, Params: raw, Kind: KindEvent}
	return envelope, envelope.Validate()
}

// NewResponse constructs a wire-compatible response and infers the response
// type from the high-level result. For explicit control use NewTypedResponse.
func NewResponse(replyTo string, result any, rpcErr *RPCError) (Envelope, error) {
	typeValue := responseType(result)
	return NewTypedResponse(typeValue, replyTo, result, rpcErr)
}

func NewTypedResponse(typeValue MessageType, replyTo string, result any, rpcErr *RPCError) (Envelope, error) {
	if strings.TrimSpace(replyTo) == "" {
		return Envelope{}, fmt.Errorf("%w: response reply_to is required", ErrInvalidProtocol)
	}
	if rpcErr != nil {
		typeValue = MessageError
		if strings.TrimSpace(rpcErr.Code) == "" || strings.TrimSpace(rpcErr.Message) == "" {
			return Envelope{}, fmt.Errorf("%w: response error code and message are required", ErrInvalidProtocol)
		}
	}
	raw, err := marshalPayload(result)
	if err != nil {
		return Envelope{}, fmt.Errorf("%w: response payload: %v", ErrInvalidProtocol, err)
	}
	envelope := Envelope{Protocol: ProtocolName, Type: typeValue, ID: fmt.Sprintf("res-%d", eventSequence.Add(1)), ReplyTo: replyTo, Payload: raw, Error: rpcErr, Kind: KindResponse, Result: raw}
	return envelope, envelope.Validate()
}

var eventSequence atomic.Uint64

func marshalPayload(value any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage(`{}`), nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return json.RawMessage(`{}`), nil
	}
	return json.RawMessage(raw), nil
}

func requestType(method string) (MessageType, bool) {
	switch method {
	case MethodHandshake:
		return MessageHandshake, true
	case MethodHealth:
		return MessageHealth, true
	case MethodToolCall:
		return MessageToolInvoke, true
	case MethodShutdown:
		return MessageShutdown, true
	case MethodCancel:
		return MessageCancel, true
	default:
		return "", false
	}
}

func responseType(result any) MessageType {
	switch result.(type) {
	case HandshakeResult, *HandshakeResult:
		return MessageHandshakeResult
	case HealthResult, *HealthResult:
		return MessageHealthResult
	case ToolInvokeResult, *ToolInvokeResult:
		return MessageToolResult
	case ShutdownResult, *ShutdownResult:
		return MessageShutdownResult
	default:
		return MessageShutdownResult
	}
}

func (e Envelope) Validate() error {
	if e.Protocol != ProtocolName {
		return fmt.Errorf("%w: expected %q, got %q", ErrInvalidProtocol, ProtocolName, e.Protocol)
	}
	if !e.Type.Valid() {
		return fmt.Errorf("%w: unknown message type %q", ErrInvalidProtocol, e.Type)
	}
	if strings.TrimSpace(e.ID) == "" || strings.ContainsAny(e.ID, "\r\n") {
		return fmt.Errorf("%w: message id is required and cannot contain newlines", ErrInvalidProtocol)
	}
	if e.ReplyTo != "" && strings.ContainsAny(e.ReplyTo, "\r\n") {
		return fmt.Errorf("%w: reply_to cannot contain newlines", ErrInvalidProtocol)
	}
	if len(e.Payload) > 0 && !json.Valid(e.Payload) {
		return fmt.Errorf("%w: payload is not valid JSON", ErrInvalidProtocol)
	}
	if e.Error != nil && (strings.TrimSpace(e.Error.Code) == "" || strings.TrimSpace(e.Error.Message) == "") {
		return fmt.Errorf("%w: error code and message are required", ErrInvalidProtocol)
	}
	switch e.Type {
	case MessageHandshake, MessageHealth, MessageToolInvoke, MessageShutdown, MessageCancel:
		if e.ReplyTo != "" || e.Error != nil {
			return fmt.Errorf("%w: request cannot contain reply_to or error", ErrInvalidProtocol)
		}
	case MessageHandshakeResult, MessageHealthResult, MessageToolResult, MessageShutdownResult:
		if e.ReplyTo == "" || e.Error != nil {
			return fmt.Errorf("%w: response requires reply_to and cannot contain error", ErrInvalidProtocol)
		}
	case MessageEvent:
		if e.ReplyTo != "" || e.Error != nil {
			return fmt.Errorf("%w: event cannot contain reply_to or error", ErrInvalidProtocol)
		}
	case MessageError:
		if e.ReplyTo == "" || e.Error == nil {
			return fmt.Errorf("%w: error response requires reply_to and error", ErrInvalidProtocol)
		}
	}
	return nil
}

type HandshakeParams struct {
	ProtocolVersion string            `json:"protocol_version"`
	CoreVersion     string            `json:"core_version"`
	PluginID        string            `json:"plugin_id,omitempty"`
	Grants          []PermissionGrant `json:"grants,omitempty"`
}

type PermissionGrant struct {
	Capability string          `json:"capability"`
	Scope      json.RawMessage `json:"scope,omitempty"`
	ExpiresAt  time.Time       `json:"expires_at,omitempty"`
}

// CapabilityGrant is retained as a convenient typed alias for host callers;
// the wire format uses PermissionGrant.Scope JSON so plugins in other
// languages can define their own scope shape.
type CapabilityGrant = PermissionGrant

type HandshakeResult struct {
	Accepted        bool              `json:"accepted"`
	ProtocolVersion string            `json:"protocol_version"`
	PluginID        string            `json:"plugin_id"`
	PluginVersion   string            `json:"plugin_version"`
	Granted         []PermissionGrant `json:"granted,omitempty"`
	Reason          string            `json:"reason,omitempty"`
}

type HealthParams struct {
	Probe string `json:"probe,omitempty"`
}

type HealthResult struct {
	Status          string    `json:"status"`
	PluginID        string    `json:"plugin_id"`
	PluginVersion   string    `json:"plugin_version"`
	ProtocolVersion string    `json:"protocol_version"`
	CheckedAt       time.Time `json:"checked_at"`
}

type ToolInvokeParams struct {
	ToolID    string          `json:"tool_id"`
	Arguments json.RawMessage `json:"arguments"`
	Deadline  time.Time       `json:"deadline,omitempty"`
}

type ToolInvokeResult struct {
	OK       bool              `json:"ok"`
	Output   json.RawMessage   `json:"output,omitempty"`
	Error    *RPCError         `json:"error,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type ShutdownParams struct {
	Reason string `json:"reason,omitempty"`
}

type ShutdownResult struct {
	Accepted bool `json:"accepted"`
}

type CancelParams struct {
	RequestID string `json:"request_id"`
}

func (p HandshakeParams) Valid() error {
	if p.ProtocolVersion == "" || p.CoreVersion == "" {
		return fmt.Errorf("%w: core and protocol versions are required", ErrInvalidProtocol)
	}
	if p.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: unsupported handshake protocol %q", ErrInvalidProtocol, p.ProtocolVersion)
	}
	return nil
}

func (r HandshakeResult) Valid(expectedPluginID string) error {
	if !r.Accepted || r.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: plugin rejected handshake or protocol is incompatible", ErrHandshakeFailed)
	}
	if expectedPluginID != "" && r.PluginID != expectedPluginID {
		return fmt.Errorf("%w: plugin id %q does not match %q", ErrHandshakeFailed, r.PluginID, expectedPluginID)
	}
	return nil
}

func (r HealthResult) Valid() error {
	if strings.EqualFold(strings.TrimSpace(r.Status), "ok") {
		return nil
	}
	return fmt.Errorf("%w: status %q", ErrHealthFailed, r.Status)
}
