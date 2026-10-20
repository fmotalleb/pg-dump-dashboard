//go:build !unix

package osprocess

import "os/exec"

// configureCommand is a no-op on platforms without POSIX process groups.
func configureCommand(*exec.Cmd) {}

// terminate kills the direct child only.
func terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
