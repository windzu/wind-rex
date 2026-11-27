package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// internalCmd runs the commands the files `gorex skill setup` installs.
// They are not part of the documented command line.
func internalCmd(args []string, out, errOut io.Writer) (int, bool) {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "gorex: unknown internal command")
		return ExitUsage, true
	}
	switch args[0] {
	case "skill":
		return internalSkillCmd(args[1:], out)
	case "session-start":
		return sessionStartCmd(args[1:], out)
	case "title":
		return internalTitleCmd(args[1:], os.Stdin, out)
	default:
		fmt.Fprintf(errOut, "gorex: unknown internal command %q\n", args[0])
		return ExitUsage, true
	}
}

// internalTitleCmd prints the terminal window title for an agent CLI from
// the state JSON piped on stdin. It exits 0 whatever happens so an agent
// never disables its title hook on transient or empty input.
func internalTitleCmd(args []string, in io.Reader, out io.Writer) (int, bool) {
	fs := flagSet("__internal title", io.Discard)
	agent := fs.String("agent", "", "")
	if fs.Parse(args) != nil {
		return ExitOK, true
	}
	if *agent != "agy" {
		return ExitOK, true
	}
	if title := agyTitleFromJSON(in); title != "" {
		fmt.Fprintln(out, title)
	}
	return ExitOK, true
}

// agyTitleFromJSON extracts the terminal title from an Antigravity CLI
// state JSON payload (https://antigravity.google/docs/cli/title). When
// conversation_title is absent or empty on a valid payload, it returns
// "Antigravity" so switching to a new untitled conversation actively
// resets the OSC 2 title instead of keeping the previous conversation's
// title. Invalid or empty input returns "".
func agyTitleFromJSON(r io.Reader) string {
	if r == nil {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(r, 256<<10))
	if err != nil {
		return ""
	}
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return ""
	}
	var payload struct {
		ConversationTitle string `json:"conversation_title"`
	}
	if json.Unmarshal(b, &payload) != nil {
		return ""
	}
	if t := sanitizeTitleLine(payload.ConversationTitle); t != "" {
		return t
	}
	return "Antigravity"
}

// sanitizeTitleLine turns a conversation title into a single clean line
// safe for an OSC 2 terminal title.
func sanitizeTitleLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// internalSkillCmd prints the guide the stub skill tells an agent to read.
// The pane-naming flag is accepted for the stub's sake and changes
// nothing here.
func internalSkillCmd(args []string, out io.Writer) (int, bool) {
	fs := flagSet("__internal skill", io.Discard)
	_ = fs.Bool("feature-pane-naming", true, "")
	if fs.Parse(args) != nil {
		return ExitOK, true
	}
	fmt.Fprint(out, gorexGuide)
	return ExitOK, true
}

// sessionStartCmd prints what an agent should know at the start of a
// session in a GoRex terminal. Outside GoRex it prints nothing. It exits 0
// whatever happens, so an agent's start never fails for it.
func sessionStartCmd(args []string, out io.Writer) (int, bool) {
	fs := flagSet("__internal session-start", io.Discard)
	agent := fs.String("agent", "", "")
	naming := fs.Bool("feature-pane-naming", true, "")
	if fs.Parse(args) != nil {
		return ExitOK, true
	}
	sid := contextValue(os.Getenv("GOREX_SESSION"))
	if sid == "" {
		return ExitOK, true
	}
	pane := contextValue(os.Getenv("GOREX_PANE"))
	server := printable(os.Getenv("GOREX_SERVER"))
	host := contextValue(os.Getenv("GOREX_HOST"))
	client := contextValue(os.Getenv("GOREX_CLIENT"))

	// Devin runs commands through a POSIX shell on Windows too, where the
	// shim resolves as gorex.cmd, not gorex.
	bin := "gorex"
	if *agent == "devin" && runtime.GOOS == "windows" {
		bin = "gorex.cmd"
	}

	var msg strings.Builder
	fmt.Fprintln(&msg, "You're running in a GoRex terminal.")
	fmt.Fprintln(&msg, "```text")
	if server != "" {
		fmt.Fprintf(&msg, "GOREX_SERVER=%s\n", server)
	}
	fmt.Fprintf(&msg, "GOREX_SESSION=%s\n", sid)
	if pane != "" {
		fmt.Fprintf(&msg, "GOREX_PANE=%s\n", pane)
	}
	if host != "" {
		fmt.Fprintf(&msg, "GOREX_HOST=%s\n", host)
	} else if client != "" {
		fmt.Fprintf(&msg, "GOREX_CLIENT=%s\n", client)
	}
	fmt.Fprintln(&msg, "```")
	fmt.Fprintln(&msg)
	fmt.Fprintln(&msg, "`GOREX_SESSION` identifies your session, and `GOREX_PANE` numbers")
	fmt.Fprintln(&msg, "your terminal pane; both are set in your shell. Commands select the")
	fmt.Fprintln(&msg, "pane with `-s \"$GOREX_SESSION\"`. The user may be viewing a different")
	fmt.Fprintln(&msg, "session or pane.")
	fmt.Fprintln(&msg)
	fmt.Fprintln(&msg, "These inherited IDs haven't been checked against the server. If a")
	fmt.Fprintf(&msg, "target can't be found, run `%s ls --json` to see the sessions on\n", bin)
	fmt.Fprintln(&msg, "the connected server.")
	fmt.Fprintln(&msg)
	fmt.Fprintf(&msg, "Use the `%s` command to create sessions, split panes, run commands,\n", bin)
	fmt.Fprintln(&msg, "and read terminal output. The gorex skill provides instructions and")
	fmt.Fprintf(&msg, "examples. If the skill isn't installed, run `%s skill show` to read\n", bin)
	fmt.Fprintln(&msg, "the same guide.")
	if *naming {
		fmt.Fprintln(&msg)
		fmt.Fprintln(&msg, "Label the pane as part of handling the first request that identifies")
		fmt.Fprintf(&msg, "the work. Run `%s rename -s \"$GOREX_SESSION\" \"<label>\"`\n", bin)
		fmt.Fprintln(&msg, "without waiting for the user to ask, before loading the skill. Use a short")
		fmt.Fprintln(&msg, "natural-language phrase in the user's language (for example, `Fix login")
		fmt.Fprintln(&msg, "redirect` or `修复登录重定向`), not a hyphenated slug. Name the task or")
		fmt.Fprintln(&msg, "project. Keep the existing label if it already describes the same work.")
	}
	if *agent == "agy" {
		WriteJSON(out, map[string]any{
			"injectSteps": []any{
				map[string]any{
					"systemMessage": map[string]any{
						"systemMessage": strings.TrimRight(msg.String(), "\n"),
					},
				},
			},
		})
		return ExitOK, true
	}
	if *agent == "devin" {
		WriteJSON(out, map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":     "SessionStart",
				"additionalContext": strings.TrimRight(msg.String(), "\n"),
			},
		})
		return ExitOK, true
	}
	fmt.Fprint(out, msg.String())
	return ExitOK, true
}
