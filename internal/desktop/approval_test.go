package desktop

import (
	"context"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestDesktopToolAuthorizerEnforcesRiskBoundary(t *testing.T) {
	authorizer := desktopToolAuthorizer{}
	tests := []struct {
		risk     domain.RiskLevel
		decision domain.PermissionDecision
	}{
		{domain.RiskLow, domain.PermissionAllow},
		{domain.RiskMedium, domain.PermissionNeedsApproval},
		{domain.RiskHigh, domain.PermissionNeedsApproval},
		{domain.RiskCritical, domain.PermissionDeny},
	}
	for _, test := range tests {
		result, err := authorizer.Authorize(context.Background(), agent.ToolAuthorizationRequest{
			Tool: agent.ToolDescriptor{Name: "test", InputSchema: []byte(`{"type":"object"}`), Risk: test.risk},
		})
		if err != nil || result.Decision != test.decision {
			t.Fatalf("risk %s decision = %s, %v; want %s", test.risk, result.Decision, err, test.decision)
		}
	}
}
