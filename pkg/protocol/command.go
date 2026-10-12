package protocol

// Slash commands. A command is either built in (handled by the server) or
// registered by an app; a person invokes one with POST /commands/invoke.

// Limits of the command registry and of an invocation.
const (
	MaxCommandsPerApp      = 50
	MaxCommandNameChars    = 32
	MaxCommandTextChars    = 200  // description and usage
	MaxCommandArgsChars    = 4000 // args of an invocation
	MaxCommandRespondChars = 4000 // text of a response
)

// Built-in command names. An app cannot register one of these.
const (
	CommandTopic  = "topic"
	CommandInvite = "invite"
	CommandLeave  = "leave"
	CommandMute   = "mute"
	CommandMe     = "me"
	CommandHelp   = "help"
)

// Command is one entry of GET /commands. AppName is empty for a built-in.
type Command struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	AppName     string `json:"app_name"`
	Builtin     bool   `json:"builtin"`
}

// CommandList is the body of GET /commands and of the response to
// PUT /apps/self/commands.
type CommandList struct {
	Items []Command `json:"items"`
}

// CommandInput is one command of the body of PUT /apps/self/commands.
type CommandInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
}

// InvokeCommandRequest is the body of POST /commands/invoke.
type InvokeCommandRequest struct {
	ChannelID    string  `json:"channel_id"`
	Command      string  `json:"command"`
	Args         string  `json:"args"`
	ThreadRootID *string `json:"thread_root_id,omitempty"`
}

// InvokedCommand is the response to POST /commands/invoke. InvocationID is
// set for an app command (202: the app was told); it is empty for a built-in,
// which the server has already run (200).
type InvokedCommand struct {
	InvocationID string `json:"invocation_id,omitempty"`
	Builtin      bool   `json:"builtin"`
}

// RespondCommandRequest is the body of
// POST /commands/invocations/{id}/respond.
type RespondCommandRequest struct {
	Text      string `json:"text"`
	Ephemeral bool   `json:"ephemeral"`
}

// RespondedCommand is the response to a command response. Message is the
// posted message, null for an ephemeral response (which is never stored).
type RespondedCommand struct {
	Ephemeral bool     `json:"ephemeral"`
	Message   *Message `json:"message"`
}

// CommandInvoker names the person who ran a command.
type CommandInvoker struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

// CommandInvoked is the data of a command.invoked event. It is sent only to
// the connections of the app that owns the command, with seq 0, and is not
// stored.
type CommandInvoked struct {
	InvocationID string         `json:"invocation_id"`
	Command      string         `json:"command"`
	Args         string         `json:"args"`
	ChannelID    string         `json:"channel_id"`
	ThreadRootID *string        `json:"thread_root_id"`
	User         CommandInvoker `json:"user"`
}

// EphemeralMessage is the data of an ephemeral.message event: a notice or an
// app's private answer shown only to the person it is for, in the channel
// (and thread) the command ran in. It has seq 0 and is not stored. Author is
// the app's bot for an app's answer and null for a notice from the server
// itself. InvocationID is empty when no app invocation is behind the notice.
type EphemeralMessage struct {
	InvocationID string  `json:"invocation_id"`
	ChannelID    string  `json:"channel_id"`
	ThreadRootID *string `json:"thread_root_id"`
	Author       *Author `json:"author"`
	Body         string  `json:"body"`
}
