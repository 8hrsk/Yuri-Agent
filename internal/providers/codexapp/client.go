package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

var ErrClosed = errors.New("codex app server connection closed")

type Options struct {
	Binary           string
	WorkingDirectory string
	ClientInfo       ClientInfo
	MaxMessageBytes  int
}

type response struct {
	Result json.RawMessage
	Error  *rpcError
	Cause  error
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (value *rpcError) Error() string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("codex app server error %d: %s", value.Code, value.Message)
}

type wireMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type Client struct {
	stdin  io.WriteCloser
	stdout io.Reader
	output io.Closer
	cancel context.CancelFunc
	kill   func() error
	wait   func() error

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[string]chan response
	err     error

	events    chan Event
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	maxBytes  int
}

func Start(ctx context.Context, options Options) (*Client, error) {
	if ctx == nil {
		return nil, errors.New("start codex app server: nil context")
	}
	binary := options.Binary
	if binary == "" {
		binary = DefaultBinary
	}
	if !filepath.IsAbs(binary) {
		resolved, err := exec.LookPath(binary)
		if err != nil {
			return nil, fmt.Errorf("find Codex CLI: %w", err)
		}
		binary = resolved
	}
	lifetime, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(lifetime, binary, "app-server")
	if options.WorkingDirectory != "" {
		command.Dir = options.WorkingDirectory
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open Codex stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open Codex stdout: %w", err)
	}
	if err := command.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start Codex app server: %w", err)
	}
	client := newClient(stdin, stdout, options.MaxMessageBytes)
	client.cancel = cancel
	client.kill = command.Process.Kill
	client.wait = command.Wait
	clientInfo := options.ClientInfo
	if clientInfo.Name == "" {
		clientInfo = ClientInfo{Name: "yuri", Title: "Yuri", Version: "0.1.0"}
	}
	if err := client.Initialize(ctx, clientInfo); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func newClient(stdin io.WriteCloser, stdout io.Reader, maxBytes int) *Client {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxMessage
	}
	client := &Client{
		stdin: stdin, stdout: stdout, nextID: 1,
		pending: make(map[string]chan response), events: make(chan Event, 128),
		stop: make(chan struct{}), done: make(chan struct{}), maxBytes: maxBytes,
	}
	if closer, ok := stdout.(io.Closer); ok {
		client.output = closer
	}
	go client.readLoop()
	return client
}

func (client *Client) Events() <-chan Event { return client.events }

func (client *Client) Initialize(ctx context.Context, info ClientInfo) error {
	params := struct {
		ClientInfo ClientInfo `json:"clientInfo"`
	}{ClientInfo: info}
	if err := client.Request(ctx, "initialize", params, nil); err != nil {
		return fmt.Errorf("initialize Codex app server: %w", err)
	}
	if err := client.Notify("initialized", struct{}{}); err != nil {
		return fmt.Errorf("acknowledge Codex initialization: %w", err)
	}
	return nil
}

