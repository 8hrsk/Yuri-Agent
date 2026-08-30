package plugins

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
)

// panicError carries a value recovered from a plugin runtime goroutine so the
// failure travels the same reporting path as any other terminal error.
type panicError struct {
	value any
	stack []byte
}

func (e *panicError) Error() string {
	return fmt.Sprintf("внутренняя ошибка исполнения плагина: %v", e.value)
}

// recoverPluginGoroutine turns a panic in a goroutine this package owns into a
// logged, reported failure instead of a process-wide crash.
//
// This mirrors the contract of the desktop bridge's recoverBridgeGoroutine, but
// deliberately does not reuse it: internal/plugins is the lower layer and must
// not import internal/desktop, and the bridge helper is a *Bridge method bound
// to the bridge's logger and durable stores. The contract that is shared is the
// one that matters:
//
//   - the panic is logged with its stack;
//   - report drives the affected object — a Client session or a Supervisor —
//     into an honest terminal state, wakes everyone parked on it, and makes
//     sure no child process is left alive holding the owner's granted
//     capabilities. report is not optional decoration: a recovery that only
//     logged would leave a plugin marked running with nobody reading its stdout
//     and callers blocked forever, which is indistinguishable from a hang;
//   - a panic inside report itself is contained, so a failing reporter cannot
//     re-panic the goroutine it was meant to rescue.
//
// The package has no injected logger, so the process-wide slog default is used.
// The panic value is passed through redactDiagnostics for the same reason the
// stderr tail is: it can quote bytes a third-party plugin chose to send.
func recoverPluginGoroutine(name string, report func(error)) {
	recovered := recover()
	if recovered == nil {
		return
	}
	err := &panicError{value: recovered, stack: debug.Stack()}
	slog.Error("panic in plugin runtime goroutine",
		"goroutine", name,
		"panic", redactDiagnostics(fmt.Sprint(recovered)),
		"stack", string(err.stack))
	if report == nil {
		return
	}
	defer func() {
		if nested := recover(); nested != nil {
			slog.Error("panic while reporting a recovered plugin runtime panic",
				"goroutine", name,
				"panic", redactDiagnostics(fmt.Sprint(nested)))
		}
	}()
	report(err)
}

// Fault-injection sites. There is no reachable panic in this package today —
// see the classification in docs/REMEDIATION_PLAN.md (N-13) — so the recovery
// contract can only be exercised by injecting one. The hook below is the seam
// that makes that possible. It is unexported, nil in production, and read
// through an atomic pointer so a test that installs it races nothing.
const (
	faultReadStdout        = "read_stdout"
	faultWaitProcess       = "wait_process"
	faultWriteLoop         = "write_loop"
	faultCloseOnContext    = "close_on_context"
	faultSupervisorWatch   = "supervisor_watch"
	faultSupervisorRestart = "supervisor_restart"
)

var goroutineFaultHook atomic.Pointer[func(site string)]

func fireFaultHook(site string) {
	if hook := goroutineFaultHook.Load(); hook != nil {
		(*hook)(site)
	}
}
