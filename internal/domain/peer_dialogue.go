package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	PeerDialoguePurposeMaxRunes = 256
	PeerDialogueMessageMaxBytes = 16 * 1024
	PeerDialogueMaxTurns        = 4
)

type PeerDialogueTriggerKind string

const (
	PeerDialogueTriggerAgentTool  PeerDialogueTriggerKind = "agent_tool"
	PeerDialogueTriggerAutonomous PeerDialogueTriggerKind = "autonomous"
)

func (kind PeerDialogueTriggerKind) Valid() bool {
	return kind == PeerDialogueTriggerAgentTool || kind == PeerDialogueTriggerAutonomous
}

type PeerDialogueStatus string

const (
	PeerDialogueQueued     PeerDialogueStatus = "queued"
	PeerDialogueRunning    PeerDialogueStatus = "running"
	PeerDialogueCancelling PeerDialogueStatus = "cancelling"
	PeerDialogueCompleted  PeerDialogueStatus = "completed"
	PeerDialogueFailed     PeerDialogueStatus = "failed"
	PeerDialogueCancelled  PeerDialogueStatus = "cancelled"
	PeerDialogueExpired    PeerDialogueStatus = "expired"
)

func (s PeerDialogueStatus) Valid() bool {
	switch s {
	case PeerDialogueQueued, PeerDialogueRunning, PeerDialogueCancelling,
		PeerDialogueCompleted, PeerDialogueFailed, PeerDialogueCancelled, PeerDialogueExpired:
		return true
	default:
		return false
	}
}

func (s PeerDialogueStatus) Terminal() bool {
	return s == PeerDialogueCompleted || s == PeerDialogueFailed || s == PeerDialogueCancelled || s == PeerDialogueExpired
}

type PeerDialogueBudget struct {
	MaxTurns           int   `json:"max_turns"`
	MaxTokens          int64 `json:"max_tokens"`
	MaxDurationSeconds int   `json:"max_duration_seconds"`
	CooldownSeconds    int   `json:"cooldown_seconds"`
}

func (b PeerDialogueBudget) Valid() bool {
	return b.MaxTurns >= 1 && b.MaxTurns <= PeerDialogueMaxTurns &&
		b.MaxTokens >= 1 && b.MaxTokens <= 16_000 &&
		b.MaxDurationSeconds >= 5 && b.MaxDurationSeconds <= 300 &&
		b.CooldownSeconds >= 0 && b.CooldownSeconds <= 24*60*60
}

type PeerDialogue struct {
	ID               ID                      `json:"id"`
	InitiatorAgentID ID                      `json:"initiator_agent_id"`
	PeerAgentID      ID                      `json:"peer_agent_id"`
	TriggerRunID     ID                      `json:"trigger_run_id"`
	TriggerKind      PeerDialogueTriggerKind `json:"trigger_kind"`
	TriggerReason    string                  `json:"trigger_reason"`
	PairKey          string                  `json:"pair_key"`
	Purpose          string                  `json:"purpose"`
	Status           PeerDialogueStatus      `json:"status"`
	Budget           PeerDialogueBudget      `json:"budget"`
	TurnCount        int                     `json:"turn_count"`
	TokensUsed       int64                   `json:"tokens_used"`
	IdempotencyKey   string                  `json:"idempotency_key"`
	RequestHash      string                  `json:"request_hash"`
	Failure          string                  `json:"failure,omitempty"`
	Version          uint64                  `json:"version"`
	CreatedAt        time.Time               `json:"created_at"`
	UpdatedAt        time.Time               `json:"updated_at"`
	StartedAt        time.Time               `json:"started_at,omitempty"`
	FinishedAt       time.Time               `json:"finished_at,omitempty"`
	ExpiresAt        time.Time               `json:"expires_at"`
}

