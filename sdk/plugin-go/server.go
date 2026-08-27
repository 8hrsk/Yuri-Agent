package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// ToolHandler is implemented by a plugin's business logic. The host has
// already made the permission decision before invoking a tool; the server
// only exposes the effective grants for optional plugin-side checks.
type ToolHandler interface {
	Invoke(context.Context, ToolInvokeRequest, []PermissionGrant) (ToolResult, error)
}

// ToolHandlerFunc adapts a function to ToolHandler.
type ToolHandlerFunc func(context.Context, ToolInvokeRequest, []PermissionGrant) (ToolResult, error)

func (f ToolHandlerFunc) Invoke(ctx context.Context, request ToolInvokeRequest, grants []PermissionGrant) (ToolResult, error) {
	return f(ctx, request, grants)
}

type ServerOptions struct {
	MaxFrameBytes    int
	ToolTimeout      time.Duration
	HandshakeTimeout time.Duration
	Now              func() time.Time
	Logger           func(string, ...any)
}

func (o ServerOptions) withDefaults() ServerOptions {
	if o.MaxFrameBytes <= 0 || o.MaxFrameBytes > MaxFrameBytes {
		o.MaxFrameBytes = MaxFrameBytes
	}
	if o.ToolTimeout <= 0 {
		o.ToolTimeout = DefaultToolTimeout
	}
	if o.HandshakeTimeout <= 0 {
		o.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = func(string, ...any) {}
	}
	return o
}

// Server implements the plugin side of the versioned stdio protocol. It
// accepts host requests serially and can publish events concurrently through
// EmitEvent. stdout is reserved for protocol frames; plugin logs belong on
// stderr via the supplied Logger.
type Server struct {
	manifest Manifest
	handler  ToolHandler
	options  ServerOptions

	mu      sync.RWMutex
	writer  *LockedWriter
	ready   bool
	stopped bool
	grants  []PermissionGrant
}

func NewServer(manifest Manifest, handler ToolHandler, options ServerOptions) (*Server, error) {
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("plugin server: invalid manifest: %w", err)
	}
	if handler == nil {
		return nil, errors.New("plugin server: tool handler is required")
	}
	return &Server{manifest: manifest, handler: handler, options: options.withDefaults()}, nil
}

// Serve reads JSON-lines frames until shutdown, context cancellation or EOF.
// A malformed frame is reported and terminates the session because continuing
// after framing/protocol corruption could desynchronize request IDs.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	if s == nil {
		return errors.New("plugin server: nil server")
	}
	if in == nil || out == nil {
		return errors.New("plugin server: input and output are required")
	}
	s.mu.Lock()
	if s.writer != nil {
		s.mu.Unlock()
		return errors.New("plugin server: already serving")
	}
	s.writer = NewLockedWriter(out)
	s.mu.Unlock()

	reader := NewFrameReader(in, s.options.MaxFrameBytes)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		message, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		shouldStop, err := s.handle(ctx, message)
		if err != nil {
			return err
		}
		if shouldStop {
			return nil
		}
	}
}

func (s *Server) handle(parent context.Context, message Envelope) (bool, error) {
	s.mu.RLock()
	ready := s.ready
	s.mu.RUnlock()

	switch message.Type {
	case MessageHandshake:
		return false, s.handleHandshake(message)
	case MessageHealth:
		if !ready {
			return false, s.writeError(message.ID, "not_ready", ErrNotReady.Error(), false)
		}
		return false, s.handleHealth(message)
	case MessageToolInvoke:
		if !ready {
			return false, s.writeError(message.ID, "not_ready", ErrNotReady.Error(), false)
		}
		return false, s.handleTool(parent, message)
	case MessageShutdown:
		if !ready {
			return false, s.writeError(message.ID, "not_ready", ErrNotReady.Error(), false)
		}
		return true, s.handleShutdown(message)
	default:
		return false, s.writeError(message.ID, "unsupported_message", fmt.Sprintf("message type %q is not accepted by plugin", message.Type), false)
	}
}

