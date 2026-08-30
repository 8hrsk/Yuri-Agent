package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

// awaitDisconnect blocks until the client goes away. The request body must be
// drained first: net/http only starts the background read that detects a
// disconnect once the handler has consumed the request body, so without this a
// handler would keep httptest.Server.Close waiting for the full fallback.
func awaitDisconnect(r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	select {
	case <-r.Context().Done():
	case <-time.After(5 * time.Second):
	}
}

// slowStreamServer emits count text deltas separated by gap, then [DONE]. The
// total duration is deliberately allowed to exceed the client Timeout.
func slowStreamServer(t *testing.T, count int, gap time.Duration) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer is not a flusher")
			return
		}
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		for i := 0; i < count; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(gap):
			}
			writeSSEJSON(w, flusher, "response.output_text.delta", map[string]any{
				"type": "response.output_text.delta", "response_id": "resp_1", "delta": "tok",
			})
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(server.Close)
	return server
}

// TestSlowStreamOutlivesFirstByteTimeout is the M-20 regression: a stream that
// keeps producing data must not be killed because its total duration exceeds
// the configured Timeout. Under the previous single-timeout implementation the
// request context expired mid-generation and this failed.
func TestSlowStreamOutlivesFirstByteTimeout(t *testing.T) {
	const chunks = 20
	const gap = 20 * time.Millisecond
	const timeout = 150 * time.Millisecond
	server := slowStreamServer(t, chunks, gap)

	client, err := New(Config{
		BaseURL:           server.URL,
		Timeout:           timeout,
		StreamIdleTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	stream, err := client.Start(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer stream.Close()

	deltas := 0
	completed := false
	for {
		event, recvErr := stream.Recv(context.Background())
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("recv after %s and %d deltas: %v", time.Since(start), deltas, recvErr)
		}
		switch event.Type {
		case agent.ModelEventTextDelta:
			deltas++
		case agent.ModelEventCompleted:
			completed = true
		}
		if completed {
			break
		}
	}
	elapsed := time.Since(start)
	if deltas != chunks {
		t.Fatalf("deltas = %d, want %d", deltas, chunks)
	}
	if !completed {
		t.Fatal("stream did not complete")
	}
	if elapsed <= timeout {
		t.Fatalf("stream finished in %s, which does not exercise the total-duration regression (timeout %s)", elapsed, timeout)
	}
}

// TestStalledStreamFailsWithIdleTimeout covers the other half of the split: a
// stream that goes completely silent is still cancelled, with a typed error.
func TestStalledStreamFailsWithIdleTimeout(t *testing.T) {
	const idle = 80 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeSSEJSON(w, flusher, "response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "response_id": "resp_1", "delta": "tok",
		})
		awaitDisconnect(r)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Timeout: 5 * time.Second, StreamIdleTimeout: idle})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Start(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer stream.Close()

	event, err := stream.Recv(context.Background())
	if err != nil || event.Type != agent.ModelEventTextDelta {
		t.Fatalf("first event = %#v, err = %v", event, err)
	}
	start := time.Now()
	_, err = stream.Recv(context.Background())
	elapsed := time.Since(start)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorKindTimeout {
		t.Fatalf("error = %#v, want ErrorKindTimeout", err)
	}
	if !strings.Contains(providerErr.Message, "stalled") {
		t.Fatalf("message = %q, want a stall description", providerErr.Message)
	}
	if providerErr.Retryable {
		t.Fatal("a mid-stream stall must not be reported as retryable: partial output was already delivered")
	}
	if elapsed < idle {
		t.Fatalf("idle timeout fired after %s, before the configured %s", elapsed, idle)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("idle timeout took %s to fire", elapsed)
	}
}

