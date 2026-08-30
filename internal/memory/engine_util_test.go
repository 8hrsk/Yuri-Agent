package memory

import (
	"errors"
	"fmt"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// N-6. errorsIsNotFound used to compare with ==, so a storage or archive
// adapter that wrapped domain.ErrNotFound — the ordinary Go idiom — was not
// recognized. The one caller (engine_write.go) uses this to decide that a
// create candidate has no prior record; a missed match turns a normal create
// into a failed write.
//
// The wrapped case is the whole test: an unwrapped sentinel passes under both
// == and errors.Is and would prove nothing. The string fallback in the helper
// is deliberately not exercised by the wrapped case, because a wrapped error's
// message still contains the sentinel text — so the assertion below uses a
// sentinel wrapped by a message that would also have matched, and the
// double-wrapped `errors.Is`-only case pins the real behaviour.
func TestErrorsIsNotFoundMatchesAWrappedSentinel(t *testing.T) {
	if !errorsIsNotFound(domain.ErrNotFound) {
		t.Fatal("the bare sentinel must be recognized")
	}
	wrapped := fmt.Errorf("read memory: %w", domain.ErrNotFound)
	if !errorsIsNotFound(wrapped) {
		t.Fatal("a wrapped domain.ErrNotFound must be recognized")
	}
	// hiddenNotFound reports Is/Unwrap correctly but deliberately does not put
	// the sentinel's text in its own message, so only errors.Is can see it.
	// Under the old == comparison, and with the string fallback, this fails.
	if !errorsIsNotFound(hiddenNotFound{}) {
		t.Fatal("a sentinel visible only through errors.Is must be recognized")
	}
	if errorsIsNotFound(nil) {
		t.Fatal("a nil error is not a missing record")
	}
	if errorsIsNotFound(errors.New("database is locked")) {
		t.Fatal("an unrelated error must not be reported as a missing record")
	}
}

type hiddenNotFound struct{}

func (hiddenNotFound) Error() string { return "archive lookup failed" }
func (hiddenNotFound) Unwrap() error { return domain.ErrNotFound }
