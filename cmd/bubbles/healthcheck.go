package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/tlsca"
)

const (
	defaultHealthURL = "https://127.0.0.1:8443"
	healthUsage      = "usage: bubbles healthcheck [--url https://127.0.0.1:8443] [--data-dir D]"
)

func runHealthcheck(args []string) int {
	return healthcheckCmd(context.Background(), args, config.Options{}, os.Stdout, os.Stderr)
}

// healthcheckCmd GETs /healthz trusting only the local CA from the data
// directory. Exit 0 when the server answers 200, 1 otherwise (Docker HEALTHCHECK).
func healthcheckCmd(ctx context.Context, args []string, opts config.Options, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bubbles healthcheck", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataDir := fs.String("data-dir", "", "data directory holding the CA (env "+config.EnvName("data-dir")+")")
	url := fs.String("url", defaultHealthURL, "server base URL")
	if code, done := parseSub(fs, "healthcheck", args, stderr, healthUsage); done {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "bubbles healthcheck: unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}
	dir, err := resolveDataDir(*dataDir, opts)
	if err != nil {
		fmt.Fprintf(stderr, "bubbles healthcheck: %v\n", err)
		return exitUsage
	}
	pem, err := os.ReadFile(filepath.Join(dir, config.DirTLS, tlsca.CACertFile))
	if err != nil {
		fmt.Fprintf(stderr, "bubbles healthcheck: read CA: %v\n", err)
		return exitRuntime
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		fmt.Fprintln(stderr, "bubbles healthcheck: CA certificate is not valid PEM")
		return exitRuntime
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(*url, "/")+"/healthz", nil)
	if err != nil {
		fmt.Fprintf(stderr, "bubbles healthcheck: %v\n", err)
		return exitUsage
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "bubbles healthcheck: %v\n", err)
		return exitRuntime
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "bubbles healthcheck: unhealthy: HTTP %d\n", resp.StatusCode)
		return exitRuntime
	}
	fmt.Fprintln(stdout, "ok")
	return exitOK
}
