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

func TestThreadStartParamsIncludesDynamicToolsInAppServerShape(t *testing.T) {
	params := threadStartParams(ThreadOptions{
		DynamicTools: []DynamicToolSpec{{
			Type:        "function",
			Name:        "filesystem_read_d8f55b3922",
			Description: "Read an allowed local file",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		}},
	})
	tools, ok := params["dynamicTools"].([]DynamicToolSpec)
	if !ok || len(tools) != 1 {
		t.Fatalf("dynamicTools = %#v", params["dynamicTools"])
	}
	if tools[0].Type != "function" || tools[0].Name != "filesystem_read_d8f55b3922" {
		t.Fatalf("dynamic tool = %#v", tools[0])
	}
	if !json.Valid(tools[0].InputSchema) {
		t.Fatalf("dynamic tool input schema is invalid: %s", tools[0].InputSchema)
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	encodedTool, ok := decoded["dynamicTools"].([]any)
	if !ok || len(encodedTool) != 1 {
		t.Fatalf("encoded dynamicTools = %#v", decoded["dynamicTools"])
	}
	toolObject, ok := encodedTool[0].(map[string]any)
	if !ok || toolObject["inputSchema"] == nil {
		t.Fatalf("encoded dynamic tool does not use inputSchema: %#v", encodedTool[0])
	}
	if _, legacy := toolObject["input_schema"]; legacy {
		t.Fatalf("encoded dynamic tool contains provider-neutral input_schema: %#v", toolObject)
	}
}

func TestTurnStartParamsIncludesNativeImageInput(t *testing.T) {
	params := turnStartParams(TurnOptions{ThreadID: "thread-test", Text: "prompt", Images: []string{"data:image/png;base64,iVBORw0KGgo="}})
	input, ok := params["input"].([]map[string]any)
	if !ok || len(input) != 2 {
		t.Fatalf("turn input = %#v", params["input"])
	}
	if input[0]["type"] != "text" || input[1]["type"] != "image" || input[1]["url"] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("turn input = %#v", input)
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
