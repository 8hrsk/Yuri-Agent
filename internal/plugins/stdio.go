package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultMaxMessageBytes = 1 << 20
	DefaultCloseTimeout    = 2 * time.Second
	// DefaultWriteTimeout bounds a single stdin frame. A plugin that stops
	// reading its stdin must not be able to wedge the host forever.
	DefaultWriteTimeout = 5 * time.Second
	// DefaultMaxStderrBytes bounds the retained crash diagnostics per plugin.
	DefaultMaxStderrBytes = 64 << 10

	writeQueueDepth = 64
	eventQueueDepth = 64
)

// ClientConfig controls a single plugin process. Stdout is reserved for the
// JSON Lines protocol; plugin diagnostics must be written to Stderr.
type ClientConfig struct {
	Executable      string
	Args            []string
	Dir             string
	Env             []string
	InheritEnv      bool
	Stderr          io.Writer
	MaxMessageBytes int
	CloseTimeout    time.Duration
	// WriteTimeout bounds one stdin frame write. It is the last line of
	// defence when a plugin stops draining its stdin.
	WriteTimeout time.Duration
	// MaxStderrBytes bounds the retained (redacted) stderr tail.
	MaxStderrBytes int
}

type response struct {
	envelope Envelope
	err      error
}

// writeRequest hands one framed message to the dedicated writer goroutine so
// callers never block on the child process' stdin buffer.
type writeRequest struct {
	data []byte
	done chan error
}

// Client is a concurrency-safe stdio JSON Lines client. It owns the child
// process and never exposes its stdout pipe to callers.
type Client struct {
	cmd    *exec.Cmd
	stdin  *os.File
	stdout *os.File
	config ClientConfig

	writes chan writeRequest

	mu         sync.Mutex
	pending    map[string]chan response
	events     chan Envelope
	done       chan struct{}
	stdoutDone chan struct{}
	doneOnce   sync.Once
	eventsOnce sync.Once
	closeOnce  sync.Once
	closing    bool
	processErr error
	sequence   atomic.Uint64

	droppedEvents atomic.Uint64
	stderr        *stderrCapture
}

func NewClient(ctx context.Context, config ClientConfig) (*Client, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrInvalidProtocol)
	}
	if strings.TrimSpace(config.Executable) == "" {
		return nil, fmt.Errorf("%w: executable is required", ErrInvalidManifest)
	}
	if config.MaxMessageBytes <= 0 {
		config.MaxMessageBytes = DefaultMaxMessageBytes
	}
	if config.CloseTimeout <= 0 {
		config.CloseTimeout = DefaultCloseTimeout
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = DefaultWriteTimeout
	}
	if config.MaxStderrBytes <= 0 {
		config.MaxStderrBytes = DefaultMaxStderrBytes
	}
	cmd := exec.Command(config.Executable, config.Args...)
	configureProcess(cmd)
	cmd.Dir = config.Dir
	if config.InheritEnv {
		cmd.Env = append(os.Environ(), config.Env...)
	} else {
		// An explicit, empty environment is the safe default: arbitrary parent
		// process secrets must not be inherited by an untrusted plugin.
		cmd.Env = append([]string{}, config.Env...)
	}

	// Both pipes are owned by this process rather than by os/exec. A pipe the
	// host created is never closed by cmd.Wait, which removes the documented
	// race between Wait and a reader that has not drained stdout yet, and it
	// guarantees that stdin supports a write deadline.
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("%w: stdin pipe: %v", ErrPluginExited, err)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		return nil, fmt.Errorf("%w: stdout pipe: %v", ErrPluginExited, err)
	}
	capture := newStderrCapture(config.MaxStderrBytes, config.Stderr)
	cmd.Stdin = stdinRead
	cmd.Stdout = stdoutWrite
	cmd.Stderr = capture
	if err := cmd.Start(); err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		return nil, fmt.Errorf("%w: start process: %v", ErrPluginExited, err)
	}
	// The child owns its ends now; keeping the host copies open would prevent
	// EOF from ever being observed on stdout.
	_ = stdinRead.Close()
	_ = stdoutWrite.Close()

	client := &Client{
		cmd: cmd, stdin: stdinWrite, stdout: stdoutRead, config: config,
		writes:  make(chan writeRequest, writeQueueDepth),
		pending: make(map[string]chan response), events: make(chan Envelope, eventQueueDepth),
		done: make(chan struct{}), stdoutDone: make(chan struct{}),
		stderr: capture,
	}
	go client.readStdout(stdoutRead)
	go client.waitProcess()
	go client.writeLoop()
	// Not guarded by recoverPluginGoroutine: the body is one channel receive
	// and two (*os.File).Close calls. It owns no durable state, blocks no
	// caller, and by the time it runs the session is already terminal.
	go func() {
		<-client.done
		// Releasing both descriptors unblocks a reader that is still parked on
		// a pipe held open by a descendant of the plugin process.
		_ = client.stdin.Close()
		_ = client.stdout.Close()
	}()
	if done := ctx.Done(); done != nil {
		go client.closeOnContext(done)
	}
	return client, nil
}