func (client *Client) Request(ctx context.Context, method string, params, target any) error {
	if ctx == nil {
		return errors.New("codex request: nil context")
	}
	if method == "" {
		return errors.New("codex request: method is required")
	}
	id, key, result := client.reserve()
	message := map[string]any{"id": id, "method": method}
	if params != nil {
		message["params"] = params
	}
	if err := client.send(message); err != nil {
		client.release(key)
		return err
	}
	select {
	case reply := <-result:
		if reply.Cause != nil {
			return reply.Cause
		}
		if reply.Error != nil {
			return reply.Error
		}
		if target == nil || len(reply.Result) == 0 || string(reply.Result) == "null" {
			return nil
		}
		if err := json.Unmarshal(reply.Result, target); err != nil {
			return fmt.Errorf("decode Codex response for %s: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		client.release(key)
		return ctx.Err()
	case <-client.done:
		return client.connectionError()
	case <-client.stop:
		return ErrClosed
	}
}

func (client *Client) Notify(method string, params any) error {
	if method == "" {
		return errors.New("codex notification: method is required")
	}
	message := map[string]any{"method": method}
	if params != nil {
		message["params"] = params
	}
	return client.send(message)
}

// Respond resolves an app-server initiated request such as an approval. The ID
// is treated as opaque JSON and is copied from Event.ID without conversion.
func (client *Client) Respond(id json.RawMessage, result any, requestError *rpcError) error {
	if len(id) == 0 || string(id) == "null" {
		return errors.New("codex response: request id is required")
	}
	var message map[string]any
	if err := json.Unmarshal(id, new(any)); err != nil {
		return errors.New("codex response: invalid request id")
	}
	message = map[string]any{"id": json.RawMessage(id)}
	if requestError != nil {
		message["error"] = requestError
	} else {
		message["result"] = result
	}
	return client.send(message)
}

func (client *Client) Close() error {
	client.closeOnce.Do(func() {
		close(client.stop)
		if client.cancel != nil {
			client.cancel()
		}
		_ = client.stdin.Close()
		if client.output != nil {
			_ = client.output.Close()
		}
		select {
		case <-client.done:
		case <-time.After(2 * time.Second):
			if client.kill != nil {
				_ = client.kill()
			}
			<-client.done
		}
	})
	return nil
}

func (client *Client) reserve() (int64, string, <-chan response) {
	client.mu.Lock()
	defer client.mu.Unlock()
	id := client.nextID
	client.nextID++
	key := strconv.FormatInt(id, 10)
	result := make(chan response, 1)
	client.pending[key] = result
	return id, key, result
}

func (client *Client) release(key string) {
	client.mu.Lock()
	delete(client.pending, key)
	client.mu.Unlock()
}

func (client *Client) send(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode Codex message: %w", err)
	}
	if len(data) > client.maxBytes {
		return fmt.Errorf("Codex message exceeds %d byte limit", client.maxBytes)
	}
	data = append(data, '\n')
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	select {
	case <-client.done:
		return client.connectionError()
	case <-client.stop:
		return ErrClosed
	default:
	}
	if _, err := client.stdin.Write(data); err != nil {
		return fmt.Errorf("write Codex message: %w", err)
	}
	return nil
}

func (client *Client) readLoop() {
	var terminal error
	defer func() {
		if client.wait != nil {
			if err := client.wait(); terminal == nil && err != nil {
				terminal = err
			}
		}
		client.finish(terminal)
	}()
	scanner := bufio.NewScanner(client.stdout)
	scanner.Buffer(make([]byte, 64*1024), client.maxBytes)
	for scanner.Scan() {
		var message wireMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			terminal = fmt.Errorf("decode Codex message: %w", err)
			return
		}
		if message.Method == "" && len(message.ID) > 0 {
			key := string(message.ID)
			client.mu.Lock()
			pending := client.pending[key]
			delete(client.pending, key)
			client.mu.Unlock()
			if pending != nil {
				pending <- response{Result: message.Result, Error: message.Error}
			}
			continue
		}
		if message.Method == "" {
			continue
		}
		event := Event{ID: message.ID, Method: message.Method, Params: message.Params}
		select {
		case client.events <- event:
		case <-client.stop:
			return
		}
	}
	if err := scanner.Err(); err != nil {
		terminal = fmt.Errorf("read Codex message: %w", err)
	}
}

func (client *Client) finish(cause error) {
	client.mu.Lock()
	if cause == nil {
		cause = ErrClosed
	}
	client.err = cause
	pending := client.pending
	client.pending = make(map[string]chan response)
	client.mu.Unlock()
	for _, result := range pending {
		result <- response{Cause: cause}
	}
	close(client.events)
	close(client.done)
}

func (client *Client) connectionError() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.err != nil {
		return client.err
	}
	return ErrClosed
}
