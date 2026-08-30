package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

// TestThreadSubscriptionRoutesEventsAndDeclinesOnClose pins the demultiplexing
// contract of the single app-server connection: a thread's events reach only the
// turn that owns that thread, connection-scoped events stay on the shared feed,
// and unsubscribing declines a request nobody will answer.
func TestThreadSubscriptionRoutesEventsAndDeclinesOnClose(t *testing.T) {
	serverInput, clientInput := io.Pipe()
	clientOutput, serverOutput := io.Pipe()
	client := newClient(clientInput, clientOutput, 0)
	t.Cleanup(func() {
		_ = serverInput.Close()
		_ = serverOutput.Close()
		_ = client.Close()
	})

	declined := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(serverInput)
		for scanner.Scan() {
			var message map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
				return
			}
			if result, ok := message["result"].(map[string]any); ok && result["decision"] == "decline" {
				declined <- fmt.Sprint(message["id"])
			}
		}
	}()

	first := client.SubscribeThread("thread-a")
	second := client.SubscribeThread("thread-b")

	lines := []string{
		`{"method":"item/agentMessage/delta","params":{"threadId":"thread-a","turnId":"turn-a","delta":"a1"}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"thread-b","turnId":"turn-b","delta":"b1"}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"thread-a","turnId":"turn-a","delta":"a2"}}`,
		`{"method":"account/updated","params":{"plan":"plus"}}`,
		`{"id":77,"method":"item/tool/call","params":{"threadId":"thread-a","turnId":"turn-a","callId":"c","tool":"x","arguments":{}}}`,
		// The read loop is sequential, so observing this last line proves the tool
		// request above has already been routed into the first thread's feed.
		`{"method":"item/agentMessage/delta","params":{"threadId":"thread-b","turnId":"turn-b","delta":"b2"}}`,
	}
	for _, line := range lines {
		if _, err := io.WriteString(serverOutput, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}

	if got := receiveDelta(t, first.Events()); got != "a1" {
		t.Fatalf("first thread event = %q", got)
	}
	if got := receiveDelta(t, first.Events()); got != "a2" {
		t.Fatalf("first thread lost its own event to another feed: %q", got)
	}
	if got := receiveDelta(t, second.Events()); got != "b1" {
		t.Fatalf("second thread event = %q", got)
	}
	if got := receiveDelta(t, second.Events()); got != "b2" {
		t.Fatalf("second thread event = %q", got)
	}
	select {
	case shared := <-client.Events():
		if shared.Method != "account/updated" {
			t.Fatalf("shared feed received a thread-scoped event: %q", shared.Method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("connection-scoped event never reached the shared feed")
	}

	// The queued dynamic tool request must be declined when the turn goes away.
	first.Close()
	select {
	case id := <-declined:
		if id != "77" {
			t.Fatalf("declined request id = %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a queued server request was not declined when the thread unsubscribed")
	}

	// After unsubscribing, the thread's events fall back to the shared feed.
	if _, err := io.WriteString(serverOutput, `{"method":"item/agentMessage/delta","params":{"threadId":"thread-a","turnId":"turn-a","delta":"a3"}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case shared := <-client.Events():
		if shared.Method != "item/agentMessage/delta" {
			t.Fatalf("unrouted event = %q", shared.Method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event of an unsubscribed thread was not routed to the shared feed")
	}
	second.Close()
}

func receiveDelta(t *testing.T, events <-chan Event) string {
	t.Helper()
	select {
	case event := <-events:
		var params struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal(event.Params, &params); err != nil {
			t.Fatal(err)
		}
		return params.Delta
	case <-time.After(2 * time.Second):
		t.Fatal("no event arrived on the thread feed")
		return ""
	}
}

// TestConcurrentTurnsReceiveOnlyTheirOwnEvents runs two independent turns at the
// same time over one app-server connection — the situation the removed provider
// turn gate used to prevent and that nested runs now create for real.
func TestConcurrentTurnsReceiveOnlyTheirOwnEvents(t *testing.T) {
	const deltasPerTurn = 8
	script := func(server *appServerHarness, threadID, turnID string) {
		for index := 0; index < deltasPerTurn; index++ {
			server.delta(threadID, turnID, fmt.Sprintf("%s:%d|", threadID, index))
			time.Sleep(time.Millisecond)
		}
		server.completeTurn(threadID, turnID)
	}
	harness := newAppServerHarness(t, script)
	backend, err := NewBackend(harness.client, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request := agent.ModelRequest{
		Model: "codex-default", Messages: []agent.Message{{Role: agent.RoleUser, Content: "запрос"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wait sync.WaitGroup
	var failures atomic.Int64
	texts := make([]string, 2)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(slot int) {
			defer wait.Done()
			stream, startErr := backend.Start(ctx, request)
			if startErr != nil {
				t.Errorf("start turn %d: %v", slot, startErr)
				failures.Add(1)
				return
			}
			defer func() { _ = stream.Close() }()
			var builder strings.Builder
			for {
				event, recvErr := stream.Recv(ctx)
				if recvErr != nil {
					t.Errorf("turn %d recv: %v", slot, recvErr)
					failures.Add(1)
					return
				}
				if event.Type == agent.ModelEventCompleted {
					texts[slot] = builder.String()
					return
				}
				builder.WriteString(event.Delta)
			}
		}(index)
	}
	wait.Wait()
	if failures.Load() > 0 {
		t.FailNow()
	}
	for slot, text := range texts {
		if strings.Count(text, "|") != deltasPerTurn {
			t.Fatalf("turn %d received %d deltas: %q", slot, strings.Count(text, "|"), text)
		}
		threadID := strings.SplitN(text, ":", 2)[0]
		other := "thread-1"
		if threadID == "thread-1" {
			other = "thread-2"
		}
		if strings.Contains(text, other) {
			t.Fatalf("turn %d consumed events of %s: %q", slot, other, text)
		}
	}
}
