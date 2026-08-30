package desktop

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestScheduleToolInputRejectsOutOfRangeIntervalAndBudget(t *testing.T) {
	cases := map[string]string{
		"interval below the floor": `{"title":"t","prompt":"p","type":"interval","intervalSeconds":30,"timezone":"UTC","misfirePolicy":"skip"}`,
		"interval above the ceiling": `{"title":"t","prompt":"p","type":"interval","intervalSeconds":40000000,` +
			`"timezone":"UTC","misfirePolicy":"skip"}`,
		"token budget above the ceiling": `{"title":"t","prompt":"p","type":"interval","intervalSeconds":3600,` +
			`"timezone":"UTC","misfirePolicy":"skip","budget":{"maxTokens":5000000}}`,
		"tool call budget above the ceiling": `{"title":"t","prompt":"p","type":"interval","intervalSeconds":3600,` +
			`"timezone":"UTC","misfirePolicy":"skip","budget":{"maxToolCalls":10000}}`,
		"duration budget above the ceiling": `{"title":"t","prompt":"p","type":"interval","intervalSeconds":3600,` +
			`"timezone":"UTC","misfirePolicy":"skip","budget":{"maxDurationSeconds":86400}}`,
		"negative budget": `{"title":"t","prompt":"p","type":"interval","intervalSeconds":3600,` +
			`"timezone":"UTC","misfirePolicy":"skip","budget":{"maxTokens":-1}}`,
		"unknown schedule type": `{"title":"t","prompt":"p","type":"forever","timezone":"UTC","misfirePolicy":"skip"}`,
		"unknown misfire policy": `{"title":"t","prompt":"p","type":"interval","intervalSeconds":3600,` +
			`"timezone":"UTC","misfirePolicy":"loop"}`,
		"unknown timezone": `{"title":"t","prompt":"p","type":"interval","intervalSeconds":3600,` +
			`"timezone":"Mars/Olympus","misfirePolicy":"skip"}`,
		"undeclared member":     `{"title":"t","prompt":"p","type":"interval","intervalSeconds":3600,"timezone":"UTC","misfirePolicy":"skip","retries":99}`,
		"existing schedule id":  `{"id":"schedule_1","title":"t","prompt":"p","type":"interval","intervalSeconds":3600,"timezone":"UTC","misfirePolicy":"skip"}`,
		"cron without fields":   `{"title":"t","prompt":"p","type":"cron","expression":"* *","timezone":"UTC","misfirePolicy":"skip"}`,
		"cron without interval": `{"title":"t","prompt":"p","type":"cron","expression":"0 9 * * *","intervalSeconds":3600,"timezone":"UTC","misfirePolicy":"skip"}`,
		"one-shot in the past":  `{"title":"t","prompt":"p","type":"once","runAt":"2000-01-01T00:00:00Z","timezone":"UTC","misfirePolicy":"skip"}`,
	}
	for name, arguments := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeScheduleToolInput(json.RawMessage(arguments)); err == nil {
				t.Fatalf("%s was accepted by the server", name)
			}
		})
	}
}

func TestScheduleToolInputAcceptsBoundedRequests(t *testing.T) {
	runAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	valid := []string{
		`{"title":"t","prompt":"p","type":"interval","intervalSeconds":60,"timezone":"Europe/Moscow","misfirePolicy":"run_once"}`,
		`{"title":"t","prompt":"p","type":"cron","expression":"0 9 * * 1","timezone":"UTC","misfirePolicy":"skip","deliveryChannel":"notification"}`,
		`{"title":"t","prompt":"p","type":"once","runAt":"` + runAt + `","timezone":"UTC","misfirePolicy":"skip",` +
			`"budget":{"maxDurationSeconds":300,"maxTokens":5000,"maxToolCalls":8}}`,
	}
	for _, arguments := range valid {
		if _, err := decodeScheduleToolInput(json.RawMessage(arguments)); err != nil {
			t.Fatalf("valid request rejected: %v\n%s", err, arguments)
		}
	}
}

func TestScheduleToolApprovalScopeShowsIntervalAndBudget(t *testing.T) {
	arguments := json.RawMessage(`{"title":"t","prompt":"p","type":"interval","intervalSeconds":900,` +
		`"timezone":"UTC","misfirePolicy":"skip","budget":{"maxTokens":4000}}`)
	scope, ok := scheduleAgentTool{}.ApprovalScope(arguments)
	if !ok {
		t.Fatal("approval scope was not produced for a valid request")
	}
	for _, fragment := range []string{"900", "UTC", "in_app", "4000", "180", "8"} {
		if !strings.Contains(scope, fragment) {
			t.Fatalf("approval scope %q is missing %q", scope, fragment)
		}
	}
	_, ok = scheduleAgentTool{}.ApprovalScope(json.RawMessage(`{"type":"interval","intervalSeconds":1}`))
	if ok {
		t.Fatal("approval scope was produced for a request the server rejects")
	}
}
