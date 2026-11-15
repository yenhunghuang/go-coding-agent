// Command agent is a small coding agent: CLI, configuration, one tool loop.
//
// stdout carries only the final answer. Progress lines and streamed deltas go
// to stderr, so a pipeline sees exactly one result.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/yenhunghuang/go-coding-agent/internal/agent"
	"github.com/yenhunghuang/go-coding-agent/internal/config"
	"github.com/yenhunghuang/go-coding-agent/internal/model"
	"github.com/yenhunghuang/go-coding-agent/internal/tools"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// run is separate from main so tests can pass arguments, capture output and
// cancel the context without touching the real process.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cfg, err := config.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		config.Describe(stdout) // --help is a normal exit, not an error
		return nil
	}
	if err != nil {
		return err
	}
	// Ctrl-C cancels ctx, which cancels the in-flight HTTP request and kills
	// the process group of any running bash command.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	registry, err := tools.NewRegistry(cfg)
	if err != nil {
		return err
	}
	client := model.New(cfg.BaseURL, cfg.APIKey)
	answer, err := agent.Run(ctx, cfg, client, registry, stderr)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, answer)
	return nil
}
