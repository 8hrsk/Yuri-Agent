package sqlite

import (
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// The list methods below scan records through the package-level rowScanner
// interface declared in plugins.go, which both *sql.Row and *sql.Rows satisfy.
// That lets one scanner serve the single-row get() path and the list path, so
// a list reads full rows in one round-trip instead of selecting ids and
// re-reading each record with its own QueryRowContext.

const (
	// defaultListLimit bounds a list method that its caller invoked without a
	// limit. The connection pool is deliberately a single connection, so an
	// unbounded read blocks every writer for its whole duration.
	defaultListLimit = 1000
	// maxListLimit clamps an explicit limit. A caller asking for more than
	// this wants a stream, not a list, and should page instead.
	maxListLimit = 10000
)

// listWindow interprets the optional variadic tail shared by the list methods:
// window[0] is a limit and window[1] an offset. A zero or absent limit means
// "no application-level limit" for the methods that still permit that.
func listWindow(subject string, window []int) (limit int, offset int, err error) {
	if len(window) > 2 {
		return 0, 0, fmt.Errorf("%w: %s accepts at most a limit and an offset", domain.ErrInvalidArgument, subject)
	}
	if len(window) > 0 {
		if window[0] < 0 {
			return 0, 0, fmt.Errorf("%w: %s limit cannot be negative", domain.ErrInvalidArgument, subject)
		}
		limit = window[0]
	}
	if len(window) > 1 {
		if window[1] < 0 {
			return 0, 0, fmt.Errorf("%w: %s offset cannot be negative", domain.ErrInvalidArgument, subject)
		}
		offset = window[1]
	}
	return limit, offset, nil
}

// boundedListWindow refuses an unbounded read: an absent or zero limit becomes
// defaultListLimit and anything above maxListLimit is clamped down to it.
func boundedListWindow(subject string, window []int) (limit int, offset int, err error) {
	limit, offset, err = listWindow(subject, window)
	if err != nil {
		return 0, 0, err
	}
	if limit == 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	return limit, offset, nil
}

// appendWindow appends the LIMIT/OFFSET clause for a resolved window. SQLite
// requires a LIMIT before an OFFSET, and documents "LIMIT -1" as the way to
// skip rows without bounding the result.
func appendWindow(query string, args []any, limit, offset int) (string, []any) {
	switch {
	case limit > 0:
		query += " LIMIT ?"
		args = append(args, limit)
	case offset > 0:
		query += " LIMIT -1"
	default:
		return query, args
	}
	if offset > 0 {
		query += " OFFSET ?"
		args = append(args, offset)
	}
	return query, args
}

// idChunkSize bounds how many ids one set-based read binds at a time. SQLite
// has a hard ceiling on host parameters per statement, so a batch read splits
// its id list rather than failing on a large page. It is deliberately far
// above any page a caller can ask for, so the common case is one query.
const idChunkSize = 500

// chunkIDs normalizes an id set for a set-based read: blanks and duplicates are
// dropped (a repeated id would otherwise multiply the rows for one owner) and
// the remainder is split into statement-sized chunks.
func chunkIDs(ids []domain.ID) [][]domain.ID {
	seen := make(map[domain.ID]struct{}, len(ids))
	unique := make([]domain.ID, 0, len(ids))
	for _, id := range ids {
		if id.Empty() {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	chunks := make([][]domain.ID, 0, (len(unique)+idChunkSize-1)/idChunkSize)
	for start := 0; start < len(unique); start += idChunkSize {
		end := start + idChunkSize
		if end > len(unique) {
			end = len(unique)
		}
		chunks = append(chunks, unique[start:end])
	}
	return chunks
}

// idPlaceholders renders the "?, ?, …" body of an IN clause together with the
// arguments to bind to it.
func idPlaceholders(ids []domain.ID) (string, []any) {
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for index, id := range ids {
		marks[index] = "?"
		args[index] = string(id)
	}
	return strings.Join(marks, ", "), args
}

// boundedPerOwnerLimit clamps the per-owner limit of a set-based read the same
// way boundedListWindow clamps a single-owner list: absent means the default,
// negative is rejected, and an over-large request is clamped rather than
// honoured.
func boundedPerOwnerLimit(subject string, limit int) (int, error) {
	if limit < 0 {
		return 0, fmt.Errorf("%w: %s limit cannot be negative", domain.ErrInvalidArgument, subject)
	}
	if limit == 0 {
		return defaultListLimit, nil
	}
	if limit > maxListLimit {
		return maxListLimit, nil
	}
	return limit, nil
}
