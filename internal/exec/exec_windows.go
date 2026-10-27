//go:build windows

package exec

import (
	"os/exec"
	"strconv"
	"syscall"
)

// setSysProcAttr puts the child in its own process group so that the kill
// below can address the whole tree it spawns (codex and claude both start
// helper processes).
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killProcessGroup terminates the child and all of its descendants.
// Windows has no process-group kill in the POSIX sense; `taskkill /T` walks
// the tree. Fall back to killing the direct child if taskkill is missing.
func killProcessGroup(cmd *exec.Cmd) {
	pid := strconv.Itoa(cmd.Process.Pid)
	if err := exec.Command("taskkill", "/T", "/F", "/PID", pid).Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}
