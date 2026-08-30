package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// streamDeadline splits what used to be a single total-duration timeout into
// two independent budgets enforced over one cancelable request context:
//
//   - firstByte bounds connection setup, the request itself (including retry
//     backoff), and the wait for the first byte of the response body.
//   - idle bounds the gap between two consecutive reads of the response body.
//     It is re-armed on every byte received, so a long but healthy generation
//     keeps streaming for as long as the provider keeps producing data.
//
// A single total-duration timeout cannot express this: it aborts a stream that
// is actively delivering tokens, which the runtime then reports as a cancelled
// run. Only a stream that goes completely silent is cancelled here.
type streamDeadline struct {
	cancel    context.CancelFunc
	firstByte time.Duration
	idle      time.Duration

	mu        sync.Mutex
	timer     *time.Timer
	armedAt   time.Time
	budget    time.Duration
	stopped   bool
	fired     bool
	firedIdle bool
	sawData   bool
}

// newStreamDeadline arms the first-byte budget immediately. A non-positive
// budget disables that phase; the idle budget is armed on the first byte read.
func newStreamDeadline(cancel context.CancelFunc, firstByte, idle time.Duration) *streamDeadline {
	d := &streamDeadline{cancel: cancel, firstByte: firstByte, idle: idle}
	if firstByte > 0 {
		d.mu.Lock()
		d.arm(firstByte)
		d.mu.Unlock()
	}
	return d
}

// arm (re)schedules the watchdog. The caller must hold d.mu.
func (d *streamDeadline) arm(budget time.Duration) {
	d.budget = budget
	d.armedAt = time.Now()
	if d.timer == nil {
		d.timer = time.AfterFunc(budget, d.expire)
		return
	}
	d.timer.Reset(budget)
}

// activity records progress on the response body: it retires the first-byte
// budget and re-arms the idle budget.
func (d *streamDeadline) activity() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped || d.fired {
		return
	}
	d.sawData = true
	if d.idle <= 0 {
		// No idle budget configured: the body may take as long as it needs.
		if d.timer != nil {
			d.timer.Stop()
		}
		d.budget = 0
		return
	}
	d.arm(d.idle)
}

// stop disarms the watchdog without cancelling the request context. It is
// idempotent and safe to call from any path, including after expiry.
func (d *streamDeadline) stop() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.stopped = true
	if d.timer != nil {
		d.timer.Stop()
	}
	d.mu.Unlock()
}

// release disarms the watchdog and cancels the request context. It is the
// CancelFunc handed to the stream, so closing the stream can never leak the
// context or leave a timer goroutine scheduled against it.
func (d *streamDeadline) release() {
	if d == nil {
		return
	}
	d.stop()
	if d.cancel != nil {
		d.cancel()
	}
}

func (d *streamDeadline) expire() {
	d.mu.Lock()
	if d.stopped || d.fired {
		d.mu.Unlock()
		return
	}
	if remaining := d.budget - time.Since(d.armedAt); remaining > 0 {
		// A concurrent activity() re-armed the timer after this callback was
		// already scheduled. Wait out the remainder instead of cancelling a
		// stream that just delivered data.
		d.arm(remaining)
		d.mu.Unlock()
		return
	}
	d.fired = true
	d.firedIdle = d.sawData
	d.stopped = true
	if d.timer != nil {
		d.timer.Stop()
	}
	d.mu.Unlock()
	if d.cancel != nil {
		d.cancel()
	}
}

// expired reports whether this watchdog — rather than the caller's context —
// aborted the request.
func (d *streamDeadline) expired() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fired
}

// err returns the typed error describing an expiry, or nil when the watchdog
// never fired. A stalled stream is not retryable because partial output was
// already handed to the caller; a first-byte timeout is.
func (d *streamDeadline) err() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	fired, idlePhase := d.fired, d.firedIdle
	firstByte, idle := d.firstByte, d.idle
	d.mu.Unlock()
	if !fired {
		return nil
	}
	if idlePhase {
		return providerError(ErrorKindTimeout, "stream", 0,
			fmt.Sprintf("stream stalled: no data received for %s", idle), false, 0)
	}
	return providerError(ErrorKindTimeout, "request", 0,
		fmt.Sprintf("timed out after %s waiting for the first response byte", firstByte), true, 0)
}

// activityBody re-arms the idle budget on every byte the provider delivers and
// translates a watchdog-triggered read failure into the typed timeout error
// instead of an opaque "context canceled" transport message.
type activityBody struct {
	body     io.ReadCloser
	deadline *streamDeadline
}

func (b *activityBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.deadline.activity()
	}
	if err == nil {
		return n, nil
	}
	if errors.Is(err, io.EOF) {
		// The provider closed the body: no further data can arrive, so disarm
		// the watchdog and release the context immediately rather than leaving
		// a child context attached to the caller's until Close runs.
		b.deadline.release()
		return n, err
	}
	if timeoutErr := b.deadline.err(); timeoutErr != nil {
		return n, timeoutErr
	}
	return n, err
}

func (b *activityBody) Close() error { return b.body.Close() }
