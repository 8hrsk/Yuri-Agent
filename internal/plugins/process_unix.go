//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package plugins

import (
	"os/exec"
	"syscall"
)

// Plugins are placed in their own process group so a forced stop also
// terminates descendants spawned by a plugin, rather than leaving a child
// process behind after the host has closed the RPC session.
func configureProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcess(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return command.Process.Kill()
}
