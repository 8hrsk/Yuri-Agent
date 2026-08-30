package desktop

import (
	"fmt"
	"runtime/debug"
)

// panicError carries a value recovered from a bridge goroutine so that the
// failure can travel the same reporting path as any other terminal error.
type panicError struct {
	value any
	stack []byte
}

func (e *panicError) Error() string {
	return fmt.Sprintf("внутренняя ошибка выполнения: %v", e.value)
}

// recoverBridgeGoroutine turns a panic in a goroutine the bridge owns into a
// logged, reported failure instead of a process-wide crash.
//
// The desktop bridge is the trust boundary of a single-owner application: one
// data anomaly in a background dialogue, one memory pass or one provider launch
// must not take the owner's whole session with it.
//
// report is not optional decoration. A recovery that only logged would leave
// the owner looking at a run that stopped producing events, which is
// indistinguishable from a hang; every call site either moves its durable
// object into a terminal failed state here or documents why it owns no such
// state. A panic inside report itself is contained too, so a failing reporter
// cannot re-panic the goroutine it was meant to rescue.
func (b *Bridge) recoverBridgeGoroutine(name string, report func(error)) {
	recovered := recover()
	if recovered == nil {
		return
	}
	err := &panicError{value: recovered, stack: debug.Stack()}
	if b != nil && b.logger != nil {
		b.logger.Error("panic in bridge goroutine",
			"goroutine", name,
			"panic", safeError(fmt.Sprint(recovered)),
			"stack", string(err.stack))
	}
	if report == nil {
		return
	}
	defer func() {
		if nested := recover(); nested != nil && b != nil && b.logger != nil {
			b.logger.Error("panic while reporting a recovered bridge panic",
				"goroutine", name, "panic", safeError(fmt.Sprint(nested)))
		}
	}()
	report(err)
}
