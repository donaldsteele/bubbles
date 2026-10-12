package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// BuiltinCommands lists the slash commands the server runs itself.
func BuiltinCommands() []protocol.Command {
	return []protocol.Command{
		{Name: protocol.CommandTopic, Description: "Set the channel topic", Usage: "/topic <text>", Builtin: true},
		{Name: protocol.CommandInvite, Description: "Add a person to the channel", Usage: "/invite @user", Builtin: true},
		{Name: protocol.CommandLeave, Description: "Leave the channel", Usage: "/leave", Builtin: true},
		{Name: protocol.CommandMute, Description: "Mute the channel (30m, 2h, 1d, until-tomorrow or off)", Usage: "/mute [duration]", Builtin: true},
		{Name: protocol.CommandMe, Description: "Post an action message", Usage: "/me <text>", Builtin: true},
		{Name: protocol.CommandHelp, Description: "List the available commands", Usage: "/help", Builtin: true},
	}
}

// IsBuiltin reports whether name is a built-in command.
func IsBuiltin(name string) bool {
	for _, c := range BuiltinCommands() {
		if c.Name == name {
			return true
		}
	}
	return false
}

// BuiltinInput are the inputs of RunBuiltin.
type BuiltinInput struct {
	Command      string
	Args         string
	ChannelID    string
	ThreadRootID string
	// Catalog is the command list /help shows; empty means the built-ins.
	Catalog []protocol.Command
}

// BuiltinResult is what a built-in command wants shown to its invoker: a
// private notice, empty when the command needs none.
type BuiltinResult struct {
	Notice string
}

// Mute durations: the default of a bare /mute and the longest accepted.
const (
	defaultMute = time.Hour
	maxMute     = 365 * 24 * time.Hour
)

// RunBuiltin runs a built-in command as actor. A command the actor may not
// run, or ran wrongly, is not an error: the reason comes back as the notice.
// Only an infrastructure failure is returned as an error.
func (s *Service) RunBuiltin(ctx context.Context, actor Actor, in BuiltinInput) (BuiltinResult, error) {
	args := strings.TrimSpace(in.Args)
	if in.Command == protocol.CommandHelp {
		return BuiltinResult{Notice: helpText(in.Catalog)}, nil
	}
	notice, err := run(ctx, s, func(tx *store.Tx) (string, error) {
		return s.runBuiltinTx(tx, actor, in, args)
	})
	if err != nil {
		if msg, ok := noticeFor(err); ok {
			return BuiltinResult{Notice: msg}, nil
		}
		return BuiltinResult{}, err
	}
	return BuiltinResult{Notice: notice}, nil
}

func (s *Service) runBuiltinTx(tx *store.Tx, actor Actor, in BuiltinInput, args string) (string, error) {
	ctx := tx.Context()
	switch in.Command {
	case protocol.CommandTopic:
		if args == "" {
			return "Usage: /topic <text>", nil
		}
		_, err := s.UpdateChannelTx(tx, actor, in.ChannelID, UpdateChannelParams{Topic: &args})
		return "", err
	case protocol.CommandInvite:
		name := strings.ToLower(strings.TrimPrefix(args, "@"))
		if name == "" || strings.ContainsAny(name, " \t\n") {
			return "Usage: /invite @user", nil
		}
		var id string
		err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ? AND deactivated_at IS NULL`, name).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "No such user: @" + name, nil
		}
		if err != nil {
			return "", fmt.Errorf("find user: %w", err)
		}
		_, err = s.AddMemberTx(tx, actor, in.ChannelID, id, protocol.MemberMember)
		return "", err
	case protocol.CommandLeave:
		return "", s.LeaveChannelTx(tx, actor, in.ChannelID)
	case protocol.CommandMute:
		now := time.UnixMilli(tx.Now())
		upd := MembershipUpdate{}
		switch strings.ToLower(args) {
		case "off", "none":
			upd.MutedUntil = Clear[time.Time]()
		default:
			until, ok := parseMute(args, now)
			if !ok {
				return "Usage: /mute [30m|2h|1d|until-tomorrow|off]", nil
			}
			upd.MutedUntil = Some(until)
		}
		_, err := s.UpdateMembershipTx(tx, actor, in.ChannelID, upd)
		return "", err
	case protocol.CommandMe:
		if args == "" {
			return "Usage: /me <text>", nil
		}
		var display, username string
		err := tx.QueryRowContext(ctx, `SELECT display_name, username FROM users WHERE id = ?`, actor.ID).Scan(&display, &username)
		if err != nil {
			return "", fmt.Errorf("load user: %w", err)
		}
		if display == "" {
			display = username
		}
		_, _, err = s.CreateMessageAsTx(tx, actor, CreateMessageParams{
			ChannelID: in.ChannelID, ThreadRootID: in.ThreadRootID, Body: "_" + display + " " + args + "_",
		})
		return "", err
	}
	return "Unknown command: /" + in.Command, nil
}

// parseMute reads a /mute argument relative to now: empty is the default
// duration; Nm, Nh and Nd are minutes, hours and days; until-tomorrow runs to
// the next local midnight.
func parseMute(arg string, now time.Time) (time.Time, bool) {
	arg = strings.ToLower(arg)
	switch {
	case arg == "":
		return now.Add(defaultMute), true
	case arg == "until-tomorrow":
		l := now.In(time.Local)
		next := time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, time.Local)
		return next, true
	}
	unit := map[byte]time.Duration{'m': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour}[arg[len(arg)-1]]
	n, err := strconv.Atoi(arg[:len(arg)-1])
	if unit == 0 || err != nil || n < 1 || time.Duration(n) > maxMute/unit {
		return time.Time{}, false
	}
	return now.Add(time.Duration(n) * unit), true
}

func helpText(catalog []protocol.Command) string {
	if len(catalog) == 0 {
		catalog = BuiltinCommands()
	}
	var b strings.Builder
	b.WriteString("Available commands:")
	for _, c := range catalog {
		b.WriteString("\n")
		u := c.Usage
		if u == "" {
			u = "/" + c.Name
		}
		b.WriteString(u)
		if c.Description != "" {
			b.WriteString(" - " + c.Description)
		}
	}
	return b.String()
}

// noticeFor turns a typed service error into text for the invoker.
func noticeFor(err error) (string, bool) {
	var ve *ValidationError
	var te *Error
	switch {
	case errors.As(err, &ve):
		return "Could not run the command: " + ve.Error(), true
	case errors.As(err, &te):
		if errors.Is(te.Kind, ErrNotFound) {
			return "Could not run the command: no such channel.", true
		}
		return "Could not run the command: " + te.Msg, true
	}
	return "", false
}

// CheckPost reports whether actor may post in channelID (and in the thread of
// threadRootID when given): nil, or the typed error a post would get. It
// writes nothing.
func (s *Service) CheckPost(ctx context.Context, actor Actor, channelID, threadRootID string) error {
	db := s.db.Read()
	_, info, err := channelInfo(ctx, db, actor, channelID)
	if err != nil {
		return err
	}
	if !visible(actor, actor.Class(), info) {
		return notFound()
	}
	if err := Can(actor, ActPost, info); err != nil {
		return err
	}
	if threadRootID != "" {
		if _, err := resolveThreadRoot(ctx, db, channelID, threadRootID); err != nil {
			return err
		}
	}
	return nil
}
