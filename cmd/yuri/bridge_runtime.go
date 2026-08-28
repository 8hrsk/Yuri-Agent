//go:build !bindings

package main

import (
	"context"

	"github.com/OrdoAI/yuri-agent/internal/desktop"
)

// newBridge opens Yuri's durable local services for the real application.
func newBridge(ctx context.Context) (*desktop.Bridge, error) {
	return desktop.NewBridge(ctx)
}
