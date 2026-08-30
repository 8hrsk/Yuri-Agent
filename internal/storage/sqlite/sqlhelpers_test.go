package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

// N-6. isNoRows used to compare with ==, so a wrapped sql.ErrNoRows slipped
// past it and every caller that asks "was this simply absent?" reported an
// unexpected internal error instead.
//
// The wrapped case is the whole test: an unwrapped sentinel passes under both
// == and errors.Is and would prove nothing.
func TestIsNoRowsMatchesAWrappedSentinel(t *testing.T) {
	if !isNoRows(sql.ErrNoRows) {
		t.Fatal("the bare sentinel must be recognized")
	}
	wrapped := fmt.Errorf("load plugin row: %w", sql.ErrNoRows)
	if !isNoRows(wrapped) {
		t.Fatal("a wrapped sql.ErrNoRows must be recognized as an absent row")
	}
	doubleWrapped := fmt.Errorf("repository: %w", wrapped)
	if !isNoRows(doubleWrapped) {
		t.Fatal("a twice-wrapped sql.ErrNoRows must still be recognized")
	}
	if isNoRows(nil) {
		t.Fatal("a nil error is not an absent row")
	}
	if isNoRows(errors.New("connection reset")) {
		t.Fatal("an unrelated error must not be reported as an absent row")
	}
	if isNoRows(fmt.Errorf("query: %w", sql.ErrConnDone)) {
		t.Fatal("a different wrapped sentinel must not be reported as an absent row")
	}
}
