package plugin

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	// MaxFrameBytes bounds one JSON-lines frame in both directions. A plugin
	// should return large artifacts through a future blob capability instead of
	// attempting to put them into one RPC message.
	MaxFrameBytes        = 1 << 20
	MaxPayloadBytes      = 768 << 10
	MaxEventPayloadBytes = 512 << 10

	DefaultToolTimeout      = 60 * time.Second
	DefaultHandshakeTimeout = 10 * time.Second
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
)

func (t MessageType) Valid() bool {
	switch t {
	case MessageHandshake, MessageHandshakeResult,
		MessageHealth, MessageHealthResult,
		MessageToolInvoke, MessageToolResult,
		MessageEvent, MessageShutdown, MessageShutdownResult,
		MessageError:
		return true
	default:
		return false
	}
}

// Envelope is the only wire-level object. Each envelope is encoded as one
// UTF-8 JSON object followed by a newline. IDs are scoped to one process
// session and correlate responses to requests.
type Envelope struct {
	Protocol string          `json:"protocol"`
	Type     MessageType     `json:"type"`
	ID       string          `json:"id"`
	ReplyTo  string          `json:"reply_to,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Error    *RPCError       `json:"error,omitempty"`
}

// RPCError is safe to expose to the host. Details must be redacted by the
// plugin and must not contain credentials or arbitrary file contents.
type RPCError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

var (
	ErrFrameTooLarge    = errors.New("plugin rpc: frame exceeds size limit")
	ErrInvalidFrame     = errors.New("plugin rpc: invalid frame")
	ErrNotReady         = errors.New("plugin rpc: handshake is required")
	ErrProtocolMismatch = errors.New("plugin rpc: protocol mismatch")
	ErrInvalidMessage   = errors.New("plugin rpc: invalid message")
)

func NewID(prefix string) string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand failure is exceptionally unlikely. Returning a timestamp
		// still preserves uniqueness for diagnostics, while callers remain
		// responsible for rejecting an empty ID (which can never occur here).
		return sanitizeIDPrefix(prefix) + fmt.Sprintf("-%x", time.Now().UnixNano())
	}
	return sanitizeIDPrefix(prefix) + "-" + hex.EncodeToString(raw[:])
}

func sanitizeIDPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "msg"
	}
	var b strings.Builder
	for _, r := range prefix {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		}
		if b.Len() >= 24 {
			break
		}
	}
	if b.Len() == 0 {
		return "msg"
	}
	return b.String()
}

func NewRequest(messageType MessageType, payload any) (Envelope, error) {
	return newEnvelope(messageType, NewID("req"), "", payload)
}

func NewResponse(messageType MessageType, replyTo string, payload any) (Envelope, error) {
	if err := ValidateID(replyTo); err != nil {
		return Envelope{}, fmt.Errorf("reply_to: %w", err)
	}
	return newEnvelope(messageType, NewID("res"), replyTo, payload)
}

func NewErrorResponse(replyTo, code, message string, retryable bool) (Envelope, error) {
	if err := ValidateID(replyTo); err != nil {
		return Envelope{}, fmt.Errorf("reply_to: %w", err)
	}
	if strings.TrimSpace(code) == "" || strings.TrimSpace(message) == "" {
		return Envelope{}, errors.New("plugin rpc: error code and message are required")
	}
	return Envelope{
		Protocol: ProtocolName,
		Type:     MessageError,
		ID:       NewID("err"),
		ReplyTo:  replyTo,
		Error:    &RPCError{Code: code, Message: message, Retryable: retryable},
	}, nil
}

func newEnvelope(messageType MessageType, id, replyTo string, payload any) (Envelope, error) {
	if !messageType.Valid() {
		return Envelope{}, fmt.Errorf("plugin rpc: unknown message type %q", messageType)
	}
	if err := ValidateID(id); err != nil {
		return Envelope{}, err
	}
	var encoded json.RawMessage
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return Envelope{}, fmt.Errorf("plugin rpc: encode payload: %w", err)
		}
		encoded = data
	}
	message := Envelope{Protocol: ProtocolName, Type: messageType, ID: id, ReplyTo: replyTo, Payload: encoded}
	if err := message.Validate(); err != nil {
		return Envelope{}, err
	}
	return message, nil
}

func (e Envelope) Validate() error {
	if e.Protocol != ProtocolName {
		return fmt.Errorf("%w: expected %q, got %q", ErrProtocolMismatch, ProtocolName, e.Protocol)
	}
	if !e.Type.Valid() {
		return fmt.Errorf("%w: unknown type %q", ErrInvalidMessage, e.Type)
	}
	if err := ValidateID(e.ID); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if e.ReplyTo != "" {
		if err := ValidateID(e.ReplyTo); err != nil {
			return fmt.Errorf("reply_to: %w", err)
		}
	}
	if len(e.Payload) > MaxPayloadBytes {
		return fmt.Errorf("%w: payload exceeds %d bytes", ErrFrameTooLarge, MaxPayloadBytes)
	}
	if len(e.Payload) > 0 && !json.Valid(e.Payload) {
		return fmt.Errorf("%w: payload is invalid JSON", ErrInvalidMessage)
	}
	if e.Error != nil {
		if strings.TrimSpace(e.Error.Code) == "" || strings.TrimSpace(e.Error.Message) == "" {
			return fmt.Errorf("%w: error code and message are required", ErrInvalidMessage)
		}
		if len(e.Error.Code) > 64 || len(e.Error.Message) > 4096 {
			return fmt.Errorf("%w: error field is too long", ErrInvalidMessage)
		}
	}
	if e.Type == MessageError && e.Error == nil {
		return fmt.Errorf("%w: error message requires error field", ErrInvalidMessage)
	}
	if e.Type != MessageError && e.Error != nil {
		return fmt.Errorf("%w: error field only allowed on error messages", ErrInvalidMessage)
	}
	return nil
}

func (e Envelope) MarshalJSON() ([]byte, error) {
	type alias Envelope
	data, err := json.Marshal(alias(e))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFrameBytes {
		return nil, ErrFrameTooLarge
	}
	return data, nil
}

func Encode(w io.Writer, message Envelope) error {
	if err := message.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("plugin rpc: encode message: %w", err)
	}
	if len(data)+1 > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("plugin rpc: write message: %w", err)
	}
	return nil
}

// FrameReader is a bounded JSON-lines reader. It accepts a final frame at EOF
// without a newline but never allocates beyond MaxFrameBytes.
type FrameReader struct {
	reader *bufio.Reader
	max    int
}

func NewFrameReader(reader io.Reader, maxBytes int) *FrameReader {
	if maxBytes <= 0 || maxBytes > MaxFrameBytes {
		maxBytes = MaxFrameBytes
	}
	return &FrameReader{reader: bufio.NewReaderSize(reader, 32<<10), max: maxBytes}
}

func (r *FrameReader) Read() (Envelope, error) {
	if r == nil || r.reader == nil {
		return Envelope{}, errors.New("plugin rpc: nil frame reader")
	}
	frame, err := readFrame(r.reader, r.max)
	if err != nil {
		return Envelope{}, err
	}
	var message Envelope
	if err := json.Unmarshal(frame, &message); err != nil {
		return Envelope{}, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	if err := message.Validate(); err != nil {
		return Envelope{}, err
	}
	return message, nil
}

func readFrame(reader *bufio.Reader, maxBytes int) ([]byte, error) {
	frame := make([]byte, 0, 1024)
	for {
		line, prefix, err := reader.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && len(frame) > 0 {
				break
			}
			return nil, err
		}
		frame = append(frame, line...)
		if len(frame) > maxBytes {
			return nil, ErrFrameTooLarge
		}
		if !prefix {
			break
		}
	}
	if len(frame) == 0 {
		return nil, ErrInvalidFrame
	}
	return frame, nil
}

func ValidateID(id string) error {
	if id == "" || len(id) > MaxIdentifierLength {
		return errors.New("must be non-empty and at most 128 bytes")
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f || r == '\n' || r == '\r' {
			return errors.New("contains a control character")
		}
	}
	return nil
}

func DecodePayload(message Envelope, target any) error {
	if err := message.Validate(); err != nil {
		return err
	}
	if len(message.Payload) == 0 {
		return errors.New("plugin rpc: message payload is empty")
	}
	if err := json.Unmarshal(message.Payload, target); err != nil {
		return fmt.Errorf("plugin rpc: decode %s payload: %w", message.Type, err)
	}
	return nil
}

// HandshakeRequest is sent by the host before any other request. Permissions
// in Grants are the host's effective grants, not the plugin's declaration.
type HandshakeRequest struct {
	CoreVersion     string            `json:"core_version"`
	ProtocolVersion string            `json:"protocol_version"`
	PluginID        string            `json:"plugin_id"`
	Grants          []PermissionGrant `json:"grants,omitempty"`
}

type PermissionGrant struct {
	Capability string          `json:"capability"`
	Scope      json.RawMessage `json:"scope,omitempty"`
	ExpiresAt  time.Time       `json:"expires_at,omitempty"`
}

type HandshakeResponse struct {
	Accepted        bool              `json:"accepted"`
	ProtocolVersion string            `json:"protocol_version"`
	PluginID        string            `json:"plugin_id"`
	PluginVersion   string            `json:"plugin_version"`
	Granted         []PermissionGrant `json:"granted,omitempty"`
	Reason          string            `json:"reason,omitempty"`
}

type HealthRequest struct {
	Probe string `json:"probe,omitempty"`
}

type HealthResponse struct {
	Status          string    `json:"status"`
	PluginID        string    `json:"plugin_id"`
	PluginVersion   string    `json:"plugin_version"`
	ProtocolVersion string    `json:"protocol_version"`
	CheckedAt       time.Time `json:"checked_at"`
}

type ToolInvokeRequest struct {
	ToolID    string          `json:"tool_id"`
	Arguments json.RawMessage `json:"arguments"`
	Deadline  time.Time       `json:"deadline,omitempty"`
}

type ToolResult struct {
	OK       bool              `json:"ok"`
	Output   json.RawMessage   `json:"output,omitempty"`
	Error    *RPCError         `json:"error,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type Event struct {
	Source     string          `json:"source"`
	EventType  string          `json:"event_type"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt time.Time       `json:"occurred_at"`
}

type ShutdownRequest struct {
	Reason string `json:"reason,omitempty"`
}

type ShutdownResponse struct {
	Accepted bool `json:"accepted"`
}

// PermissionGranted reports a copy of the latest grants received during the
// handshake. It is intentionally read-only from the caller's perspective.
func PermissionGranted(grants []PermissionGrant, capability string) bool {
	for _, grant := range grants {
		if grant.Capability == capability {
			return true
		}
	}
	return false
}

// LockedWriter serializes JSON-lines writes from a server's request loop and
// asynchronous event publishers.
type LockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func NewLockedWriter(w io.Writer) *LockedWriter { return &LockedWriter{w: w} }

func (w *LockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}
