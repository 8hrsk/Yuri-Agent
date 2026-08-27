package openai

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

type responsesRequest struct {
	Model           string            `json:"model"`
	Input           []any             `json:"input"`
	Tools           []responsesTool   `json:"tools,omitempty"`
	Stream          bool              `json:"stream"`
	MaxOutputTokens int64             `json:"max_output_tokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type responsesTextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesMessage struct {
	Role    string              `json:"role"`
	Content []responsesTextPart `json:"content"`
}

type responsesFunctionCall struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesFunctionOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

func responsesInput(messages []agent.Message) []any {
	result := make([]any, 0, len(messages))
	for _, message := range messages {
		if message.Role == agent.RoleTool {
			result = append(result, responsesFunctionOutput{Type: "function_call_output", CallID: message.ToolCallID, Output: message.Content})
			continue
		}
		if message.Content != "" {
			partType := "input_text"
			if message.Role == agent.RoleAssistant {
				partType = "output_text"
			}
			result = append(result, responsesMessage{Role: string(message.Role), Content: []responsesTextPart{{Type: partType, Text: message.Content}}})
		}
		for _, call := range message.ToolCalls {
			result = append(result, responsesFunctionCall{Type: "function_call", ID: call.ID, CallID: call.ID, Name: call.Name, Arguments: string(call.Arguments)})
		}
	}
	return result
}

func responsesTools(tools []agent.ToolDescriptor) []responsesTool {
	result := make([]responsesTool, 0, len(tools))
	for _, tool := range tools {
		result = append(result, responsesTool{Type: "function", Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema})
	}
	return result
}

type chatRequest struct {
	Model         string             `json:"model"`
	Messages      []chatMessage      `json:"messages"`
	Tools         []chatTool         `json:"tools,omitempty"`
	Stream        bool               `json:"stream"`
	StreamOptions *chatStreamOptions `json:"stream_options,omitempty"`
	MaxTokens     int64              `json:"max_tokens,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	Metadata      map[string]string  `json:"metadata,omitempty"`
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []chatCall `json:"tool_calls,omitempty"`
}

type chatCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatCallFunction `json:"function"`
}

type chatCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func chatMessages(messages []agent.Message) []chatMessage {
	result := make([]chatMessage, 0, len(messages))
	for _, message := range messages {
		content := message.Content
		item := chatMessage{Role: string(message.Role), Content: &content, Name: message.Name, ToolCallID: message.ToolCallID}
		if content == "" {
			item.Content = nil
		}
		for _, call := range message.ToolCalls {
			item.ToolCalls = append(item.ToolCalls, chatCall{ID: call.ID, Type: "function", Function: chatCallFunction{Name: call.Name, Arguments: string(call.Arguments)}})
		}
		result = append(result, item)
	}
	return result
}

func chatTools(tools []agent.ToolDescriptor) []chatTool {
	result := make([]chatTool, 0, len(tools))
	for _, tool := range tools {
		result = append(result, chatTool{Type: "function", Function: chatToolFunction{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema}})
	}
	return result
}

func stringOrNumber(value json.RawMessage) string {
	var stringValue string
	if json.Unmarshal(value, &stringValue) == nil {
		return stringValue
	}
	var number json.Number
	if json.Unmarshal(value, &number) == nil {
		return number.String()
	}
	return string(value)
}

func requiredString(value json.RawMessage, field string) (string, error) {
	var result string
	if err := json.Unmarshal(value, &result); err != nil || result == "" {
		return "", fmt.Errorf("openai: %s is required", field)
	}
	return result, nil
}

func ordinalID(index int) string {
	return "call_" + strconv.Itoa(index+1)
}
