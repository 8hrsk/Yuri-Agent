package desktop

import (
	"strings"
	"testing"
)

func TestParsePeerDialogueTurnStructuredAndLegacy(t *testing.T) {
	structured, err := parsePeerDialogueTurn("```json\n{\"message\":\"Цель достигнута.\",\"outcome\":\"complete\"}\n```")
	if err != nil || structured.Message != "Цель достигнута." || structured.Outcome != peerDialogueTurnComplete || !structured.Structured {
		t.Fatalf("structured turn = %#v, %v", structured, err)
	}
	legacy, err := parsePeerDialogueTurn("Обычный ответ старого провайдера.")
	if err != nil || legacy.Message == "" || legacy.Outcome != peerDialogueTurnContinue || legacy.Structured {
		t.Fatalf("legacy turn = %#v, %v", legacy, err)
	}
}

func TestParsePeerDialogueTurnRejectsMalformedClaimedJSON(t *testing.T) {
	for _, value := range []string{
		`{"message":"нет outcome"}`,
		`{"message":"x","outcome":"stop"}`,
		`{"message":`,
		"```json\nnot json\n```",
	} {
		if _, err := parsePeerDialogueTurn(value); err == nil {
			t.Fatalf("malformed output accepted: %q", value)
		}
	}
	tooLong := `{"message":"` + strings.Repeat("я", 20_000) + `","outcome":"continue"}`
	turn, err := parsePeerDialogueTurn(tooLong)
	if err != nil || len(turn.Message) > 16*1024 {
		t.Fatalf("bounded turn bytes=%d err=%v", len(turn.Message), err)
	}
}