// closeOnContext is the only thing that turns "the owner's lifecycle context
// was cancelled" into "the plugin process is gone". A panic here would leave a
// third-party child alive holding the owner's granted capabilities, so the
// recovery kills the process group directly instead of trusting Close.
func (c *Client) closeOnContext(done <-chan struct{}) {
	defer recoverPluginGoroutine("plugin_close_on_context", func(err error) {
		_ = killProcess(c.cmd)
		c.finish(fmt.Errorf("%w: %v", ErrPluginExited, err))
	})
	select {
	case <-done:
		fireFaultHook(faultCloseOnContext)
		_ = c.Close()
	case <-c.done:
	}
}

func (c *Client) readStdout(reader io.Reader) {
	defer close(c.stdoutDone)
	defer c.eventsOnce.Do(func() { close(c.events) })
	// This is the one goroutine in the package that parses bytes a hostile or
	// merely buggy third-party process controls end to end. Without the guard a
	// single fault in the decoder takes the owner's whole desktop application,
	// UI included, down with it. The report terminates the session: nothing
	// will read this stdout again, so leaving Done() open would park every
	// in-flight caller and the supervisor's watcher forever.
	defer recoverPluginGoroutine("plugin_read_stdout", func(err error) {
		c.finish(fmt.Errorf("%w: %v", ErrInvalidProtocol, err))
	})
	scanner := bufio.NewScanner(reader)
	bufferSize := c.config.MaxMessageBytes
	if bufferSize > 64*1024 {
		bufferSize = 64 * 1024
	}
	if bufferSize < 1 {
		bufferSize = 1
	}
	scanner.Buffer(make([]byte, bufferSize), c.config.MaxMessageBytes+1)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) > c.config.MaxMessageBytes {
			c.finish(fmt.Errorf("%w: got %d bytes", ErrMessageTooLarge, len(line)))
			return
		}
		var envelope Envelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			c.finish(fmt.Errorf("%w: decode JSONL: %v", ErrInvalidProtocol, err))
			return
		}
		if err := envelope.Validate(); err != nil {
			c.finish(err)
			return
		}
		fireFaultHook(faultReadStdout)
		if envelope.Type == MessageHandshakeResult || envelope.Type == MessageHealthResult || envelope.Type == MessageToolResult || envelope.Type == MessageShutdownResult || envelope.Type == MessageError {
			c.deliverResponse(envelope)
			continue
		}
		select {
		case c.events <- envelope:
		case <-c.done:
			return
		default:
			// A slow event consumer is a load problem, not a protocol
			// violation: dropping the event keeps the session alive instead of
			// turning a burst into a restart loop.
			c.droppedEvents.Add(1)
		}
	}
	if err := scanner.Err(); err != nil {
		// bufio.Scanner stores ErrTooLong on itself, so errors.Is identifies
		// an oversized line exactly.  Matching the substring "token too long"
		// instead also fired on any reader error whose own message happened to
		// quote that phrase, mislabelling an unrelated read failure as a
		// protocol size violation.
		if errors.Is(err, bufio.ErrTooLong) {
			c.finish(fmt.Errorf("%w: JSONL line is too long", ErrMessageTooLarge))
			return
		}
		c.finish(fmt.Errorf("%w: read stdout: %v", ErrPluginExited, err))
		return
	}
	// A clean EOF is reported by waitProcess so the exit status is preserved.
	// The fallback only matters for a plugin that closes stdout but keeps
	// running: callers must still fail fast instead of blocking.
	//
	// Not guarded by recoverPluginGoroutine: the body is a two-way select and
	// one call to finish, which is itself a sync.Once around map and channel
	// operations that cannot panic. It owns no state of its own.
	go func() {
		select {
		case <-c.done:
		case <-time.After(c.config.CloseTimeout):
			c.finish(nil)
		}
	}()
}

