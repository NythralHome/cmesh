//go:build windows

package workercontrol

import (
	"os"
	"os/exec"
)

func configureWorkerCommand(_ *exec.Cmd) {}

func interruptWorkerProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(os.Interrupt)
}

func killWorkerProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
