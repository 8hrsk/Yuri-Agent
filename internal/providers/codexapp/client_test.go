package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInitializeAndAccountFlow(t *testing.T) {
	serverInput, clientInput := io.Pipe()
	clientOutput, serverOutput := io.Pipe()
	client := newClient(clientInput, clientOutput, 0)
	t.Cleanup(func() {
		_ = serverInput.Close()
		_ = serverOutput.Close()
		_ = client.Close()
	})

	serverDone := make(chan error, 1)
	var logoutSeen atomic.Bool
	var experimentalAPISeen atomic.Bool
	go func() {
		scanner := bufio.NewScanner(serverInput)
		for scanner.Scan() {
			var message map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
				serverDone <- err
				return
			}
			method, _ := message["method"].(string)
			if method == "initialize" {
				params, _ := message["params"].(map[string]any)
				capabilities, _ := params["capabilities"].(map[string]any)
				experimentalAPISeen.Store(capabilities["experimentalApi"] == true)
			}
			id, hasID := message["id"]
			if !hasID {
				continue
			}
			var result any = map[string]any{}
			switch method {
			case "account/read":
				result = map[string]any{
					"account":            map[string]any{"type": "chatgpt", "email": "owner@example.com", "planType": "plus"},
					"requiresOpenaiAuth": true,
				}
			case "account/rateLimits/read":
				result = map[string]any{"rateLimits": map[string]any{
					"limitId": "codex", "primary": map[string]any{"usedPercent": 25, "windowDurationMins": 300, "resetsAt": 2000000000},
				}}
			case "account/logout":
				logoutSeen.Store(true)
			}
			encoded, _ := json.Marshal(map[string]any{"id": id, "result": result})
			if _, err := serverOutput.Write(append(encoded, '\n')); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- scanner.Err()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Initialize(ctx, ClientInfo{Name: "yuri", Title: "Yuri", Version: "test"}); err != nil {
		t.Fatal(err)
	}
	if !experimentalAPISeen.Load() {
		t.Fatal("initialize did not opt into experimental API required by runtimeWorkspaceRoots")
	}
	account, err := client.ReadAccount(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if account.Account == nil || account.Account.PlanType == nil || *account.Account.PlanType != "plus" {
		t.Fatalf("unexpected account: %#v", account)
	}
	limits, err := client.ReadRateLimits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if limits.RateLimits == nil || limits.RateLimits.Primary == nil || limits.RateLimits.Primary.UsedPercent != 25 {
		t.Fatalf("unexpected limits: %#v", limits)
	}
	if err := client.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if !logoutSeen.Load() {
		t.Fatal("account/logout was not sent")
	}
}

func TestReceivesNotificationAndServerRequest(t *testing.T) {
	_, clientInput := io.Pipe()
	clientOutput, serverOutput := io.Pipe()
	client := newClient(clientInput, clientOutput, 0)
	t.Cleanup(func() {
		_ = serverOutput.Close()
		_ = client.Close()
	})

	_, _ = io.WriteString(serverOutput, "{\"method\":\"item/agentMessage/delta\",\"params\":{\"delta\":\"Привет\"}}\n")
	_, _ = io.WriteString(serverOutput, "{\"id\":99,\"method\":\"item/commandExecution/requestApproval\",\"params\":{\"reason\":\"test\"}}\n")

	first := <-client.Events()
	second := <-client.Events()
	if first.Method != "item/agentMessage/delta" || second.Method != "item/commandExecution/requestApproval" {
		t.Fatalf("unexpected events: %q, %q", first.Method, second.Method)
	}
	if !second.IsServerRequest() || string(second.ID) != "99" {
		t.Fatalf("expected opaque request id, got %s", second.ID)
	}
}

func TestRejectsOversizedMessageAndRedactsProtocolErrorData(t *testing.T) {
	_, clientInput := io.Pipe()
	clientOutput, _ := io.Pipe()
	client := newClient(clientInput, clientOutput, 32)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Notify("oversized", strings.Repeat("secret", 20)); err == nil {
		t.Fatal("expected size limit error")
	}
	err := (&rpcError{Code: -1, Message: "denied", Data: json.RawMessage(`{"token":"secret"}`)}).Error()
	if strings.Contains(err, "secret") {
		t.Fatalf("protocol error leaked data: %s", err)
	}
}

// TestReadLoopSurvivesUnconsumedNotifications pins the M-18 fix: between turns
// nothing drains the shared event channel, and a blocking send would stall the
// single read loop that also resolves RPC replies.
func TestReadLoopSurvivesUnconsumedNotifications(t *testing.T) {
	serverInput, clientInput := io.Pipe()
	clientOutput, serverOutput := io.Pipe()
	client := newClient(clientInput, clientOutput, 0)
	t.Cleanup(func() {
		_ = serverInput.Close()
		_ = serverOutput.Close()
		_ = client.Close()
	})

	var declines atomic.Int64
	go func() {
		scanner := bufio.NewScanner(serverInput)
		scanner.Buffer(make([]byte, 64*1024), 1<<20)
		for scanner.Scan() {
			var message map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
				return
			}
			if result, ok := message["result"].(map[string]any); ok && result["decision"] == "decline" {
				declines.Add(1)
				continue
			}
			method, _ := message["method"].(string)
			if _, hasID := message["id"]; !hasID || method == "" {
				continue
			}
			encoded, _ := json.Marshal(map[string]any{"id": message["id"], "result": map[string]any{}})
			if _, err := serverOutput.Write(append(encoded, '\n')); err != nil {
				return
			}
		}
	}()

	// Far more events than the 128-slot buffer, with no consumer attached.
	const flood = 512
	flooded := make(chan error, 1)
	go func() {
		for index := 0; index < flood; index++ {
			line := "{\"method\":\"item/agentMessage/delta\",\"params\":{\"delta\":\"x\"}}\n"
			if index%2 == 0 {
				line = "{\"id\":" + strconv.Itoa(1000+index) + ",\"method\":\"item/commandExecution/requestApproval\",\"params\":{}}\n"
			}
			if _, err := io.WriteString(serverOutput, line); err != nil {
				flooded <- err
				return
			}
		}
		flooded <- nil
	}()
	select {
	case err := <-flooded:
		if err != nil {
			t.Fatalf("flood write: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read loop blocked on an unconsumed event channel")
	}

	// The read loop must still be parsing responses after the flood.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Request(ctx, "account/read", nil, nil); err != nil {
		t.Fatalf("request after unconsumed flood: %v", err)
	}
	if dropped := client.DroppedEvents(); dropped == 0 {
		t.Fatal("expected dropped notifications to be reported")
	}
	// Dropped server-initiated requests are declined so the app server is never
	// left waiting for a response nobody will produce.
	deadline := time.Now().Add(2 * time.Second)
	for declines.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if declines.Load() == 0 {
		t.Fatal("dropped server requests were not declined")
	}
}
