package desktop

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/executionbudget"
)

// StartPeerDialogueInput is an explicit owner action. Optional numeric fields
// are upper bounds for this one exchange and are intersected with the active
// agent preset and model metadata before persistence.
type StartPeerDialogueInput struct {
	PeerAgentID        string `json:"peerAgentId"`
	Purpose            string `json:"purpose"`
	Message            string `json:"message"`
	MaxTurns           int    `json:"maxTurns,omitempty"`
	MaxTokens          int64  `json:"maxTokens,omitempty"`
	MaxDurationSeconds int    `json:"maxDurationSeconds,omitempty"`
}

type StartPeerDialogueView struct {
	ID                 string `json:"id"`
	MinTurns           int    `json:"minTurns"`
	MaxTurns           int    `json:"maxTurns"`
	MaxTokens          int64  `json:"maxTokens"`
	MaxDurationSeconds int    `json:"maxDurationSeconds"`
}

func (b *Bridge) StartPeerDialogue(input StartPeerDialogueInput) (StartPeerDialogueView, error) {
	if err := validateManualPeerBudget(input); err != nil {
		return StartPeerDialogueView{}, err
	}
	arguments, err := json.Marshal(peerDialogueToolInput{
		PeerAgentID: input.PeerAgentID,
		Purpose:     input.Purpose,
		Message:     input.Message,
	})
	if err != nil {
		return StartPeerDialogueView{}, err
	}
	if _, err := decodePeerDialogueInput(arguments); err != nil {
		return StartPeerDialogueView{}, err
	}

	ctx, cancel := b.context()
	defer cancel()
	initiatorID := b.personaProfileID()
	profile, err := b.repositories.Agents.Get(ctx, initiatorID)
	if err != nil {
		return StartPeerDialogueView{}, err
	}
	backend, model, err := b.chatBackendForAgent(ctx, initiatorID)
	if err != nil {
		return StartPeerDialogueView{}, err
	}
	route, err := b.resolveInferenceRoute(profile.ProviderID, model)
	if err != nil {
		return StartPeerDialogueView{}, err
	}
	runID, err := domain.NewID("run_peer_manual")
	if err != nil {
		return StartPeerDialogueView{}, err
	}
	now := time.Now().UTC()
	run, err := domain.NewRunForAgent(initiatorID, runID, domain.RunKindBackground, "", now)
	if err != nil {
		return StartPeerDialogueView{}, err
	}
	run.Inference = route
	run.Budget = executionbudget.ResolveRun(profile.ExecutionBudget, executionbudget.WorkloadBackground, modelExecutionLimits(backend, model)).Budget
	if err := b.repositories.Runs.Create(ctx, run); err != nil {
		return StartPeerDialogueView{}, err
	}
	if err := transitionAndSave(ctx, b.repositories.Runs, &run, domain.RunStateQueued); err != nil {
		return StartPeerDialogueView{}, err
	}
	if err := transitionAndSave(ctx, b.repositories.Runs, &run, domain.RunStateRunning); err != nil {
		return StartPeerDialogueView{}, err
	}
	callID, err := domain.NewID("peer_manual_call")
	if err != nil {
		return StartPeerDialogueView{}, b.failPeerTurn(run, err)
	}
	tool := peerDialogueAgentTool{
		bridge: b, backend: backend, model: model, initiatorAgentID: initiatorID, triggerRunID: runID,
		budgetOverride: executionbudget.PeerOverride{MaxTurns: input.MaxTurns, MaxTokens: input.MaxTokens, MaxDurationSeconds: input.MaxDurationSeconds},
		triggerReason:  "Владелец вручную запустил внутренний диалог из Collaboration.",
	}
	if _, err := tool.Execute(ctx, agent.ToolCall{ID: string(callID), Name: peerDialogueToolID, Arguments: arguments}); err != nil {
		return StartPeerDialogueView{}, b.failPeerTurn(run, err)
	}
	if err := transitionAndSave(ctx, b.repositories.Runs, &run, domain.RunStateCompleted); err != nil {
		return StartPeerDialogueView{}, err
	}
	dialogue, err := b.repositories.PeerDialogues.Get(ctx, peerDialogueID(runID, string(callID)))
	if err != nil {
		return StartPeerDialogueView{}, err
	}
	return StartPeerDialogueView{
		ID: dialogue.ID.String(), MinTurns: dialogue.Budget.MinTurns, MaxTurns: dialogue.Budget.MaxTurns,
		MaxTokens: dialogue.Budget.MaxTokens, MaxDurationSeconds: dialogue.Budget.MaxDurationSeconds,
	}, nil
}

func validateManualPeerBudget(input StartPeerDialogueInput) error {
	if input.MaxTurns < 0 || input.MaxTurns > domain.PeerDialogueMaxTurns {
		return fmt.Errorf("%w: max turns must be zero or between 1 and %d", domain.ErrInvalidArgument, domain.PeerDialogueMaxTurns)
	}
	if input.MaxTokens < 0 || input.MaxTokens > 16_000 {
		return fmt.Errorf("%w: max tokens must be zero or between 1 and 16000", domain.ErrInvalidArgument)
	}
	if input.MaxDurationSeconds < 0 || input.MaxDurationSeconds > 300 || input.MaxDurationSeconds > 0 && input.MaxDurationSeconds < 5 {
		return fmt.Errorf("%w: max duration must be between 5 and 300 seconds", domain.ErrInvalidArgument)
	}
	if strings.TrimSpace(input.PeerAgentID) == "" {
		return fmt.Errorf("%w: peer agent is required", domain.ErrInvalidArgument)
	}
	return nil
}
