package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/service"
)

const serviceUsage = "usage: bubbles service install|uninstall|start|stop|status [--data-dir D] [--https-addr A] [--http-addr A]"

func runService(args []string) int {
	return serviceCmd(context.Background(), args, config.Options{AsService: true}, service.New(service.Options{}), os.Stdout, os.Stderr)
}

// runWindowsService runs the server under the Windows service manager. The
// SCM passes the install-time arguments ("serve --data-dir ...").
func runWindowsService() int {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	err := service.RunAsService(func(ctx context.Context) int {
		return serve(ctx, args, config.Options{AsService: true}, io.Discard, io.Discard)
	})
	if err != nil {
		return exitRuntime
	}
	return exitOK
}

func serviceCmd(ctx context.Context, args []string, opts config.Options, mgr service.Manager, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, serviceUsage)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "install", "uninstall", "start", "stop", "status":
	default:
		fmt.Fprintf(stderr, "bubbles service: unknown subcommand %q\n%s\n", sub, serviceUsage)
		return exitUsage
	}
	fs := flag.NewFlagSet("bubbles service "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataDir := fs.String("data-dir", "", "service data directory (env "+config.EnvName("data-dir")+")")
	httpsAddr := fs.String("https-addr", "", "HTTPS listen address passed to the service (default :28225)")
	httpAddr := fs.String("http-addr", "", "onboarding HTTP listen address passed to the service (default :28226)")
	if code, done := parseSub(fs, "service "+sub, rest, stderr, serviceUsage); done {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "bubbles service %s: unexpected argument %q\n", sub, fs.Arg(0))
		return exitUsage
	}
	var err error
	switch sub {
	case "install":
		var spec service.Spec
		if spec, err = service.DefaultSpec(); err == nil {
			spec.DataDir, err = resolveDataDir(*dataDir, opts)
			spec.HTTPSAddr, spec.HTTPAddr = *httpsAddr, *httpAddr
		}
		if err == nil {
			err = mgr.Install(ctx, spec)
		}
		if err == nil {
			fmt.Fprintf(stdout, "service installed and started (data directory %s)\n", spec.DataDir)
		}
	case "uninstall":
		if err = mgr.Uninstall(ctx); err == nil {
			fmt.Fprintln(stdout, "service removed (data directory kept)")
		}
	case "start":
		if err = mgr.Start(ctx); err == nil {
			fmt.Fprintln(stdout, "service started")
		}
	case "stop":
		if err = mgr.Stop(ctx); err == nil {
			fmt.Fprintln(stdout, "service stopped")
		}
	case "status":
		var st string
		if st, err = mgr.Status(ctx); err == nil {
			fmt.Fprintln(stdout, st)
			if strings.Contains(st, "not installed") {
				return exitRuntime
			}
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "bubbles service %s: %v\n", sub, err)
		return exitRuntime
	}
	return exitOK
}
