package main

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

var binaryVersion = "dev"

func currentBinaryVersion() string {
	value := strings.TrimSpace(binaryVersion)
	if value == "" {
		return "dev"
	}
	return value
}

func printBinaryVersion(out io.Writer) {
	slog.Debug("version: printing binary version", "version", currentBinaryVersion())
	fmt.Fprintf(out, "gopher %s\n", currentBinaryVersion())
}
