// Package execx runs a shell command with a deadline, bounded output and one
// documented exit-code convention. It is the operating-system boundary of the
// agent, so the details live here instead of in the tool layer.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// Result is what the bash tool reports back to the model.
type Result struct {
	Output    string
	ExitCode  int
	TimedOut  bool
	Truncated bool
}

// OK reports whether the command finished inside the deadline with status 0.
func (r Result) OK() bool { return r.ExitCode == 0 && !r.TimedOut }

// bounded collects stdout and stderr into one buffer, like a shell that merges
// them. Both streams are written from separate goroutines, hence the mutex.
type bounded struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *bounded) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - b.buf.Len(); len(p) > room {
		if room > 0 {
			b.buf.Write(p[:room])
		}
		b.truncated = true
	} else {
		b.buf.Write(p)
	}
	// Report the whole slice as consumed. Returning a short count would make
	// the child see a write error and could wedge it on a full pipe; the point
	// is to keep draining while discarding the excess.
	return len(p), nil
}

func (b *bounded) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *bounded) truncatedFlag() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// Run executes command with bash -c inside dir. timeout bounds this single
// call; ctx bounds the whole program, so Ctrl-C also stops a running command.
//
// Exit codes follow one convention: 0 means success, a positive value is the
// command's own status, and a negative value is the signal that killed it.
// TimedOut is reported separately because a timeout is not the command's fault.
func Run(ctx context.Context, command, dir string, timeout time.Duration, maxOutput int) (Result, error) {
	if command == "" {
		return Result{}, errors.New("expected nonempty command")
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "bash", "-c", command)
	cmd.Dir = dir
	cmd.Stdin = nil // children see EOF, never an interactive prompt
	out := &bounded{max: maxOutput}
	cmd.Stdout = out
	cmd.Stderr = out
	configure(cmd) // process group + cancellation, per operating system

	err := cmd.Run()
	res := Result{
		Output:    out.String(),
		ExitCode:  exitCode(cmd.ProcessState),
		Truncated: out.truncatedFlag(),
	}
	switch {
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
	case ctx.Err() != nil:
		return res, ctx.Err() // interrupted on purpose: stop the whole run
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil, errors.As(err, &exitErr), errors.Is(err, exec.ErrWaitDelay):
		return res, nil
	default:
		return res, fmt.Errorf("cannot run command: %w", err)
	}
}
