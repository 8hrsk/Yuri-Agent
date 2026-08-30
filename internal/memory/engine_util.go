package memory

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func nonZeroTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback.UTC()
	}
	return value.UTC()
}

func truncateUTF8(value string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= maxChars {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxChars])
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// errorsIsNotFound must use errors.Is, not ==. Archive and storage adapters
// wrap what they return, and a wrapped domain.ErrNotFound compared with ==
// reports "not found" as an unexpected failure. The string fallback stays for
// adapters that rebuild the error instead of wrapping it.
func errorsIsNotFound(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, domain.ErrNotFound) || strings.Contains(err.Error(), domain.ErrNotFound.Error())
}
