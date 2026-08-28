package codexapp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCurrentAppServerThreadAndTurnPoliciesAreFailClosed(t *testing.T) {
	roots := []string{"/tmp/yuri-data", "/tmp/yuri-documents"}
	thread := threadStartParams(ThreadOptions{
		Model: "gpt-test", CWD: roots[0], ReadableRoots: roots,
	})
	if thread["approvalPolicy"] != "never" {
		t.Fatalf("thread approvalPolicy = %#v, want never", thread["approvalPolicy"])
	}
	if thread["sandbox"] != "read-only" {
		t.Fatalf("thread sandbox = %#v, want read-only", thread["sandbox"])
	}
	assertRuntimeRoots(t, thread, roots)

	turn := turnStartParams(TurnOptions{
		ThreadID: "thread-test", Text: "hello", CWD: roots[0], Model: "gpt-test", ReadableRoots: roots,
	})
	if turn["approvalPolicy"] != "never" {
		t.Fatalf("turn approvalPolicy = %#v, want never", turn["approvalPolicy"])
	}
	sandbox, ok := turn["sandboxPolicy"].(map[string]any)
	if !ok || sandbox["type"] != "readOnly" || sandbox["networkAccess"] != false {
		t.Fatalf("turn sandboxPolicy = %#v", turn["sandboxPolicy"])
	}
	if _, exists := sandbox["access"]; exists {
		t.Fatalf("turn sandboxPolicy contains removed access field: %#v", sandbox)
	}
	assertRuntimeRoots(t, turn, roots)

	encoded, err := json.Marshal(map[string]any{"thread": thread, "turn": turn})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "unlessTrusted") || strings.Contains(string(encoded), `"sandbox":"readOnly"`) {
		t.Fatalf("request contains a legacy app-server variant: %s", encoded)
	}
}

func assertRuntimeRoots(t *testing.T, params map[string]any, want []string) {
	t.Helper()
	got, ok := params["runtimeWorkspaceRoots"].([]string)
	if !ok || len(got) != len(want) {
		t.Fatalf("runtimeWorkspaceRoots = %#v, want %#v", params["runtimeWorkspaceRoots"], want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("runtimeWorkspaceRoots = %#v, want %#v", got, want)
		}
	}
}
