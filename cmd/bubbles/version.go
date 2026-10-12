package main

import (
	"fmt"
	"os"

	"github.com/donaldsteele/bubbles/internal/buildinfo"
)

// runVersion prints "bubbles <Version>" to stdout.
func runVersion(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: bubbles version")
		return 2
	}
	fmt.Fprintf(os.Stdout, "bubbles %s\n", buildinfo.Version)
	return 0
}
