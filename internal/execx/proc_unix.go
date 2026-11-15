//go:build unix

package execx

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// killCode is reported when the OS hands back no process status at all.
const killCode = -9 // SIGKILL

// configure puts the child in its own process group so the deadline can kill
// everything the command spawned, not only the shell itself.
func configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			// Negative pid means the whole group. The result is ignored: the
			// group may already be gone, which is what we wanted anyway.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	// Do not let a stray grandchild holding the pipes keep Wait blocked after
	// the process group is dead.
	cmd.WaitDelay = 2 * time.Second
}

// exitCode follows the agent's convention: the command's own status, or a
// negative signal number when a signal killed it.
func exitCode(ps *os.ProcessState) int {
	if ps == nil {
		return killCode
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			return -int(ws.Signal())
		}
		return ws.ExitStatus()
	}
	return ps.ExitCode()
}