func (c *Client) deliverResponse(envelope Envelope) {
	pending := c.takePending(envelope.ReplyTo)
	if pending != nil {
		pending <- response{envelope: envelope}
	}
}

// takePending removes and returns one waiter. The unlock is deferred so a
// panic in the decoder above cannot leave c.mu held, which would wedge every
// caller and the recovery reporter itself.
func (c *Client) takePending(id string) chan response {
	c.mu.Lock()
	defer c.mu.Unlock()
	pending := c.pending[id]
	if pending != nil {
		delete(c.pending, id)
	}
	return pending
}

func (c *Client) waitProcess() {
	// Nothing else reaps the child. If this goroutine dies before Wait has
	// returned, the process is neither reaped nor killed and keeps running with
	// the owner's granted capabilities, so the recovery kills the process group
	// and terminates the session.
	reaped := false
	defer recoverPluginGoroutine("plugin_wait_process", func(err error) {
		if !reaped {
			_ = killProcess(c.cmd)
		}
		c.finish(fmt.Errorf("%w: %v", ErrPluginExited, err))
	})
	err := c.cmd.Wait()
	reaped = true
	fireFaultHook(faultWaitProcess)
	// os/exec may report the exit before the last protocol frame has been read
	// out of the pipe. Draining first keeps a short-lived plugin's tool_result
	// from being replaced by ErrPluginExited.
	select {
	case <-c.stdoutDone:
	case <-time.After(c.config.CloseTimeout):
	}
	c.mu.Lock()
	closing := c.closing
	c.mu.Unlock()
	if err != nil {
		c.finish(fmt.Errorf("%w: %v", ErrPluginExited, err))
		return
	}
	if closing {
		c.finish(nil)
		return
	}
	c.finish(ErrPluginExited)
}

