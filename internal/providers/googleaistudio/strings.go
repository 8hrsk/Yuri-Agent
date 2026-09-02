package googleaistudio

import (
	"fmt"
	"regexp"
	"strings"
)

var modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,256}$`)

func normalizeModelID(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "models/")
	if !modelIDPattern.MatchString(value) {
		return "", fmt.Errorf("invalid Google model id")
	}
	return value, nil
}

func uniqueStrings(values []string, maximum int) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || len(value) > 128 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == maximum {
			break
		}
	}
	return result
}

func boundedText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) > maximum {
		return value[:maximum]
	}
	return value
}
