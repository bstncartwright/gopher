package main

import (
	"errors"
	"io/fs"
	"strings"
)

var permissionErrorSubstrings = []string{
	"permission denied",
	"operation not permitted",
	"access denied",
	"interactive authentication required",
	"authentication is required",
	"must be superuser",
	"polkit",
}

func isLikelyPermissionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrPermission) {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	for _, pattern := range permissionErrorSubstrings {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}
