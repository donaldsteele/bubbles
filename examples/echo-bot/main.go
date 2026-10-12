// Command echo-bot registers a /echo slash command and answers every
// invocation with its arguments, visible only to the person who ran it.
//
//	echo-bot --conn 'bubbles://192.168.1.20:28225/?token=bub_...&fp=sha256:...'
//
// The token needs the commands and events:read scopes.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/pkg/bubblesclient"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func main() {
	conn := flag.String("conn", os.Getenv("BUBBLES_CONN"), "connection string (or env BUBBLES_CONN)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, *conn); err != nil {
		fmt.Fprintln(os.Stderr, "echo-bot:", err)
		if errors.Is(err, bubblesclient.ErrUnauthorized) || errors.Is(err, bubblesclient.ErrServerIdentityChanged) {
			os.Exit(3) // re-pair required
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, conn string) error {
	cfg, err := bubblesclient.ParseConnectionString(conn)
	if err != nil {
		return err
	}
	c, err := bubblesclient.New(cfg)
	if err != nil {
		return err
	}
	if err := c.RegisterCommands(ctx, []protocol.Command{{
		Name: "echo", Description: "Repeat the text back to you", Usage: "/echo <text>",
	}}); err != nil {
		return err
	}
	events, err := c.Events(ctx)
	if err != nil {
		return err
	}
	fmt.Println("echo-bot ready: /echo registered")
	for ev := range events {
		if ev.Type != "command.invoked" {
			continue
		}
		var inv protocol.CommandInvoked
		if json.Unmarshal(ev.Data, &inv) != nil || inv.Command != "echo" {
			continue
		}
		text := strings.TrimSpace(inv.Args)
		if text == "" {
			text = "(nothing to echo)"
		}
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := c.Respond(rctx, inv.InvocationID, protocol.CommandResponse{Text: text, Ephemeral: true})
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "respond failed:", err)
		}
	}
	if err := c.EventsErr(); err != nil {
		return err
	}
	return nil
}
