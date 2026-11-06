//go:build !windows

package core

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// terminateNocturne stops a stuck NOCTURNE server, but only after checking
// that the process really is NOCTURNE (the pid could have been reused).
func terminateNocturne(pid int) {
	if pid <= 1 {
		return
	}
	out, err := exec.Command("/bin/ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return
	}
	if !strings.Contains(strings.ToLower(string(out)), "nocturne") {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
}
