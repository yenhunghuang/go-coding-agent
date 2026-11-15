package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/yenhunghuang/go-coding-agent/internal/config"
	"github.com/yenhunghuang/go-coding-agent/internal/execx"
)

// bashTool runs a shell command in the workspace. It is the only tool with real
// power, and it is not a sandbox: it inherits the user's environment.
type bashTool struct{ cfg *config.Config }

func (t bashTool) Name() string { return "bash" }

// BashResult tells the model what happened. exit_code follows one convention:
// 0 is success, positive is the command's own status, negative is the signal
// that killed it, and timed_out says the deadline ended the run.
type BashResult struct {
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
}

func (t bashTool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Command   string `json:"command"`
		Timeout   int    `json:"timeout"`
		MaxOutput int    `json:"max_output"`
	}
	args.Timeout, args.MaxOutput = 30, 16000
	if err := decode(raw, &args); err != nil {
		return nil, err
	}
	if err := requireText("command", args.Command); err != nil {
		return nil, err
	}
	if err := checkRange("timeout", args.Timeout, 1, 300); err != nil {
		return nil, err
	}
	if err := checkRange("max_output", args.MaxOutput, 1, 100_000); err != nil {
		return nil, err
	}
	result, err := execx.Run(
		ctx,
		args.Command,
		t.cfg.Workspace,
		time.Duration(args.Timeout)*time.Second,
		args.MaxOutput,
	)
	if err != nil {
		return nil, err
	}
	return BashResult{
		Output:    result.Output,
		ExitCode:  result.ExitCode,
		TimedOut:  result.TimedOut,
		Truncated: result.Truncated,
	}, nil
}