func (s *Server) handleHandshake(message Envelope) error {
	var request HandshakeRequest
	if err := DecodePayload(message, &request); err != nil {
		return s.writeError(message.ID, "invalid_handshake", err.Error(), false)
	}
	response := HandshakeResponse{
		Accepted:        false,
		ProtocolVersion: ProtocolVersion,
		PluginID:        s.manifest.ID,
		PluginVersion:   s.manifest.Version,
	}
	if request.ProtocolVersion != ProtocolVersion {
		response.Reason = fmt.Sprintf("unsupported protocol version %q", request.ProtocolVersion)
		return s.writeResponse(MessageHandshakeResult, message.ID, response)
	}
	if request.PluginID != "" && request.PluginID != s.manifest.ID {
		response.Reason = "handshake plugin_id does not match manifest"
		return s.writeResponse(MessageHandshakeResult, message.ID, response)
	}
	if err := validateGrants(request.Grants); err != nil {
		response.Reason = err.Error()
		return s.writeResponse(MessageHandshakeResult, message.ID, response)
	}
	grants := cloneGrants(request.Grants)
	response.Accepted = true
	response.Granted = cloneGrants(grants)
	s.mu.Lock()
	s.ready = true
	s.grants = grants
	s.mu.Unlock()
	return s.writeResponse(MessageHandshakeResult, message.ID, response)
}

func (s *Server) handleHealth(message Envelope) error {
	return s.writeResponse(MessageHealthResult, message.ID, HealthResponse{
		Status:          "ok",
		PluginID:        s.manifest.ID,
		PluginVersion:   s.manifest.Version,
		ProtocolVersion: ProtocolVersion,
		CheckedAt:       s.options.Now().UTC(),
	})
}

func (s *Server) handleTool(parent context.Context, message Envelope) error {
	var request ToolInvokeRequest
	if err := DecodePayload(message, &request); err != nil {
		return s.writeError(message.ID, "invalid_tool_invoke", err.Error(), false)
	}
	if err := validateIdentifier("tool_id", request.ToolID); err != nil {
		return s.writeError(message.ID, "invalid_tool_id", err.Error(), false)
	}
	if len(request.Arguments) == 0 || !json.Valid(request.Arguments) {
		return s.writeError(message.ID, "invalid_arguments", "arguments must be valid JSON", false)
	}
	if !s.hasTool(request.ToolID) {
		return s.writeError(message.ID, "unknown_tool", fmt.Sprintf("tool %q is not declared by manifest", request.ToolID), false)
	}
	deadline := s.options.ToolTimeout
	if !request.Deadline.IsZero() {
		remaining := time.Until(request.Deadline)
		if remaining <= 0 {
			return s.writeError(message.ID, "deadline_exceeded", "tool deadline has expired", true)
		}
		if remaining < deadline {
			deadline = remaining
		}
	}
	ctx, cancel := context.WithTimeout(parent, deadline)
	defer cancel()
	s.mu.RLock()
	grants := cloneGrants(s.grants)
	s.mu.RUnlock()
	result, err := s.handler.Invoke(ctx, request, grants)
	if err != nil {
		result = ToolResult{OK: false, Error: &RPCError{Code: "tool_failed", Message: redactError(err.Error()), Retryable: false}}
	}
	if err := validateToolResult(result); err != nil {
		return s.writeError(message.ID, "invalid_tool_result", err.Error(), false)
	}
	return s.writeResponse(MessageToolResult, message.ID, result)
}

func (s *Server) handleShutdown(message Envelope) error {
	s.mu.Lock()
	s.stopped = true
	s.ready = false
	s.mu.Unlock()
	return s.writeResponse(MessageShutdownResult, message.ID, ShutdownResponse{Accepted: true})
}

func (s *Server) hasTool(id string) bool {
	for _, tool := range s.manifest.Tools {
		if tool.ID == id {
			return true
		}
	}
	return false
}

