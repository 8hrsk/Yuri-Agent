package googleaistudio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

// TokenCount is Google's pre-inference countTokens result. It is an input
// count; completed generation accounting still comes from stream usage.
type TokenCount struct {
	TotalTokens             int64
	CachedContentTokenCount int64
	ToolUsePromptTokenCount int64
	ThoughtsTokenCount      int64
}

type nativeTokenCountResponse struct {
	TotalTokens             int64 `json:"totalTokens"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	ToolUsePromptTokenCount int64 `json:"toolUsePromptTokenCount"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
}

type nativeCountRequest struct {
	Contents          []nativeContent `json:"contents"`
	SystemInstruction *nativeContent  `json:"systemInstruction,omitempty"`
	Tools             []nativeTool    `json:"tools,omitempty"`
}

type nativeContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []nativePart `json:"parts"`
}

type nativePart struct {
	Text         string              `json:"text,omitempty"`
	InlineData   *nativeData         `json:"inlineData,omitempty"`
	FunctionCall *nativeFunctionCall `json:"functionCall,omitempty"`
}

type nativeData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type nativeFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type nativeTool struct {
	FunctionDeclarations []nativeFunctionDeclaration `json:"functionDeclarations"`
}

type nativeFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// CountTokens maps Yuri's final request to Gemini's native countTokens
// endpoint. System/developer messages and tool-result messages are represented
// conservatively as text because the provider-neutral ToolResult does not keep
// the original function name. The returned count is still preferable to a
// local tokenizer estimate near a quota boundary, but not a statement that
// the subsequent compatibility request is byte-for-byte identical.
func (c *Client) CountTokens(ctx context.Context, request agent.ModelRequest) (TokenCount, error) {
	if c == nil {
		return TokenCount{}, &Error{Operation: "count tokens", Message: "client is nil"}
	}
	if ctx == nil {
		return TokenCount{}, &Error{Operation: "count tokens", Message: "context is nil"}
	}
	if strings.TrimSpace(request.Model) == "" {
		request.Model = c.config.Model
	}
	if err := request.Valid(); err != nil {
		return TokenCount{}, &Error{Operation: "count tokens", Message: err.Error()}
	}
	model, err := normalizeModelID(request.Model)
	if err != nil {
		return TokenCount{}, &Error{Operation: "count tokens", Message: err.Error()}
	}
	payload, err := nativeTokenRequest(request)
	if err != nil {
		return TokenCount{}, &Error{Operation: "count tokens", Message: err.Error()}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return TokenCount{}, &Error{Operation: "count tokens", Message: err.Error()}
	}
	requestURL, err := c.nativeURL("models/" + model + ":countTokens")
	if err != nil {
		return TokenCount{}, &Error{Operation: "count tokens", Message: err.Error()}
	}
	if err := nativeContextError(ctx); err != nil {
		return TokenCount{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodPost, requestURL.String(), bytes.NewReader(body))
	if err != nil {
		return TokenCount{}, &Error{Operation: "count tokens", Message: sanitize(err.Error(), c.config.APIKey)}
	}
	c.applyHeaders(httpRequest)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := c.config.HTTPClient.Do(httpRequest)
	if err != nil {
		if err := nativeContextError(ctx); err != nil {
			return TokenCount{}, err
		}
		return TokenCount{}, &Error{Operation: "count tokens", Message: sanitize(err.Error(), c.config.APIKey), Retryable: true}
	}
	defer response.Body.Close()
	responseBody, err := readBounded(response.Body, c.config.MaxResponseBytes)
	if err != nil {
		return TokenCount{}, &Error{Operation: "count tokens", StatusCode: response.StatusCode, Message: err.Error()}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return TokenCount{}, ParseError("count tokens", response.StatusCode, responseBody, response.Header, c.config.APIKey)
	}
	var decoded nativeTokenCountResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return TokenCount{}, &Error{Operation: "count tokens", StatusCode: response.StatusCode, Message: sanitize(err.Error(), c.config.APIKey)}
	}
	return TokenCount{
		TotalTokens: decoded.TotalTokens, CachedContentTokenCount: decoded.CachedContentTokenCount,
		ToolUsePromptTokenCount: decoded.ToolUsePromptTokenCount, ThoughtsTokenCount: decoded.ThoughtsTokenCount,
	}, nil
}

func nativeTokenRequest(request agent.ModelRequest) (nativeCountRequest, error) {
	var result nativeCountRequest
	var systemParts []nativePart
	for _, message := range request.Messages {
		parts := nativeParts(message)
		if len(parts) == 0 {
			continue
		}
		switch message.Role {
		case agent.RoleSystem, agent.RoleDeveloper:
			systemParts = append(systemParts, parts...)
		case agent.RoleAssistant:
			result.Contents = append(result.Contents, nativeContent{Role: "model", Parts: parts})
		default:
			result.Contents = append(result.Contents, nativeContent{Role: "user", Parts: parts})
		}
	}
	if len(systemParts) > 0 {
		result.SystemInstruction = &nativeContent{Parts: systemParts}
	}
	if len(request.Tools) > 0 {
		declarations := make([]nativeFunctionDeclaration, 0, len(request.Tools))
		for _, tool := range request.Tools {
			if !json.Valid(tool.InputSchema) {
				return nativeCountRequest{}, fmt.Errorf("tool %q has invalid input schema", tool.Name)
			}
			declarations = append(declarations, nativeFunctionDeclaration{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema})
		}
		result.Tools = []nativeTool{{FunctionDeclarations: declarations}}
	}
	return result, nil
}

func nativeParts(message agent.Message) []nativePart {
	parts := make([]nativePart, 0, 1+len(message.Parts)+len(message.ToolCalls))
	if message.Content != "" {
		text := message.Content
		if message.Role == agent.RoleTool {
			text = "Tool result (" + message.ToolCallID + "): " + text
		}
		parts = append(parts, nativePart{Text: text})
	}
	for _, part := range message.Parts {
		if part.Type == agent.ContentPartImage {
			parts = append(parts, nativePart{InlineData: &nativeData{MIMEType: part.MediaType, Data: part.Data}})
		}
	}
	for _, call := range message.ToolCalls {
		if json.Valid(call.Arguments) {
			parts = append(parts, nativePart{FunctionCall: &nativeFunctionCall{Name: call.Name, Args: call.Arguments}})
		}
	}
	return parts
}