func NewPeerDialogue(id, initiatorAgentID, peerAgentID, triggerRunID ID, purpose, idempotencyKey, requestHash string, budget PeerDialogueBudget, now time.Time) (PeerDialogue, error) {
	now = now.UTC()
	dialogue := PeerDialogue{
		ID: id, InitiatorAgentID: initiatorAgentID, PeerAgentID: peerAgentID, TriggerRunID: triggerRunID,
		TriggerKind: PeerDialogueTriggerAgentTool, TriggerReason: "Агент явно запросил консультацию peer через tool.",
		PairKey: AgentPairKey(initiatorAgentID, peerAgentID), Purpose: strings.TrimSpace(purpose),
		Status: PeerDialogueQueued, Budget: budget, IdempotencyKey: strings.TrimSpace(idempotencyKey), RequestHash: strings.TrimSpace(requestHash),
		Version: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Duration(budget.MaxDurationSeconds) * time.Second),
	}
	if err := dialogue.Validate(); err != nil {
		return PeerDialogue{}, err
	}
	return dialogue, nil
}

func (d PeerDialogue) Validate() error {
	if d.ID.Empty() || d.InitiatorAgentID.Empty() || d.PeerAgentID.Empty() || d.TriggerRunID.Empty() || d.InitiatorAgentID == d.PeerAgentID {
		return fmt.Errorf("%w: dialogue identity, participants, and trigger run are required", ErrInvalidArgument)
	}
	if !d.TriggerKind.Valid() || strings.TrimSpace(d.TriggerReason) == "" || utf8.RuneCountInString(d.TriggerReason) > 512 || strings.ContainsRune(d.TriggerReason, '\x00') {
		return fmt.Errorf("%w: peer dialogue trigger provenance is invalid", ErrInvalidArgument)
	}
	if d.PairKey != AgentPairKey(d.InitiatorAgentID, d.PeerAgentID) {
		return fmt.Errorf("%w: invalid dialogue pair key", ErrInvalidArgument)
	}
	if strings.TrimSpace(d.Purpose) == "" || utf8.RuneCountInString(d.Purpose) > PeerDialoguePurposeMaxRunes || strings.ContainsRune(d.Purpose, '\x00') {
		return fmt.Errorf("%w: dialogue purpose must contain 1..%d characters", ErrInvalidArgument, PeerDialoguePurposeMaxRunes)
	}
	if !d.Status.Valid() || !d.Budget.Valid() || d.Version == 0 || d.TurnCount < 0 || d.TurnCount > d.Budget.MaxTurns || d.TokensUsed < 0 || d.TokensUsed > d.Budget.MaxTokens {
		return fmt.Errorf("%w: invalid dialogue lifecycle or budget", ErrInvalidArgument)
	}
	if strings.TrimSpace(d.IdempotencyKey) == "" || len(d.IdempotencyKey) > 256 || strings.TrimSpace(d.RequestHash) == "" || len(d.RequestHash) > 256 {
		return fmt.Errorf("%w: dialogue idempotency metadata is invalid", ErrInvalidArgument)
	}
	if len(d.Failure) > 1024 || strings.ContainsRune(d.Failure, '\x00') {
		return fmt.Errorf("%w: dialogue failure exceeds its bound", ErrInvalidArgument)
	}
	if d.Status == PeerDialogueFailed && strings.TrimSpace(d.Failure) == "" {
		return fmt.Errorf("%w: failed dialogue requires a failure", ErrInvalidArgument)
	}
	if d.Status != PeerDialogueFailed && strings.TrimSpace(d.Failure) != "" {
		return fmt.Errorf("%w: only failed dialogue may persist a failure", ErrInvalidArgument)
	}
	if d.Status == PeerDialogueCompleted && d.TurnCount == 0 {
		return fmt.Errorf("%w: completed dialogue requires at least one generated turn", ErrInvalidArgument)
	}
	if d.CreatedAt.IsZero() || d.UpdatedAt.IsZero() || d.ExpiresAt.IsZero() || !d.ExpiresAt.After(d.CreatedAt) || d.UpdatedAt.Before(d.CreatedAt) {
		return fmt.Errorf("%w: invalid dialogue timestamps", ErrInvalidArgument)
	}
	return nil
}

