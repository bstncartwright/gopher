//go:build !windows

package main

import (
	"errors"
	"syscall"
)

func gatewayProcessIsRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return !errors.Is(err, syscall.ESRCH)
}
