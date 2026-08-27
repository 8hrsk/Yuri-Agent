//go:build windows

package plugins

import "os/exec"

func configureProcess(*exec.Cmd) {}

func killProcess(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}
