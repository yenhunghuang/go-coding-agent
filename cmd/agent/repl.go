package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/yenhunghuang/go-coding-agent/internal/agent"
	"github.com/yenhunghuang/go-coding-agent/internal/config"
	"github.com/yenhunghuang/go-coding-agent/internal/model"
	"github.com/yenhunghuang/go-coding-agent/internal/tools"
)

const replHelp = `  <text>      ask for something; the conversation keeps its history
  /tools      show which tools are enabled
  /clear      forget everything except the system prompt
  /help       this text
  /quit       leave (Ctrl-D works too)

Ctrl-C stops the current request; the prompt comes back.`

// repl keeps one conversation in memory: every line is a new user turn, and
// the history returned by agent.RunTurn is fed back in, so the model can refer
// to what it did earlier.
//
// Two contexts, on purpose:
//   - ctx is the session. Cancelling it ends the program.
//   - turnCtx is derived per request and is what Ctrl-C cancels, so a slow
//     model call or a stuck bash command stops without closing the session.
//
// stdin, stdout and stderr are arguments so tests can script a session.
func repl(
	ctx context.Context,
	cfg *config.Config,
	client *model.Client,
	registry *tools.Registry,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	history := []model.Message{{Role: "system", Content: agent.SystemPrompt}}
	scanner := bufio.NewScanner(stdin)
	// A pasted stack trace is longer than the 64 KiB default.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	fmt.Fprint(stderr, "agent in "+cfg.Workspace+
		" ("+strings.Join(cfg.Tools, ", ")+")\n"+replHelp+"\n")

	for {
		fmt.Fprint(stderr, "\nyou> ")
		if !scanner.Scan() {
			return nil // Ctrl-D, or stdin closed: the session is over
		}
		switch line := strings.TrimSpace(scanner.Text()); line {
		case "":
			continue
		case "/quit", "/exit":
			return nil
		case "/help":
			fmt.Fprint(stderr, replHelp+"\n")
			continue
		case "/clear":
			history = history[:1] // keep the system prompt
			fmt.Fprintln(stderr, "(history cleared)")
			continue
		case "/tools":
			fmt.Fprintln(stderr, "enabled:", strings.Join(cfg.Tools, ", "))
			continue
		default:
			history = append(history, model.Message{Role: "user", Content: line})
		}

		before := history
		turnCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		answer, next, err := agent.RunTurn(turnCtx, cfg, client, registry, history, stderr)
		stop()

		if interrupted(err, ctx) {
			// An interrupted turn leaves tool calls without results, which
			// the API would reject, so the session rolls back to the last
			// complete exchange. Files the tools already wrote stay on disk.
			history = append(before, model.Message{
				Role:    "user",
				Content: "(the previous request was interrupted)",
			})
			fmt.Fprintln(stderr, "(interrupted)")
			continue
		}
		if err != nil {
			// A protocol or HTTP error ends this request, not the session:
			// the model can simply be asked again.
			history = before
			fmt.Fprintln(stderr, "Error:", err)
			continue
		}
		history = next
		if cfg.Stream {
			fmt.Fprintln(stderr) // the answer already arrived as deltas
		} else {
			fmt.Fprintln(stdout, answer)
		}
	}
}

// interrupted reports whether a turn was cancelled while the session lives on.
// If ctx itself is done, the whole program is shutting down and the caller
// should not keep asking the model.
func interrupted(err error, session context.Context) bool {
	return errors.Is(err, context.Canceled) && session.Err() == nil
}
