package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultMaxMessageBytes = 1 << 20
	DefaultCloseTimeout    = 2 * time.Second
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
}

type response struct {
	envelope Envelope
	err      error
}

// Client is a concurrency-safe stdio JSON Lines client. It owns the child
// process and never exposes its stdout pipe to callers.
type Client struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	config     ClientConfig
	writeMu    sync.Mutex
	mu         sync.Mutex
	pending    map[string]chan response
	events     chan Envelope
	done       chan struct{}
	doneOnce   sync.Once
	eventsOnce sync.Once
	closeOnce  sync.Once
	closing    bool
	processErr error
	sequence   atomic.Uint64
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
	if config.Stderr == nil {
		config.Stderr = io.Discard
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
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: stdin pipe: %v", ErrPluginExited, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("%w: stdout pipe: %v", ErrPluginExited, err)
	}
	cmd.Stderr = config.Stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("%w: start process: %v", ErrPluginExited, err)
	}

	client := &Client{
		cmd: cmd, stdin: stdin, config: config,
		pending: make(map[string]chan response), events: make(chan Envelope, 64), done: make(chan struct{}),
	}
	go client.readStdout(stdout)
	go client.waitProcess()
	if done := ctx.Done(); done != nil {
		go func() {
			select {
			case <-done:
				_ = client.Close()
			case <-client.done:
			}
		}()
	}
	return client, nil
}

func (c *Client) readStdout(reader io.Reader) {
	defer c.eventsOnce.Do(func() { close(c.events) })
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
		if envelope.Type == MessageHandshakeResult || envelope.Type == MessageHealthResult || envelope.Type == MessageToolResult || envelope.Type == MessageShutdownResult || envelope.Type == MessageError {
			c.deliverResponse(envelope)
			continue
		}
		select {
		case c.events <- envelope:
		case <-c.done:
			return
		default:
			c.finish(fmt.Errorf("%w: event buffer is full", ErrInvalidProtocol))
			return
		}
	}
	if err := scanner.Err(); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "token too long") {
			c.finish(fmt.Errorf("%w: JSONL line is too long", ErrMessageTooLarge))
			return
		}
		c.finish(fmt.Errorf("%w: read stdout: %v", ErrPluginExited, err))
		return
	}
	c.finish(nil)
}

func (c *Client) deliverResponse(envelope Envelope) {
	c.mu.Lock()
	pending := c.pending[envelope.ReplyTo]
	if pending != nil {
		delete(c.pending, envelope.ReplyTo)
	}
	c.mu.Unlock()
	if pending != nil {
		pending <- response{envelope: envelope}
	}
}

func (c *Client) waitProcess() {
	err := c.cmd.Wait()
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
		c.mu.Unlock()
	})
}

func (c *Client) nextID() string {
	return fmt.Sprintf("req-%d", c.sequence.Add(1))
}

func (c *Client) writeEnvelope(envelope Envelope) error {
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
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	closed := isClosed(c.done)
	err = c.processErr
	c.mu.Unlock()
	if closed {
		if err == nil {
			err = ErrPluginExited
		}
		return err
	}
	if _, err := c.stdin.Write(data); err != nil {
		wrapped := fmt.Errorf("%w: write stdin: %v", ErrPluginExited, err)
		c.finish(wrapped)
		return wrapped
	}
	return nil
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
	if err := c.writeEnvelope(envelope); err != nil {
		c.removePending(id)
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

func (c *Client) writeCancel(id string) error {
	envelope, err := NewEvent(MethodCancel, CancelParams{RequestID: id})
	if err != nil {
		return err
	}
	return c.writeEnvelope(envelope)
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
