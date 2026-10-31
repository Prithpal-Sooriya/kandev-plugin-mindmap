//go:build windows

package share

import (
	"os"
	"os/exec"
)

func setGroup(cmd *exec.Cmd) {}

func killGroup(pid int) {
	if pid <= 0 {
		return
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = p.Kill()
}

func alive(pid int) bool {
	// FindProcess on Windows does not tell us the process is still running.
	// Health uses the wait goroutine; this is only a fallback.
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	return err == nil && p != nil
}
