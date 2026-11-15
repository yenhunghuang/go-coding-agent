//go:build windows

package execx

import (
	"os"
	"os/exec"
	"time"
)

// killCode is reported when the OS hands back no process status at all.
const killCode = -1

// configure cancels the command when its context ends. Windows has no process
// groups here, so only the direct child is killed; that is why the bash tool is
// documented as a teaching tool, not a sandbox.
func configure(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
}

func exitCode(ps *os.ProcessState) int {
	if ps == nil {
		return killCode
	}
	return ps.ExitCode()
}
