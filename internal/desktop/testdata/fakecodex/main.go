package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type request struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

func main() {
	if len(os.Args) != 2 || os.Args[1] != "app-server" {
		_, _ = fmt.Fprintln(os.Stderr, "expected app-server mode")
		os.Exit(2)
	}
	marker := os.Getenv("YURI_TEST_CODEX_MARKER")
	token := os.Getenv("YURI_TEST_CODEX_TOKEN")
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var message request
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			os.Exit(3)
		}
		appendMethod(marker, message.Method)
		if len(message.ID) == 0 {
			continue
		}
		var result any = map[string]any{}
		switch message.Method {
		case "account/login/start":
			result = map[string]any{
				"type": "chatgptDeviceCode", "loginId": "login-fake-codex",
				"verificationUrl": "https://example.invalid/device", "userCode": "YURI-CODE",
				"accessToken": token,
			}
		case "account/read":
			result = map[string]any{
				"account": map[string]any{
					"type": "chatgpt", "email": "owner@example.invalid", "planType": "plus",
					"accessToken": token,
				},
				"requiresOpenaiAuth": true,
			}
		case "account/rateLimits/read":
			result = map[string]any{
				"rateLimits": map[string]any{
					"limitId": "codex", "planType": "plus",
					"primary": map[string]any{"usedPercent": 25, "windowDurationMins": 300, "resetsAt": 2000000000},
				},
				"refreshToken": token,
			}
		case "model/list":
			result = map[string]any{"data": []map[string]any{
				{
					"id": "model-default", "model": "gpt-test-default", "displayName": "GPT Test Default",
					"description": "Default fake Codex model", "hidden": false, "isDefault": true,
					"defaultReasoningEffort": "medium", "supportedReasoningEfforts": []any{},
				},
			}}
		}
		if err := encoder.Encode(map[string]any{"id": message.ID, "result": result}); err != nil {
			os.Exit(4)
		}
	}
	if err := scanner.Err(); err != nil {
		os.Exit(5)
	}
}

func appendMethod(path, method string) {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(method) == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(file, method)
	_ = file.Close()
}
