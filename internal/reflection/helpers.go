package reflection

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

var namePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)

func validateName(value string) error {
	value = strings.TrimSpace(value)
	if !namePattern.MatchString(value) {
		return fmt.Errorf("name must match %s", namePattern.String())
	}
	return nil
}

func validateScalarMap(values map[string]float64, label string) error {
	if len(values) > 128 {
		return fmt.Errorf("%w: %s contains too many dimensions", ErrInvalidSnapshot, label)
	}
	for name, value := range values {
		if err := validateName(name); err != nil {
			return fmt.Errorf("%w: invalid %s key %q: %v", ErrInvalidSnapshot, label, name, err)
		}
		if !finite(value) {
			return fmt.Errorf("%w: %s value %q is not finite", ErrInvalidSnapshot, label, name)
		}
	}
	return nil
}

func validateIDSlice(ids []domain.ID, label string, sentinel error) error {
	seen := make(map[domain.ID]struct{}, len(ids))
	for _, id := range ids {
		if id.Empty() {
			return fmt.Errorf("%w: %s contains an empty id", sentinel, label)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: %s contains duplicate id %s", sentinel, label, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validatePromptText(value string, maxBytes int, sentinel error) error {
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%w: prompt contains NUL", sentinel)
	}
	if maxBytes > 0 && len([]byte(value)) > maxBytes {
		return fmt.Errorf("%w: prompt exceeds %d bytes", sentinel, maxBytes)
	}
	return nil
}

// sortedKeys makes deterministic guard/error ordering possible for map-based
// deltas and is also useful to adapters that want stable audit output.
func sortedKeys(values map[string]float64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ContextError converts a nil/cancelled context to a stable package error
// while preserving context.Canceled and context.DeadlineExceeded for callers.
func ContextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidSnapshot)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
