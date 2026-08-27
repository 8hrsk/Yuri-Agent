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
	select {
	case backend.turns <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	stream, err := backend.backend.Start(ctx, request)
	if err != nil {
		<-backend.turns
		return nil, err
	}
	return &gatedStream{ModelStream: stream, release: func() { <-backend.turns }}, nil
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

var _ agent.ModelBackend = gatedBackend{}
