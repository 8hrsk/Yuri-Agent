package agent

import "errors"

var (
	ErrInvalidRequest   = errors.New("agent: invalid request")
	ErrBudgetExceeded   = errors.New("agent: run budget exceeded")
	ErrApprovalRequired = errors.New("agent: tool approval required")
	ErrToolNotFound     = errors.New("agent: tool not found")
	ErrToolArguments    = errors.New("agent: invalid tool arguments")
	ErrBackend          = errors.New("agent: backend error")
)
