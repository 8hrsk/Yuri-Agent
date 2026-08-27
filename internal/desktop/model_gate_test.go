package desktop

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

type gateTestBackend struct{}

func (gateTestBackend) Start(context.Context, agent.ModelRequest) (agent.ModelStream, error) {
	return gateTestStream{}, nil
}

type gateTestStream struct{}

func (gateTestStream) Recv(context.Context) (agent.ModelEvent, error) {
	return agent.ModelEvent{}, io.EOF
}
func (gateTestStream) Close() error { return nil }

func TestGatedBackendReleasesOnlyWhenStreamCloses(t *testing.T) {
	backend := gatedBackend{backend: gateTestBackend{}, turns: make(chan struct{}, 1)}
	request := agent.ModelRequest{Model: "test", Messages: []agent.Message{{Role: agent.RoleUser, Content: "test"}}}
	first, err := backend.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan agent.ModelStream, 1)
	go func() {
		stream, _ := backend.Start(context.Background(), request)
		started <- stream
	}()
	select {
	case <-started:
		t.Fatal("second turn started before first stream closed")
	case <-time.After(25 * time.Millisecond):
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case second := <-started:
		if second == nil {
			t.Fatal("second stream is nil")
		}
		_ = second.Close()
	case <-time.After(time.Second):
		t.Fatal("second turn did not start after release")
	}
}
