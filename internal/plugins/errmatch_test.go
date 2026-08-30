package plugins

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

// maskedSentinel wraps a sentinel but reports a message that shares no text
// with it.  errors.Is still identifies it; substring matching cannot.
type maskedSentinel struct{ inner error }

func (e maskedSentinel) Error() string { return "supervisor: shutdown handshake abandoned" }

func (e maskedSentinel) Unwrap() error { return e.inner }

// quoting builds the shape that makes substring matching actively dangerous: a
// genuine, unrelated failure whose message happens to quote a sentinel's text.
func quoting(sentinel error) error {
	return errors.New("shutdown rpc failed and will not be retried after " + strconv.Quote(sentinel.Error()))
}

// N-16. isIgnorableStopError decides whether a shutdown failure is dropped
// from Supervisor.lastErr, so it must identify the sentinels by identity.
// Both directions are asserted: a sentinel whose message does not contain the
// sentinel text must still be recognized, and a non-sentinel whose message
// does contain it must be rejected.
func TestIsIgnorableStopErrorMatchesBySentinelNotMessage(t *testing.T) {
	for _, sentinel := range []error{ErrPluginExited, ErrPluginNotReady} {
		masked := maskedSentinel{inner: sentinel}
		if strings.Contains(masked.Error(), sentinel.Error()) {
			t.Fatalf("test setup: masked message %q must not contain %q", masked.Error(), sentinel.Error())
		}
		if !isIgnorableStopError(masked) {
			t.Errorf("isIgnorableStopError(masked %v) = false, want true: the error is the sentinel even though its message never says so", sentinel)
		}

		impostor := quoting(sentinel)
		if !strings.Contains(impostor.Error(), sentinel.Error()) {
			t.Fatalf("test setup: impostor message %q must contain %q", impostor.Error(), sentinel.Error())
		}
		if errors.Is(impostor, sentinel) {
			t.Fatalf("test setup: impostor must not be %v", sentinel)
		}
		if isIgnorableStopError(impostor) {
			t.Errorf("isIgnorableStopError(%q) = true, want false: a real failure that merely quotes %v must not be discarded", impostor, sentinel)
		}

		if !isIgnorableStopError(fmt.Errorf("%w: wrapped as producers do", sentinel)) {
			t.Errorf("isIgnorableStopError(%%w-wrapped %v) = false, want true", sentinel)
		}
	}
	if !isIgnorableStopError(nil) {
		t.Error("isIgnorableStopError(nil) = false, want true")
	}
	if isIgnorableStopError(errors.New("disk quota exceeded")) {
		t.Error("isIgnorableStopError(unrelated) = true, want false")
	}
}

// errReader hands readStdout a read failure of our choosing without any
// process behind it.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// readStdoutClient builds the minimum Client that readStdout and finish touch:
// no process, no pipes.
func readStdoutClient(maxMessageBytes int) *Client {
	return &Client{
		// CloseTimeout is generous so the clean-EOF fallback timer cannot fire
		// while a subtest is still inspecting the client.
		config:  ClientConfig{MaxMessageBytes: maxMessageBytes, CloseTimeout: time.Minute},
		pending: make(map[string]chan response),
		events:  make(chan Envelope, 1), done: make(chan struct{}),
		stdoutDone: make(chan struct{}),
	}
}

// N-16. readStdout classified an oversized JSONL line by looking for the words
// "token too long" in the scanner's error.  bufio.ErrTooLong is a sentinel, so
// errors.Is is exact; the substring also fired on unrelated read failures that
// happened to contain the phrase, reporting a transport failure as a protocol
// size violation.
func TestReadStdoutClassifiesOversizeLineBySentinel(t *testing.T) {
	t.Run("real oversized line is still ErrMessageTooLarge", func(t *testing.T) {
		client := readStdoutClient(64)
		client.readStdout(strings.NewReader(strings.Repeat("x", 4096) + "\n"))
		err := client.Err()
		if !errors.Is(err, ErrMessageTooLarge) {
			t.Fatalf("Err() = %v, want ErrMessageTooLarge", err)
		}
	})

	t.Run("unrelated read failure quoting the phrase is not ErrMessageTooLarge", func(t *testing.T) {
		impostor := errors.New("read |0: token too long for the retry window")
		if errors.Is(impostor, bufio.ErrTooLong) {
			t.Fatal("test setup: impostor must not be bufio.ErrTooLong")
		}
		if !strings.Contains(strings.ToLower(impostor.Error()), "token too long") {
			t.Fatal("test setup: impostor must contain the phrase the old code matched")
		}
		client := readStdoutClient(64)
		client.readStdout(errReader{err: impostor})
		err := client.Err()
		if errors.Is(err, ErrMessageTooLarge) {
			t.Fatalf("Err() = %v, want a transport failure: a read error is not a protocol size violation", err)
		}
		if !errors.Is(err, ErrPluginExited) {
			t.Fatalf("Err() = %v, want ErrPluginExited", err)
		}
	})

	t.Run("clean EOF is unaffected", func(t *testing.T) {
		client := readStdoutClient(64)
		client.readStdout(errReader{err: io.EOF})
		select {
		case <-client.done:
			t.Fatal("a clean EOF must not terminate the session from readStdout")
		default:
		}
	})
}