func (c *Client) finish(err error) {
	c.doneOnce.Do(func() {
		if err == nil {
			// A clean EOF without a requested close is still an unexpected
			// plugin exit from the host's point of view.
			c.mu.Lock()
			if !c.closing {
				err = ErrPluginExited
			}
			c.mu.Unlock()
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.processErr = err
		for id, pending := range c.pending {
			delete(c.pending, id)
			pendingErr := err
			if pendingErr == nil {
				pendingErr = ErrPluginExited
			}
			pending <- response{err: pendingErr}
		}
		close(c.done)
	})
}

func (c *Client) nextID() string {
	return fmt.Sprintf("req-%d", c.sequence.Add(1))
}

// writeLoop is the single owner of the stdin descriptor. Serializing writes in
// one goroutine lets every caller wait on its own context instead of on a
// blocking pipe write.
func (c *Client) writeLoop() {
	// A panic here strands the frame that was already taken off the queue: its
	// caller is parked on request.done, and nothing drains c.writes again. The
	// report terminates the session first (which is what every writer's select
	// is watching), then answers the in-flight request and anything already
	// queued, so no caller is left waiting on a writer that no longer exists.
	var inFlight chan error
	defer recoverPluginGoroutine("plugin_write_loop", func(err error) {
		// A half-written or unwritten frame desynchronizes the stream exactly
		// as a write error does, so callers get the same classification.
		failure := fmt.Errorf("%w: %v", ErrPluginExited, err)
		c.finish(failure)
		if inFlight != nil {
			select {
			case inFlight <- failure:
			default:
			}
		}
		c.drainWrites(failure)
	})
	for {
		select {
		case request := <-c.writes:
			inFlight = request.done
			fireFaultHook(faultWriteLoop)
			err := c.writeFrame(request.data)
			if err != nil {
				// A failed or partial frame desynchronizes the stream; the
				// session cannot be reused. Terminate the session *before*
				// releasing the caller: otherwise the caller observes the
				// write error while Done() is still open and Err() is still
				// nil, and may reuse a client that is already doomed. The
				// done channel is buffered, so ordering these two sends
				// cannot deadlock.
				c.finish(err)
				request.done <- err
				return
			}
			request.done <- nil
			inFlight = nil
		case <-c.done:
			return
		}
	}
}

// drainWrites releases every frame still queued for a writer that will never
// run again. Each done channel is buffered, so the non-blocking send only ever
// skips a caller that has already given up.
func (c *Client) drainWrites(err error) {
	for {
		select {
		case request := <-c.writes:
			select {
			case request.done <- err:
			default:
			}
		default:
			return
		}
	}
}

func (c *Client) writeFrame(data []byte) error {
	var deadlineExpired chan struct{}
	var deadlineTimer *time.Timer
	if c.config.WriteTimeout > 0 {
		_ = c.stdin.SetWriteDeadline(time.Now().Add(c.config.WriteTimeout))
		// Windows anonymous pipes do not implement write deadlines. Closing the
		// host end is safe here because any partial frame already makes the
		// protocol session unusable, and it reliably releases a blocked write.
		if runtime.GOOS == "windows" {
			deadlineExpired = make(chan struct{})
			deadlineTimer = time.AfterFunc(c.config.WriteTimeout, func() {
				_ = c.stdin.Close()
				close(deadlineExpired)
			})
		}
	}
	written, err := c.stdin.Write(data)
	timedOut := false
	if deadlineTimer != nil && !deadlineTimer.Stop() {
		<-deadlineExpired
		timedOut = true
	}
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		return nil
	}
	if timedOut || errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("%w: stdin write timed out after %s; the plugin stopped reading its stdin", ErrPluginExited, c.config.WriteTimeout)
	}
	return fmt.Errorf("%w: write stdin: %v", ErrPluginExited, err)
}

func (c *Client) exitError() error {
	if err := c.Err(); err != nil {
		return err
	}
	return ErrPluginExited
}

func (c *Client) writeEnvelope(ctx context.Context, envelope Envelope) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := envelope.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("%w: encode JSONL: %v", ErrInvalidProtocol, err)
	}
	if len(data) > c.config.MaxMessageBytes {
		return fmt.Errorf("%w: got %d bytes", ErrMessageTooLarge, len(data))
	}
	data = append(data, '\n')
	if isClosed(c.done) {
		return c.exitError()
	}
	request := writeRequest{data: data, done: make(chan error, 1)}
	select {
	case c.writes <- request:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.exitError()
	}
	select {
	case err := <-request.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.exitError()
	}
}

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidProtocol)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id := c.nextID()
	envelope, err := NewRequest(id, method, params)
	if err != nil {
		return err
	}
	channel := make(chan response, 1)
	c.mu.Lock()
	if isClosed(c.done) {
		err := c.processErr
		c.mu.Unlock()
		if err == nil {
			err = ErrPluginExited
		}
		return err
	}
	c.pending[id] = channel
	c.mu.Unlock()
	if err := c.writeEnvelope(ctx, envelope); err != nil {
		c.removePending(id)
		if ctx.Err() != nil {
			// The frame may already be on its way to the plugin.
			c.CancelRequest(id)
		}
		return err
	}
	select {
	case resultResponse, ok := <-channel:
		if !ok {
			if err := c.Err(); err != nil {
				return err
			}
			return ErrPluginExited
		}
		if resultResponse.err != nil {
			return resultResponse.err
		}
		if resultResponse.envelope.Error != nil {
			return resultResponse.envelope.Error
		}
		if result == nil || len(resultResponse.envelope.Payload) == 0 {
			return nil
		}
		if err := json.Unmarshal(resultResponse.envelope.Payload, result); err != nil {
			return fmt.Errorf("%w: decode %s result: %v", ErrInvalidProtocol, method, err)
		}
		return nil
	case <-ctx.Done():
		c.removePending(id)
		// Graceful cancellation first: killing the process group is an
		// escalation the supervisor performs only if the plugin stays wedged.
		c.CancelRequest(id)
		return ctx.Err()
	case <-c.done:
		if err := c.Err(); err != nil {
			return err
		}
		return ErrPluginExited
	}
}

