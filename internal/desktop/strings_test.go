package desktop

import "testing"

func TestSafeErrorKeepsTokenBudgetDiagnosticsButRedactsCredentials(t *testing.T) {
	if got := safeError("maximum output token limit exceeded"); got != "maximum output token limit exceeded" {
		t.Fatalf("safe token diagnostic = %q", got)
	}
	for _, unsafe := range []string{
		"Authorization: Bearer private-value",
		`{"access_token":"private-value"}`,
		"api_key=private-value",
	} {
		if got := safeError(unsafe); got != "Операция провайдера завершилась ошибкой" {
			t.Fatalf("credential-like error was not redacted: %q", got)
		}
	}
}
