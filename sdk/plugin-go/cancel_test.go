package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

// serveOverPipes runs the server against a pair of pipes so a test can
// interleave frames while a handler is still running.
func serveOverPipes(t *testing.T, server *Server) (io.WriteCloser, *FrameReader, <-chan error) {
	t.Helper()
	hostToPlugin, pluginInput := io.Pipe()
	pluginOutput, hostFromPlugin := io.Pipe()
	served := make(chan error, 1)
	go func() {
		served <- server.Serve(context.Background(), hostToPlugin, hostFromPlugin)
		_ = hostFromPlugin.Close()
	}()
	t.Cleanup(func() {
		_ = pluginInput.Close()
		_ = pluginOutput.Close()
	})
	return pluginInput, NewFrameReader(pluginOutput, MaxFrameBytes), served
}

func mustEncode(t *testing.T, writer io.Writer, message Envelope) {
	t.Helper()
	if err := Encode(writer, message); err != nil {
		t.Fatalf("Encode(%s) error = %v", message.Type, err)
	}
}

func readTyped(t *testing.T, reader *FrameReader, want MessageType) Envelope {
	t.Helper()
	message, err := reader.Read()
	if err != nil {
		t.Fatalf("read %s error = %v", want, err)
	}
	if message.Type != want {
		t.Fatalf("message type = %q, want %q", message.Type, want)
	}
	return message
}

// M-29, plugin side. The host's request.cancel must reach a running handler,
// and the session must survive it: a cancelled turn is not a reason to lose
// the plugin process.
func TestServerCancelsInFlightToolAndKeepsServing(t *testing.T) {
	started := make(chan struct{})
	observed := make(chan error, 1)
	server, err := NewServer(validTestManifest(), ToolHandlerFunc(func(ctx context.Context, _ ToolInvokeRequest, _ []PermissionGrant) (ToolResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			observed <- ctx.Err()
			return ToolResult{}, ctx.Err()
		case <-time.After(10 * time.Second):
			observed <- errors.New("handler was never cancelled")
			return ToolResult{OK: true, Output: json.RawMessage(`{}`)}, nil
		}
	}), ServerOptions{ToolTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	input, output, served := serveOverPipes(t, server)

	handshake, _ := NewRequest(MessageHandshake, HandshakeRequest{
		CoreVersion: "0.4.0", ProtocolVersion: ProtocolVersion, PluginID: validTestManifest().ID,
	})
	mustEncode(t, input, handshake)
	readTyped(t, output, MessageHandshakeResult)

	invoke, _ := NewRequest(MessageToolInvoke, ToolInvokeRequest{ToolID: "echo", Arguments: json.RawMessage(`{"message":"slow"}`)})
	mustEncode(t, input, invoke)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("tool handler never started")
	}

	// The read loop must still be live while the handler runs.
	cancel, _ := NewRequest(MessageCancel, CancelRequest{RequestID: invoke.ID})
	mustEncode(t, input, cancel)

	select {
	case err := <-observed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handler context error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request.cancel never reached the running handler")
	}

	failure := readTyped(t, output, MessageToolResult)
	if failure.ReplyTo != invoke.ID {
		t.Fatalf("reply_to = %q, want %q", failure.ReplyTo, invoke.ID)
	}
	var result ToolResult
	if err := DecodePayload(failure, &result); err != nil {
		t.Fatal(err)
	}
	if result.OK || result.Error == nil || result.Error.Code != "cancelled" {
		t.Fatalf("cancelled tool result = %#v", result)
	}

	// The plugin is still serving: a cancel is not a fatal protocol event.
	health, _ := NewRequest(MessageHealth, HealthRequest{})
	mustEncode(t, input, health)
	readTyped(t, output, MessageHealthResult)

	shutdown, _ := NewRequest(MessageShutdown, ShutdownRequest{Reason: "test"})
	mustEncode(t, input, shutdown)
	readTyped(t, output, MessageShutdownResult)
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve() did not return after shutdown")
	}
}

// An unknown or stale request id is ordinary: the response may already be on
// the wire. It must never produce an error frame or end the session.
func TestServerIgnoresUnknownCancel(t *testing.T) {
	server, err := NewServer(validTestManifest(), ToolHandlerFunc(func(context.Context, ToolInvokeRequest, []PermissionGrant) (ToolResult, error) {
		return ToolResult{OK: true, Output: json.RawMessage(`{"message":"ok"}`)}, nil
	}), ServerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	input, output, served := serveOverPipes(t, server)

	handshake, _ := NewRequest(MessageHandshake, HandshakeRequest{
		CoreVersion: "0.4.0", ProtocolVersion: ProtocolVersion, PluginID: validTestManifest().ID,
	})
	mustEncode(t, input, handshake)
	readTyped(t, output, MessageHandshakeResult)

	cancel, _ := NewRequest(MessageCancel, CancelRequest{RequestID: "req-does-not-exist"})
	mustEncode(t, input, cancel)

	health, _ := NewRequest(MessageHealth, HealthRequest{})
	mustEncode(t, input, health)
	readTyped(t, output, MessageHealthResult)

	shutdown, _ := NewRequest(MessageShutdown, ShutdownRequest{Reason: "test"})
	mustEncode(t, input, shutdown)
	readTyped(t, output, MessageShutdownResult)
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve() did not return after shutdown")
	}
}

// The concurrency bound is a protocol-visible, retryable rejection rather than
// an unbounded goroutine fan-out.
func TestServerBoundsConcurrentToolInvocations(t *testing.T) {
	release := make(chan struct{})
	server, err := NewServer(validTestManifest(), ToolHandlerFunc(func(ctx context.Context, _ ToolInvokeRequest, _ []PermissionGrant) (ToolResult, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return ToolResult{}, ctx.Err()
		}
		return ToolResult{OK: true, Output: json.RawMessage(`{"message":"ok"}`)}, nil
	}), ServerOptions{MaxConcurrentTools: 1, ToolTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	input, output, served := serveOverPipes(t, server)
	defer func() {
		close(release)
		<-served
	}()

	handshake, _ := NewRequest(MessageHandshake, HandshakeRequest{
		CoreVersion: "0.4.0", ProtocolVersion: ProtocolVersion, PluginID: validTestManifest().ID,
	})
	mustEncode(t, input, handshake)
	readTyped(t, output, MessageHandshakeResult)

	first, _ := NewRequest(MessageToolInvoke, ToolInvokeRequest{ToolID: "echo", Arguments: json.RawMessage(`{"message":"a"}`)})
	second, _ := NewRequest(MessageToolInvoke, ToolInvokeRequest{ToolID: "echo", Arguments: json.RawMessage(`{"message":"b"}`)})
	mustEncode(t, input, first)
	mustEncode(t, input, second)

	rejection := readTyped(t, output, MessageError)
	if rejection.ReplyTo != second.ID || rejection.Error.Code != "too_many_requests" || !rejection.Error.Retryable {
		t.Fatalf("rejection = %#v", rejection.Error)
	}

	shutdown, _ := NewRequest(MessageShutdown, ShutdownRequest{Reason: "test"})
	mustEncode(t, input, shutdown)
	readTyped(t, output, MessageToolResult)
	readTyped(t, output, MessageShutdownResult)
}