func (c *Client) removePending(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// CancelRequest sends the best-effort request.cancel notification for an
// in-flight request. It never blocks the caller — a caller whose context was
// just cancelled must not be made to wait on the plugin again — and the write
// itself is bounded by the configured close timeout.
func (c *Client) CancelRequest(id string) {
	if strings.TrimSpace(id) == "" || isClosed(c.done) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), c.config.CloseTimeout)
		defer cancel()
		_ = c.writeCancel(ctx, id)
	}()
}

func (c *Client) writeCancel(ctx context.Context, id string) error {
	envelope, err := NewRequest(c.nextID(), MethodCancel, CancelParams{RequestID: id})
	if err != nil {
		return err
	}
	return c.writeEnvelope(ctx, envelope)
}

func (c *Client) Handshake(ctx context.Context, params HandshakeParams) (HandshakeResult, error) {
	if params.ProtocolVersion == "" {
		params.ProtocolVersion = ProtocolVersion
	}
	if err := params.Valid(); err != nil {
		return HandshakeResult{}, err
	}
	var result HandshakeResult
	if err := c.call(ctx, MethodHandshake, params, &result); err != nil {
		return HandshakeResult{}, fmt.Errorf("%w: %w", ErrHandshakeFailed, err)
	}
	if err := result.Valid(params.PluginID); err != nil {
		return HandshakeResult{}, err
	}
	return result, nil
}

func (c *Client) Health(ctx context.Context, params HealthParams) (HealthResult, error) {
	var result HealthResult
	if err := c.call(ctx, MethodHealth, params, &result); err != nil {
		return HealthResult{}, fmt.Errorf("%w: %w", ErrHealthFailed, err)
	}
	if err := result.Valid(); err != nil {
		return HealthResult{}, err
	}
	return result, nil
}

func (c *Client) InvokeTool(ctx context.Context, params ToolInvokeParams) (ToolInvokeResult, error) {
	if strings.TrimSpace(params.ToolID) == "" {
		return ToolInvokeResult{}, fmt.Errorf("%w: tool id is required", ErrInvalidProtocol)
	}
	if len(params.Arguments) == 0 {
		params.Arguments = json.RawMessage(`{}`)
	}
	if !json.Valid(params.Arguments) {
		return ToolInvokeResult{}, fmt.Errorf("%w: tool arguments are not valid JSON", ErrInvalidProtocol)
	}
	var result ToolInvokeResult
	if err := c.call(ctx, MethodToolCall, params, &result); err != nil {
		return ToolInvokeResult{}, err
	}
	return result, nil
}

func (c *Client) Shutdown(ctx context.Context) error {
	return c.call(ctx, MethodShutdown, struct{}{}, nil)
}

func (c *Client) Events() <-chan Envelope { return c.events }

// DroppedEvents reports how many plugin events were discarded because the host
// consumer could not keep up. It is a health signal, not a protocol error.
func (c *Client) DroppedEvents() uint64 { return c.droppedEvents.Load() }

// StderrTail returns the retained plugin stderr with secrets redacted. It is
// bounded by ClientConfig.MaxStderrBytes.
func (c *Client) StderrTail() string {
	if c == nil || c.stderr == nil {
		return ""
	}
	return c.stderr.Snapshot()
}