func (s *Server) writeResponse(messageType MessageType, replyTo string, payload any) error {
	response, err := NewResponse(messageType, replyTo, payload)
	if err != nil {
		return err
	}
	s.mu.RLock()
	writer := s.writer
	s.mu.RUnlock()
	if writer == nil {
		return errors.New("plugin server: output is not initialized")
	}
	return Encode(writer, response)
}

func (s *Server) writeError(replyTo, code, message string, retryable bool) error {
	response, err := NewErrorResponse(replyTo, code, redactError(message), retryable)
	if err != nil {
		return err
	}
	s.mu.RLock()
	writer := s.writer
	s.mu.RUnlock()
	if writer == nil {
		return errors.New("plugin server: output is not initialized")
	}
	return Encode(writer, response)
}

// EmitEvent publishes an event to the host. The event source must be declared
// by the manifest and the handshake must have completed.
func (s *Server) EmitEvent(ctx context.Context, event Event) error {
	if s == nil {
		return errors.New("plugin server: nil server")
	}
	if err := validateEvent(event); err != nil {
		return err
	}
	s.mu.RLock()
	ready := s.ready
	stopped := s.stopped
	writer := s.writer
	s.mu.RUnlock()
	if !ready || stopped {
		return ErrNotReady
	}
	if !s.hasEvent(event.Source) {
		return fmt.Errorf("event source %q is not declared by manifest", event.Source)
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = s.options.Now().UTC()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	message, err := NewRequest(MessageEvent, event)
	if err != nil {
		return err
	}
	if writer == nil {
		return errors.New("plugin server: output is not initialized")
	}
	return Encode(writer, message)
}

func (s *Server) Grants() []PermissionGrant {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneGrants(s.grants)
}

func (s *Server) hasEvent(id string) bool {
	for _, event := range s.manifest.Events {
		if event.ID == id {
			return true
		}
	}
	return false
}

func validateGrants(grants []PermissionGrant) error {
	seen := make(map[string]struct{}, len(grants))
	for i, grant := range grants {
		if _, ok := knownCapabilities[grant.Capability]; !ok {
			return fmt.Errorf("grant[%d]: unknown capability %q", i, grant.Capability)
		}
		if _, ok := seen[grant.Capability]; ok {
			return fmt.Errorf("grant[%d]: duplicate capability %q", i, grant.Capability)
		}
		seen[grant.Capability] = struct{}{}
		if len(grant.Scope) > 0 && !json.Valid(grant.Scope) {
			return fmt.Errorf("grant[%d]: scope is invalid JSON", i)
		}
	}
	return nil
}

func validateToolResult(result ToolResult) error {
	if result.OK {
		if len(result.Output) == 0 || !json.Valid(result.Output) {
			return errors.New("successful tool result must contain valid JSON output")
		}
		if result.Error != nil {
			return errors.New("successful tool result cannot contain an error")
		}
		return nil
	}
	if result.Error == nil || result.Error.Code == "" || result.Error.Message == "" {
		return errors.New("failed tool result must contain an error")
	}
	if len(result.Error.Message) > 4096 {
		return errors.New("tool error message is too long")
	}
	if len(result.Output) > 0 && !json.Valid(result.Output) {
		return errors.New("tool error output is invalid JSON")
	}
	return nil
}

func validateEvent(event Event) error {
	if err := validateIdentifier("event.source", event.Source); err != nil {
		return err
	}
	if err := validateIdentifier("event.event_type", event.EventType); err != nil {
		return err
	}
	if len(event.Payload) == 0 || len(event.Payload) > MaxEventPayloadBytes || !json.Valid(event.Payload) {
		return errors.New("event.payload must be valid JSON within the event size limit")
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	return nil
}

func cloneGrants(grants []PermissionGrant) []PermissionGrant {
	if len(grants) == 0 {
		return nil
	}
	result := make([]PermissionGrant, len(grants))
	copy(result, grants)
	for i := range result {
		result[i].Scope = append(json.RawMessage(nil), grants[i].Scope...)
	}
	return result
}

func redactError(message string) string {
	if len(message) > 4096 {
		return message[:4096]
	}
	return message
}