// TestFirstByteTimeoutAfterHeaders: a provider that answers with headers and
// then never sends a byte must fail against the hard deadline, not the idle one.
func TestFirstByteTimeoutAfterHeaders(t *testing.T) {
	const timeout = 100 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		awaitDisconnect(r)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Timeout: timeout, StreamIdleTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Start(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer stream.Close()

	start := time.Now()
	_, err = stream.Recv(context.Background())
	elapsed := time.Since(start)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorKindTimeout {
		t.Fatalf("error = %#v, want ErrorKindTimeout", err)
	}
	if !strings.Contains(providerErr.Message, "first response byte") {
		t.Fatalf("message = %q, want a first-byte description", providerErr.Message)
	}
	if !providerErr.Retryable {
		t.Fatal("a first-byte timeout should be retryable: nothing was delivered yet")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("first-byte timeout took %s to fire", elapsed)
	}
}

// TestFirstByteTimeoutBeforeHeaders keeps the fail-fast behavior for a provider
// that never responds at all: Start itself must return the typed timeout.
func TestFirstByteTimeoutBeforeHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		awaitDisconnect(r)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Timeout: 100 * time.Millisecond, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = client.Start(context.Background(), testRequest())
	elapsed := time.Since(start)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorKindTimeout {
		t.Fatalf("error = %#v, want ErrorKindTimeout", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("start timeout took %s", elapsed)
	}
}

// TestStreamCloseCancelsRequestContext proves the cancel func is still called
// on the success path: closing the stream must cancel the server-side request.
func TestStreamCloseCancelsRequestContext(t *testing.T) {
	requestCtx := make(chan context.Context, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCtx <- r.Context()
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeSSEJSON(w, flusher, "response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "response_id": "resp_1", "delta": "tok",
		})
		awaitDisconnect(r)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Timeout: 5 * time.Second, StreamIdleTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Start(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatalf("recv: %v", err)
	}
	if err := stream.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("close: %v", err)
	}
	select {
	case ctx := <-requestCtx:
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("request context still live after stream.Close: context leak")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler never ran")
	}
}

