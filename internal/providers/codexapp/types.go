// Package codexapp implements the official Codex App Server protocol boundary.
// Authentication remains owned by the Codex process; Yuri never reads or
// persists ChatGPT access or refresh tokens.
package codexapp

import (
	"encoding/json"
	"time"
)

const (
	DefaultBinary     = "codex"
	DefaultMaxMessage = 8 << 20
)

type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

type InitializeCapabilities struct {
	ExperimentalAPI bool `json:"experimentalApi"`
}

type Event struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

func (event Event) IsServerRequest() bool { return len(event.ID) > 0 }

type Account struct {
	Type     string  `json:"type"`
	Email    *string `json:"email"`
	PlanType *string `json:"planType"`
}

type AccountReadResult struct {
	Account            *Account `json:"account"`
	RequiresOpenAIAuth bool     `json:"requiresOpenaiAuth"`
}

type LoginResult struct {
	Type            string `json:"type"`
	LoginID         string `json:"loginId,omitempty"`
	AuthURL         string `json:"authUrl,omitempty"`
	VerificationURL string `json:"verificationUrl,omitempty"`
	UserCode        string `json:"userCode,omitempty"`
}

type RateLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int64   `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

func (window RateLimitWindow) ResetTime() time.Time {
	if window.ResetsAt <= 0 {
		return time.Time{}
	}
	return time.Unix(window.ResetsAt, 0).UTC()
}

type RateLimit struct {
	LimitID              string           `json:"limitId"`
	LimitName            *string          `json:"limitName"`
	Primary              *RateLimitWindow `json:"primary"`
	Secondary            *RateLimitWindow `json:"secondary"`
	RateLimitReachedType *string          `json:"rateLimitReachedType"`
	PlanType             *string          `json:"planType"`
}

type RateLimitsResult struct {
	RateLimits          *RateLimit           `json:"rateLimits"`
	RateLimitsByLimitID map[string]RateLimit `json:"rateLimitsByLimitId"`
}

type Model struct {
	ID                     string   `json:"id"`
	Model                  string   `json:"model"`
	DisplayName            string   `json:"displayName"`
	Description            string   `json:"description"`
	Hidden                 bool     `json:"hidden"`
	IsDefault              bool     `json:"isDefault"`
	DefaultReasoningEffort string   `json:"defaultReasoningEffort"`
	InputModalities        []string `json:"inputModalities,omitempty"`
}

type ModelListResult struct {
	Data       []Model `json:"data"`
	NextCursor string  `json:"nextCursor,omitempty"`
}

type Thread struct {
	ID string `json:"id"`
}

type Turn struct {
	ID     string `json:"id"`
	Status string `json:"status,omitempty"`
}
