package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/donaldsteele/bubbles/internal/buildinfo"
	"github.com/donaldsteele/bubbles/internal/mcpserver"
	"github.com/donaldsteele/bubbles/pkg/bubblesclient"
)

// connEnv names the environment variable that carries the connection string,
// so the token need not appear in the process arguments.
const connEnv = "BUBBLES_CONN"

// runMCP implements `bubbles mcp [--conn bubbles://…]`, a stdio MCP server
// that forwards to a Bubbles server with an app token.
func runMCP(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return mcpCmd(ctx, args, os.Getenv, os.Stdin, os.Stdout, os.Stderr)
}

// nopWriteCloser lets the MCP transport leave the process's stdout open.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// mcpCmd serves MCP on in/out. Nothing but MCP frames is written to out;
// diagnostics go to errOut and never contain the token.
func mcpCmd(ctx context.Context, args []string, getenv func(string) string, in io.Reader, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("bubbles mcp", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		fmt.Fprintln(errOut, `usage: bubbles mcp [--conn "bubbles://host:28225/?token=…&fp=sha256:…"]`)
		fmt.Fprintf(errOut, "       The connection string may also be given in $%s, which keeps the token out of the process list.\n", connEnv)
	}
	conn := fs.String("conn", "", "connection string of the app token")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	s := strings.TrimSpace(*conn)
	if s == "" {
		s = strings.TrimSpace(getenv(connEnv))
	}
	if s == "" {
		fmt.Fprintf(errOut, "bubbles mcp: give --conn or set %s\n", connEnv)
		fs.Usage()
		return 2
	}
	cfg, err := bubblesclient.ParseConnectionString(s)
	if err != nil {
		fmt.Fprintf(errOut, "bubbles mcp: invalid connection string: %s\n", redact(err.Error(), s, ""))
		return 2
	}
	if cfg.CAFingerprint == "" {
		fmt.Fprintln(errOut, "bubbles mcp: the connection string has no fp= (CA fingerprint); refusing to connect without pinning the server's CA")
		return 2
	}
	cfg.UserAgent = "bubbles-mcp/" + buildinfo.Version
	client, err := bubblesclient.New(cfg)
	if err != nil {
		fmt.Fprintf(errOut, "bubbles mcp: %s\n", redact(err.Error(), s, cfg.Token))
		return 2
	}
	srv := mcpserver.New(mcpserver.NewRemote(client), mcpserver.Info{Version: buildinfo.Version})
	rc, ok := in.(io.ReadCloser)
	if !ok {
		rc = io.NopCloser(in)
	}
	if err := srv.Run(ctx, &mcp.IOTransport{Reader: rc, Writer: nopWriteCloser{out}}); err != nil && ctx.Err() == nil {
		fmt.Fprintf(errOut, "bubbles mcp: %s\n", redact(err.Error(), s, cfg.Token))
		return 1
	}
	return 0
}

// redact removes the connection string and the token from a message.
func redact(msg string, secrets ...string) string {
	for _, s := range secrets {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "[redacted]")
		}
	}
	return msg
}