// TestTimeoutPathsDoNotLeakGoroutines exercises every exit path — success,
// idle expiry, first-byte expiry, and a failed Start — and asserts the process
// settles back to its baseline goroutine count.
func TestTimeoutPathsDoNotLeakGoroutines(t *testing.T) {
	baseline := runtime.NumGoroutine()

	func() {
		server := slowStreamServer(t, 3, 5*time.Millisecond)
		client, err := New(Config{BaseURL: server.URL, Timeout: time.Second, StreamIdleTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		stream, err := client.Start(context.Background(), testRequest())
		if err != nil {
			t.Fatal(err)
		}
		for {
			event, recvErr := stream.Recv(context.Background())
			if recvErr != nil || event.Type == agent.ModelEventCompleted {
				break
			}
		}
		if err := stream.Close(); err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("close: %v", err)
		}
	}()

	func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			awaitDisconnect(r)
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL, Timeout: 50 * time.Millisecond, StreamIdleTimeout: 50 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		stream, err := client.Start(context.Background(), testRequest())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Recv(context.Background()); err == nil {
			t.Fatal("expected a timeout error")
		}
		_ = stream.Close()
	}()

	func() {
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			awaitDisconnect(r)
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL, Timeout: 50 * time.Millisecond, MaxAttempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Start(context.Background(), testRequest()); err == nil {
			t.Fatal("expected a timeout error")
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		current := runtime.NumGoroutine()
		if current <= baseline+2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d, baseline = %d: leaked goroutines", current, baseline)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestWaitBackoffHonoursRetryAfterBeyondMaxBackoff is the L-16 regression:
// MaxBackoff governs the adapter's own backoff, not an explicit server hint.
func TestWaitBackoffHonoursRetryAfterBeyondMaxBackoff(t *testing.T) {
	config, err := Config{
		BaseURL:        "https://api.example.com",
		InitialBackoff: 5 * time.Millisecond,
		MaxBackoff:     10 * time.Millisecond,
		MaxRetryAfter:  2 * time.Second,
	}.normalized()
	if err != nil {
		t.Fatal(err)
	}
	const retryAfter = 150 * time.Millisecond
	if config.MaxBackoff >= retryAfter {
		t.Fatalf("MaxBackoff = %s does not exercise the clamp against a %s hint", config.MaxBackoff, retryAfter)
	}
	start := time.Now()
	if err := waitBackoff(context.Background(), 1, config, retryAfter); err != nil {
		t.Fatalf("waitBackoff: %v", err)
	}
	elapsed := time.Since(start)
	// The previous implementation clamped this to MaxBackoff (10ms).
	if elapsed < retryAfter-10*time.Millisecond {
		t.Fatalf("waited %s, want at least the server-requested %s", elapsed, retryAfter)
	}
	if elapsed > time.Second {
		t.Fatalf("waited %s, far beyond the requested %s", elapsed, retryAfter)
	}
}

func TestWaitBackoffCapsRetryAfterAtMaxRetryAfter(t *testing.T) {
	config, err := Config{
		BaseURL:        "https://api.example.com",
		InitialBackoff: 5 * time.Millisecond,
		MaxBackoff:     10 * time.Millisecond,
		MaxRetryAfter:  60 * time.Millisecond,
	}.normalized()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := waitBackoff(context.Background(), 1, config, time.Hour); err != nil {
		t.Fatalf("waitBackoff: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waited %s, want the %s ceiling", elapsed, config.MaxRetryAfter)
	}
}

func TestWaitBackoffCancellation(t *testing.T) {
	config, err := Config{BaseURL: "https://api.example.com", MaxRetryAfter: time.Hour}.normalized()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	defer cancel()
	if err := waitBackoff(ctx, 1, config, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestTimeoutConfigDefaults(t *testing.T) {
	cases := []struct {
		name          string
		in            Config
		wantTimeout   time.Duration
		wantIdle      time.Duration
		wantRetryCeil time.Duration
	}{
		{
			name:          "zero value",
			in:            Config{BaseURL: "https://api.example.com"},
			wantTimeout:   defaultTimeout,
			wantIdle:      defaultTimeout,
			wantRetryCeil: defaultMaxRetryAfter,
		},
		{
			name:          "short timeout keeps the default idle budget",
			in:            Config{BaseURL: "https://api.example.com", Timeout: 10 * time.Second},
			wantTimeout:   10 * time.Second,
			wantIdle:      defaultStreamIdleTimeout,
			wantRetryCeil: defaultMaxRetryAfter,
		},
		{
			name:          "long timeout is preserved as the idle budget",
			in:            Config{BaseURL: "https://api.example.com", Timeout: 5 * time.Minute},
			wantTimeout:   5 * time.Minute,
			wantIdle:      5 * time.Minute,
			wantRetryCeil: defaultMaxRetryAfter,
		},
		{
			name:          "explicit idle budget wins",
			in:            Config{BaseURL: "https://api.example.com", Timeout: 5 * time.Minute, StreamIdleTimeout: 5 * time.Second},
			wantTimeout:   5 * time.Minute,
			wantIdle:      5 * time.Second,
			wantRetryCeil: defaultMaxRetryAfter,
		},
		{
			name:          "retry ceiling never falls below MaxBackoff",
			in:            Config{BaseURL: "https://api.example.com", MaxBackoff: 90 * time.Second, MaxRetryAfter: time.Second},
			wantTimeout:   defaultTimeout,
			wantIdle:      defaultTimeout,
			wantRetryCeil: 90 * time.Second,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := testCase.in.normalized()
			if err != nil {
				t.Fatal(err)
			}
			if got.Timeout != testCase.wantTimeout {
				t.Errorf("Timeout = %s, want %s", got.Timeout, testCase.wantTimeout)
			}
			if got.StreamIdleTimeout != testCase.wantIdle {
				t.Errorf("StreamIdleTimeout = %s, want %s", got.StreamIdleTimeout, testCase.wantIdle)
			}
			if got.MaxRetryAfter != testCase.wantRetryCeil {
				t.Errorf("MaxRetryAfter = %s, want %s", got.MaxRetryAfter, testCase.wantRetryCeil)
			}
		})
	}
}

// TestActivityBodyReleasesContextOnEOF pins the last cancel path: a body that
// reaches EOF releases the request context on its own, so a caller that drops
// the stream without closing it still cannot leak the derived context.
func TestActivityBodyReleasesContextOnEOF(t *testing.T) {
	var once sync.Once
	cancelled := make(chan struct{})
	deadline := newStreamDeadline(func() { once.Do(func() { close(cancelled) }) }, time.Second, time.Second)
	body := &activityBody{body: io.NopCloser(strings.NewReader("chunk")), deadline: deadline}

	buf := make([]byte, 8)
	if n, err := body.Read(buf); err != nil || n != len("chunk") {
		t.Fatalf("first read = (%d, %v)", n, err)
	}
	select {
	case <-cancelled:
		t.Fatal("context released while data was still flowing")
	default:
	}
	if _, err := body.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("second read error = %v, want io.EOF", err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("context not released at EOF")
	}
	if deadline.expired() {
		t.Fatal("EOF must not be reported as a timeout")
	}
	if err := deadline.err(); err != nil {
		t.Fatalf("deadline error after EOF = %v, want nil", err)
	}
}
