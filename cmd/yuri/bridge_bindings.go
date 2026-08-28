//go:build bindings

package main

import (
	"context"

	"github.com/OrdoAI/yuri-agent/internal/desktop"
)

// Binding generation only reflects exported bridge methods. It must not open
// or migrate the owner's database as a side effect of building the app.
func newBridge(context.Context) (*desktop.Bridge, error) {
	return &desktop.Bridge{}, nil
}