func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.processErr
}

func (c *Client) Close() error {
	return c.CloseContext(context.Background())
}

func (c *Client) CloseContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closing = true
		c.mu.Unlock()
		_ = c.stdin.Close()
	})
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		_ = killProcess(c.cmd)
		select {
		case <-c.done:
		case <-time.After(c.config.CloseTimeout):
		}
		return ctx.Err()
	case <-time.After(c.config.CloseTimeout):
		_ = killProcess(c.cmd)
		select {
		case <-c.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.config.CloseTimeout):
			return ErrPluginExited
		}
	}
}

func isClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

// stderrCapture keeps a bounded tail of the plugin's diagnostics. Raw bytes
// never leave the process: Snapshot is the only accessor and it redacts
// credential-shaped values first.
type stderrCapture struct {
	mu         sync.Mutex
	max        int
	buffer     []byte
	sink       io.Writer
	overflowed bool
}

func newStderrCapture(max int, sink io.Writer) *stderrCapture {
	if max <= 0 {
		max = DefaultMaxStderrBytes
	}
	if sink == io.Discard {
		sink = nil
	}
	return &stderrCapture{max: max, sink: sink}
}

func (s *stderrCapture) Write(data []byte) (int, error) {
	if s.sink != nil {
		_, _ = s.sink.Write(data)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(data) >= s.max {
		s.buffer = append(s.buffer[:0], data[len(data)-s.max:]...)
		s.overflowed = true
		return len(data), nil
	}
	if len(s.buffer)+len(data) > s.max {
		drop := len(s.buffer) + len(data) - s.max
		s.buffer = append(s.buffer[:0], s.buffer[drop:]...)
		s.overflowed = true
	}
	s.buffer = append(s.buffer, data...)
	return len(data), nil
}

func (s *stderrCapture) Snapshot() string {
	s.mu.Lock()
	raw := string(s.buffer)
	overflowed := s.overflowed
	s.mu.Unlock()
	redacted := redactDiagnostics(raw)
	if redacted == "" {
		return ""
	}
	if overflowed {
		return "…" + redacted
	}
	return redacted
}

// Redaction mirrors the discipline used for provider errors: a plugin can
// print anything at all to stderr, and that text ends up in the audit log and
// in the UI. Every match is replaced wholesale, keyword included, so a later
// keyword-based filter does not have to blank the entire diagnostic.
var (
	diagnosticsBearerPattern = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`)
	diagnosticsKeyPattern    = regexp.MustCompile(`(?i)\b(?:sk|rk)-[A-Za-z0-9_-]{8,}`)
	diagnosticsJWTPattern    = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}`)
	diagnosticsJSONPattern   = regexp.MustCompile(`(?i)"[A-Za-z0-9_.-]*(?:api[_-]?key|token|secret|password|passphrase|credential|authorization)[A-Za-z0-9_.-]*"\s*:\s*"[^"]*"`)
	diagnosticsAssignPattern = regexp.MustCompile(`(?i)\b[A-Za-z0-9_.-]*(?:api[_-]?key|apikey|token|secret|password|passwd|passphrase|credential|authorization)[A-Za-z0-9_.-]*\s*[:=]\s*\S+`)
)

func redactDiagnostics(value string) string {
	value = strings.ToValidUTF8(value, "")
	value = diagnosticsJSONPattern.ReplaceAllString(value, "[REDACTED]")
	value = diagnosticsBearerPattern.ReplaceAllString(value, "[REDACTED]")
	value = diagnosticsJWTPattern.ReplaceAllString(value, "[REDACTED]")
	value = diagnosticsKeyPattern.ReplaceAllString(value, "[REDACTED]")
	// The assignment sweep runs last so it also swallows the keyword in front
	// of a value one of the shape-based patterns has already replaced.
	value = diagnosticsAssignPattern.ReplaceAllString(value, "[REDACTED]")
	return strings.TrimSpace(value)
}
