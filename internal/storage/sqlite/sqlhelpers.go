package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func safeFTSQuery(query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("%w: search query is required", domain.ErrInvalidArgument)
	}
	words := strings.FieldsFunc(query, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	})
	quoted := make([]string, 0, len(words))
	for _, word := range words {
		if strings.TrimSpace(word) == "" {
			continue
		}
		quoted = append(quoted, `"`+strings.ReplaceAll(word, `"`, `""`)+`"`)
	}
	if len(quoted) == 0 {
		return "", fmt.Errorf("%w: search query has no searchable terms", domain.ErrInvalidArgument)
	}
	return strings.Join(quoted, " AND "), nil
}

func approximateTokens(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	return (len([]rune(value)) + 3) / 4
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// isNoRows must use errors.Is, not ==. Rows reach this helper through
// interfaces and wrappers (rowScanner, wrappedSQLError, repository helpers),
// and a single fmt.Errorf("...: %w", sql.ErrNoRows) anywhere on that path
// turns a plain "not found" into an unexpected internal error under ==.
func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
