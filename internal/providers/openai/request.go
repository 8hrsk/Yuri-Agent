package openai

import (
	"encoding/json"
	"strconv"

	"github.com/OrdoAI/yuri-agent/internal/agent"
)

type responsesRequest struct {
	Model           string            `json:"model"`
	Input           []any             `json:"input"`
	Tools           []responsesTool   `json:"tools,omitempty"`
	ToolChoice      any               `json:"tool_choice,omitempty"`
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

type responsesContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type responsesMessage struct {
	Role    string                 `json:"role"`
	Content []responsesContentPart `json:"content"`
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
		if message.Content != "" || len(message.Parts) > 0 {
			partType := "input_text"
			if message.Role == agent.RoleAssistant {
				partType = "output_text"
			}
			parts := make([]responsesContentPart, 0, 1+len(message.Parts))
			if message.Content != "" {
				parts = append(parts, responsesContentPart{Type: partType, Text: message.Content})
			}
			if message.Role == agent.RoleUser {
				for _, part := range message.Parts {
					if part.Type == agent.ContentPartImage {
						parts = append(parts, responsesContentPart{Type: "input_image", ImageURL: dataURL(part)})
					}
				}
			}
			result = append(result, responsesMessage{Role: string(message.Role), Content: parts})
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
	ToolChoice    any                `json:"tool_choice,omitempty"`
	Stream        bool               `json:"stream"`
	StreamOptions *chatStreamOptions `json:"stream_options,omitempty"`
	MaxTokens     int64              `json:"max_tokens,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	Metadata      map[string]string  `json:"metadata,omitempty"`
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []chatCall `json:"tool_calls,omitempty"`
}

type chatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL string `json:"url"`
}

type chatCall struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	Function     chatCallFunction `json:"function"`
	ExtraContent json.RawMessage  `json:"extra_content,omitempty"`
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
		item := chatMessage{Role: string(message.Role), Name: message.Name, ToolCallID: message.ToolCallID}
		if len(message.Parts) == 0 {
			if message.Content != "" {
				item.Content = message.Content
			}
		} else {
			parts := make([]chatContentPart, 0, 1+len(message.Parts))
			if message.Content != "" {
				parts = append(parts, chatContentPart{Type: "text", Text: message.Content})
			}
			if message.Role == agent.RoleUser {
				for _, part := range message.Parts {
					if part.Type == agent.ContentPartImage {
						parts = append(parts, chatContentPart{Type: "image_url", ImageURL: &chatImageURL{URL: dataURL(part)}})
					}
				}
			}
			item.Content = parts
		}
		for _, call := range message.ToolCalls {
			item.ToolCalls = append(item.ToolCalls, chatCall{
				ID: call.ID, Type: "function", Function: chatCallFunction{Name: call.Name, Arguments: string(call.Arguments)},
				ExtraContent: append(json.RawMessage(nil), call.ProviderExtras...),
			})
		}
		result = append(result, item)
	}
	return result
}

func dataURL(part agent.ContentPart) string {
	return "data:" + part.MediaType + ";base64," + part.Data
}

func chatTools(tools []agent.ToolDescriptor) []chatTool {
	result := make([]chatTool, 0, len(tools))
	for _, tool := range tools {
		result = append(result, chatTool{Type: "function", Function: chatToolFunction{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema}})
	}
	return result
}

func responsesToolChoice(choice agent.ToolChoice) any {
	if choice.Mode == "" {
		return nil
	}
	if choice.Name == "" {
		return string(choice.Mode)
	}
	return struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}{Type: "function", Name: choice.Name}
}

func chatToolChoice(choice agent.ToolChoice) any {
	if choice.Mode == "" {
		return nil
	}
	if choice.Name == "" {
		return string(choice.Mode)
	}
	return struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}{Type: "function", Function: struct {
		Name string `json:"name"`
	}{Name: choice.Name}}
}

func ordinalID(index int) string {
	return "call_" + strconv.Itoa(index+1)
}
