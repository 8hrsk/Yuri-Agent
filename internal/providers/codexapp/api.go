package codexapp

import (
	"context"
	"encoding/json"
)

const (
	appServerApprovalPolicy = "never"
	appServerThreadSandbox  = "read-only"
)

func (client *Client) ReadAccount(ctx context.Context, refresh bool) (AccountReadResult, error) {
	var result AccountReadResult
	err := client.Request(ctx, "account/read", struct {
		RefreshToken bool `json:"refreshToken"`
	}{RefreshToken: refresh}, &result)
	return result, err
}

func (client *Client) StartChatGPTLogin(ctx context.Context) (LoginResult, error) {
	var result LoginResult
	err := client.Request(ctx, "account/login/start", map[string]any{
		"type":                      "chatgpt",
		"useHostedLoginSuccessPage": true,
		"appBrand":                  "chatgpt",
	}, &result)
	return result, err
}

func (client *Client) StartDeviceCodeLogin(ctx context.Context) (LoginResult, error) {
	var result LoginResult
	err := client.Request(ctx, "account/login/start", map[string]any{
		"type": "chatgptDeviceCode",
	}, &result)
	return result, err
}

func (client *Client) CancelLogin(ctx context.Context, loginID string) error {
	return client.Request(ctx, "account/login/cancel", map[string]string{"loginId": loginID}, nil)
}

func (client *Client) Logout(ctx context.Context) error {
	return client.Request(ctx, "account/logout", nil, nil)
}

func (client *Client) ReadRateLimits(ctx context.Context) (RateLimitsResult, error) {
	var result RateLimitsResult
	err := client.Request(ctx, "account/rateLimits/read", nil, &result)
	return result, err
}

func (client *Client) ListModels(ctx context.Context) ([]Model, error) {
	models := make([]Model, 0, 16)
	cursor := ""
	for page := 0; page < 10; page++ {
		params := map[string]any{"includeHidden": false, "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result ModelListResult
		if err := client.Request(ctx, "model/list", params, &result); err != nil {
			return nil, err
		}
		models = append(models, result.Data...)
		if result.NextCursor == "" || result.NextCursor == cursor {
			break
		}
		cursor = result.NextCursor
	}
	return models, nil
}

func (client *Client) StartThread(ctx context.Context, model string) (Thread, error) {
	return client.StartThreadWithOptions(ctx, ThreadOptions{Model: model})
}

type ThreadOptions struct {
	Model         string
	CWD           string
	ReadableRoots []string
	DynamicTools  []DynamicToolSpec
}

// DynamicToolSpec is the Codex app-server representation of a Yuri tool.
// Unlike the Responses/Chat Completions schema, the app-server protocol uses
// camelCase inputSchema and accepts these tools on thread/start. The schema is
// kept as raw JSON so the provider does not reinterpret or weaken the
// provider-neutral tool contract.
type DynamicToolSpec struct {
	Type         string          `json:"type"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	DeferLoading bool            `json:"deferLoading,omitempty"`
}

func (client *Client) StartThreadWithOptions(ctx context.Context, options ThreadOptions) (Thread, error) {
	params := threadStartParams(options)
	var result struct {
		Thread Thread `json:"thread"`
	}
	err := client.Request(ctx, "thread/start", params, &result)
	return result.Thread, err
}

func threadStartParams(options ThreadOptions) map[string]any {
	params := map[string]any{
		"approvalPolicy": appServerApprovalPolicy,
		"sandbox":        appServerThreadSandbox,
		"serviceName":    "yuri",
	}
	if options.Model != "" {
		params["model"] = options.Model
	}
	if options.CWD != "" {
		params["cwd"] = options.CWD
	}
	if len(options.ReadableRoots) > 0 {
		params["runtimeWorkspaceRoots"] = append([]string(nil), options.ReadableRoots...)
	}
	if len(options.DynamicTools) > 0 {
		params["dynamicTools"] = append([]DynamicToolSpec(nil), options.DynamicTools...)
	}
	return params
}

func (client *Client) StartTurn(ctx context.Context, threadID, text string) (Turn, error) {
	return client.StartTurnWithOptions(ctx, TurnOptions{ThreadID: threadID, Text: text})
}

type TurnOptions struct {
	ThreadID      string
	Text          string
	CWD           string
	Model         string
	ReadableRoots []string
}

func (client *Client) StartTurnWithOptions(ctx context.Context, options TurnOptions) (Turn, error) {
	params := turnStartParams(options)
	var result struct {
		Turn Turn `json:"turn"`
	}
	err := client.Request(ctx, "turn/start", params, &result)
	return result.Turn, err
}

func turnStartParams(options TurnOptions) map[string]any {
	params := map[string]any{
		"threadId":       options.ThreadID,
		"input":          []map[string]string{{"type": "text", "text": options.Text}},
		"approvalPolicy": appServerApprovalPolicy,
		"sandboxPolicy": map[string]any{
			"type":          "readOnly",
			"networkAccess": false,
		},
	}
	if options.CWD != "" {
		params["cwd"] = options.CWD
	}
	if options.Model != "" {
		params["model"] = options.Model
	}
	if len(options.ReadableRoots) > 0 {
		params["runtimeWorkspaceRoots"] = append([]string(nil), options.ReadableRoots...)
	}
	return params
}

func (client *Client) InterruptTurn(ctx context.Context, threadID, turnID string) error {
	return client.Request(ctx, "turn/interrupt", map[string]string{
		"threadId": threadID,
		"turnId":   turnID,
	}, nil)
}

func DecodeEventParams[T any](event Event) (T, error) {
	var value T
	err := json.Unmarshal(event.Params, &value)
	return value, err
}
