// Command bubbles is the single-binary chat server and its admin tooling.
package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/donaldsteele/bubbles/internal/service"
)

// command runs a subcommand with the arguments that follow its name and
// returns the process exit code.
type command func(args []string) int

// commands maps subcommand names to their implementations.
var commands = map[string]command{
	"admin":       runAdmin,
	"backup":      runBackup,
	"cert":        runCert,
	"healthcheck": runHealthcheck,
	"mcp":         runMCP,
	"reset":       runReset,
	"restore":     runRestore,
	"serve":       runServe,
	"service":     runService,
	"version":     runVersion,
}

func main() {
	if service.IsWindowsService() {
		os.Exit(runWindowsService())
	}
	os.Exit(dispatch(os.Args[1:], os.Stderr))
}

// dispatch routes args to a registered subcommand. With no arguments, or
// when the first argument is a flag, it runs `serve` (the CLI contract:
// `bubbles` = `bubbles serve`). Unknown commands print usage to stderr and
// return exit code 2.
func dispatch(args []string, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return commands["serve"](args)
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "bubbles: unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
	return cmd(args[1:])
}

func usage(w io.Writer) {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Fprintln(w, "usage: bubbles [command] [arguments]   (no command = serve)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	for _, name := range names {
		fmt.Fprintf(w, "  %s\n", name)
	}
}
