package desktop

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

type peerDialogueTurnOutcome string

const (
	peerDialogueTurnContinue peerDialogueTurnOutcome = "continue"
	peerDialogueTurnComplete peerDialogueTurnOutcome = "complete"
)

type peerDialogueTurn struct {
	Message    string
	Outcome    peerDialogueTurnOutcome
	Structured bool
}

// parsePeerDialogueTurn supports old providers that return plain text while
// making the new semantic-completion contract strict whenever a response
// claims to be JSON. Malformed structured output is never stored as dialogue
// text because it could expose control fields or markdown scaffolding.
func parsePeerDialogueTurn(raw string) (peerDialogueTurn, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return peerDialogueTurn{}, errors.New("peer returned an empty response")
	}
	payload, claimsJSON := peerDialogueJSONPayload(value)
	if !claimsJSON {
		message := boundUTF8Bytes(value, domain.PeerDialogueMessageMaxBytes)
		if message == "" {
			return peerDialogueTurn{}, errors.New("peer returned an empty response")
		}
		return peerDialogueTurn{Message: message, Outcome: peerDialogueTurnContinue}, nil
	}
	if payload == nil {
		return peerDialogueTurn{}, errors.New("peer returned malformed structured output")
	}
	var envelope struct {
		Message string                  `json:"message"`
		Outcome peerDialogueTurnOutcome `json:"outcome"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return peerDialogueTurn{}, errors.New("peer returned malformed structured output")
	}
	message := boundUTF8Bytes(strings.TrimSpace(envelope.Message), domain.PeerDialogueMessageMaxBytes)
	if message == "" {
		return peerDialogueTurn{}, errors.New("peer returned an empty structured message")
	}
	if envelope.Outcome != peerDialogueTurnContinue && envelope.Outcome != peerDialogueTurnComplete {
		return peerDialogueTurn{}, errors.New("peer returned unsupported dialogue outcome")
	}
	return peerDialogueTurn{Message: message, Outcome: envelope.Outcome, Structured: true}, nil
}

func peerDialogueJSONPayload(value string) ([]byte, bool) {
	trimmed := strings.TrimSpace(value)
	start, end := strings.IndexByte(trimmed, '{'), strings.LastIndexByte(trimmed, '}')
	claimsJSON := strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "```") || (start >= 0 && end > start)
	if !claimsJSON {
		return nil, false
	}
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```JSON")
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSuffix(strings.TrimSpace(trimmed), "```")
	}
	start, end = strings.IndexByte(trimmed, '{'), strings.LastIndexByte(trimmed, '}')
	if start < 0 || end <= start {
		return nil, true
	}
	payload := []byte(trimmed[start : end+1])
	if !json.Valid(payload) {
		return nil, true
	}
	return payload, true
}
