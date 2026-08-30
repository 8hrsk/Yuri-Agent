package desktop

import (
	"context"
	"sync"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

// gatedBackend serializes turns that share the Codex app-server event channel.
// A background memory review therefore cannot consume foreground events.
type gatedBackend struct {
	backend agent.ModelBackend
	turns   chan struct{}
}

func (backend gatedBackend) Start(ctx context.Context, request agent.ModelRequest) (agent.ModelStream, error) {
	if backend.turns == nil {
		return backend.backend.Start(ctx, request)
	}
	release, err := modelTurnLeaseFrom(ctx).acquire(ctx, backend.turns)
	if err != nil {
		return nil, err
	}
	stream, err := backend.backend.Start(ctx, request)
	if err != nil {
		release()
		return nil, err
	}
	return newGatedStream(stream, release), nil
}

// newGatedStream wraps a stream without hiding the optional capabilities the
// runtime discovers through type assertions.
//
// Embedding the agent.ModelStream *interface* silently erased them: a Codex
// app-server stream implements agent.InteractiveToolStream, but the wrapper
// exposed only Recv/Close, so runtime.Run took the non-interactive path where
// Close() runs before tools execute. A Codex dynamic tool call could then never
// be answered and the run hung until its duration budget expired.
//
// The wrapper is therefore chosen per stream: a distinct type that forwards
// RespondToolResult is returned only when the wrapped stream really implements
// it, so a plain transport still fails the assertion instead of acquiring a
// method that cannot work. Static assertions below keep both shapes honest, and
// the release/Close bookkeeping stays in one place because the interactive
// wrapper embeds the plain one.
func newGatedStream(stream agent.ModelStream, release func()) agent.ModelStream {
	gated := &gatedStream{ModelStream: stream, release: release}
	if interactive, ok := stream.(agent.InteractiveToolStream); ok {
		return &gatedInteractiveStream{gatedStream: gated, interactive: interactive}
	}
	return gated
}

type gatedStream struct {
	agent.ModelStream
	release func()
	once    sync.Once
}

func (stream *gatedStream) Close() error {
	err := stream.ModelStream.Close()
	stream.once.Do(stream.release)
	return err
}

// gatedInteractiveStream preserves the mid-turn tool-result channel of the
// wrapped transport. Gating only decides when a turn may start; it must not
// change what the transport can do once it has started.
type gatedInteractiveStream struct {
	*gatedStream
	interactive agent.InteractiveToolStream
}

func (stream *gatedInteractiveStream) RespondToolResult(ctx context.Context, callID string, result agent.ToolResult) error {
	return stream.interactive.RespondToolResult(ctx, callID, result)
}

// modelTurnLease makes the shared turn gate reentrant for one run subtree.
//
// A nested run — an anonymous subagent from agent.delegate, or a peer dialogue
// turn — executes as a tool call inside the parent run, so it inherits the
// parent run context. Without reentrancy the child would block on a slot that
// only the parent can release, and the parent is itself blocked waiting for
// the child's tool result: a deterministic self-deadlock whose only exit is
// the run duration budget. The lease counts nested turns instead, so the
// subtree that already owns the slot never queues behind itself while the gate
// keeps serializing unrelated runs (a background memory review, a scheduled
// job) exactly as before.
type modelTurnLease struct {
	mu    sync.Mutex
	depth int
}

type modelTurnLeaseKey struct{}

// withModelTurnLease scopes one reentrant gate lease to a run context. Every
// context derived from it — including the contexts handed to tools and to the
// nested runs those tools start — shares the same lease.
func withModelTurnLease(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if modelTurnLeaseFrom(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, modelTurnLeaseKey{}, &modelTurnLease{})
}

func modelTurnLeaseFrom(ctx context.Context) *modelTurnLease {
	if ctx == nil {
		return nil
	}
	lease, _ := ctx.Value(modelTurnLeaseKey{}).(*modelTurnLease)
	return lease
}

// acquire reserves the gate slot unless this run subtree already holds it. The
// returned release is idempotent and only returns the slot when it was the
// acquisition that took it.
func (lease *modelTurnLease) acquire(ctx context.Context, turns chan struct{}) (func(), error) {
	if lease == nil {
		select {
		case turns <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		var once sync.Once
		return func() { once.Do(func() { <-turns }) }, nil
	}
	lease.mu.Lock()
	nested := lease.depth > 0
	if nested {
		lease.depth++
	}
	lease.mu.Unlock()
	if nested {
		var once sync.Once
		return func() { once.Do(lease.leave) }, nil
	}
	select {
	case turns <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	lease.mu.Lock()
	lease.depth++
	lease.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			lease.leave()
			<-turns
		})
	}, nil
}

func (lease *modelTurnLease) leave() {
	lease.mu.Lock()
	if lease.depth > 0 {
		lease.depth--
	}
	lease.mu.Unlock()
}

var _ agent.ModelBackend = gatedBackend{}
var _ agent.ModelStream = (*gatedStream)(nil)
var _ agent.InteractiveToolStream = (*gatedInteractiveStream)(nil)