func (d *PeerDialogue) MarkAutonomous(reason string) error {
	if d == nil {
		return fmt.Errorf("%w: peer dialogue is required", ErrInvalidArgument)
	}
	d.TriggerKind = PeerDialogueTriggerAutonomous
	d.TriggerReason = strings.TrimSpace(reason)
	return d.Validate()
}

func (d PeerDialogue) CanTransition(next PeerDialogueStatus) bool {
	if !next.Valid() || d.Status.Terminal() {
		return false
	}
	switch d.Status {
	case PeerDialogueQueued:
		return next == PeerDialogueRunning || next == PeerDialogueFailed || next == PeerDialogueCancelled || next == PeerDialogueExpired
	case PeerDialogueRunning:
		return next == PeerDialogueCompleted || next == PeerDialogueFailed || next == PeerDialogueCancelling || next == PeerDialogueExpired
	case PeerDialogueCancelling:
		return next == PeerDialogueCancelled || next == PeerDialogueFailed
	default:
		return false
	}
}

func (d *PeerDialogue) Transition(next PeerDialogueStatus, now time.Time) error {
	if d == nil || now.IsZero() {
		return fmt.Errorf("%w: dialogue and timestamp are required", ErrInvalidArgument)
	}
	if !d.CanTransition(next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, d.Status, next)
	}
	d.Status = next
	d.UpdatedAt = now.UTC()
	d.Version++
	if next == PeerDialogueRunning && d.StartedAt.IsZero() {
		d.StartedAt = d.UpdatedAt
	}
	if next.Terminal() {
		d.FinishedAt = d.UpdatedAt
	}
	return nil
}

func (d *PeerDialogue) RecordTurn(tokens int64, now time.Time) error {
	if d == nil || d.Status != PeerDialogueRunning || tokens < 0 || now.IsZero() {
		return fmt.Errorf("%w: dialogue is not accepting turns", ErrInvalidArgument)
	}
	if d.TurnCount+1 > d.Budget.MaxTurns || d.TokensUsed+tokens > d.Budget.MaxTokens {
		return fmt.Errorf("%w: dialogue budget exceeded", ErrNotPermitted)
	}
	d.TurnCount++
	d.TokensUsed += tokens
	d.UpdatedAt = now.UTC()
	d.Version++
	return nil
}

func AgentPairKey(left, right ID) string {
	a, b := strings.TrimSpace(string(left)), strings.TrimSpace(string(right))
	if a > b {
		a, b = b, a
	}
	digest := sha256.Sum256([]byte(a + "\x00" + b))
	return "sha256:" + hex.EncodeToString(digest[:])
}

type PeerDialogueMessage struct {
	ID               ID        `json:"id"`
	DialogueID       ID        `json:"dialogue_id"`
	Sequence         int       `json:"sequence"`
	SenderAgentID    ID        `json:"sender_agent_id"`
	RecipientAgentID ID        `json:"recipient_agent_id"`
	SourceRunID      ID        `json:"source_run_id"`
	Content          string    `json:"content"`
	CreatedAt        time.Time `json:"created_at"`
}

func (m PeerDialogueMessage) Validate() error {
	if m.ID.Empty() || m.DialogueID.Empty() || m.SenderAgentID.Empty() || m.RecipientAgentID.Empty() || m.SourceRunID.Empty() || m.SenderAgentID == m.RecipientAgentID || m.Sequence < 0 {
		return fmt.Errorf("%w: invalid peer dialogue message identity", ErrInvalidArgument)
	}
	content := strings.TrimSpace(m.Content)
	if content == "" || len(content) > PeerDialogueMessageMaxBytes || strings.ContainsRune(content, '\x00') || !utf8.ValidString(content) {
		return fmt.Errorf("%w: peer dialogue message is empty or too large", ErrInvalidArgument)
	}
	if m.CreatedAt.IsZero() {
		return fmt.Errorf("%w: peer dialogue message timestamp is required", ErrInvalidArgument)
	}
	return nil
}
