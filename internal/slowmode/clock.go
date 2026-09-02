package slowmode

import "time"

// Clock makes queue timing deterministic in tests.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

// Timer is the subset of time.Timer used by Coordinator.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type realClock struct{}

func (realClock) Now() time.Time                     { return time.Now() }
func (realClock) NewTimer(delay time.Duration) Timer { return realTimer{Timer: time.NewTimer(delay)} }

type realTimer struct{ *time.Timer }

func (timer realTimer) C() <-chan time.Time { return timer.Timer.C }

// Jitter returns a value in [0, upperBound]. Tests normally inject a
// deterministic implementation.
type Jitter func(upperBound time.Duration) time.Duration
