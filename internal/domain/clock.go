package domain

import "time"

// Clock is a small seam for deterministic application and domain tests.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the local process clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// FixedClock is useful for callers that need deterministic timestamps. It is
// deliberately immutable; construct a new value when the time should change.
type FixedClock struct {
	At time.Time
}

func (c FixedClock) Now() time.Time { return c.At }
