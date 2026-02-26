//go:build windows

package main

func gatewayProcessIsRunning(pid int) bool {
	return pid > 0
}
