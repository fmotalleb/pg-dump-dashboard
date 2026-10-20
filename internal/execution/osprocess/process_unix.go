//go:build unix

package osprocess

import (
	"os/exec"
	"syscall"
)

// configureCommand puts the child in its own process group so termination
// reaches helper processes too.
func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate sends SIGKILL to the whole process group. SIGKILL is used because
// pg_dump must not be able to survive cancellation while flushing.
func terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		// The group may already be gone; fall back to the process itself.
		_ = cmd.Process.Kill()
	}
}
