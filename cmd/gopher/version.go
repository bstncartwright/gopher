package main

import (
	"fmt"
	"io"
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
	fmt.Fprintf(out, "gopher %s\n", currentBinaryVersion())
}
