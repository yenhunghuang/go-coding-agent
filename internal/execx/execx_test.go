package execx

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunReportsOutputAndExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash is not available on Windows")
	}
	dir := t.TempDir()

	res, err := Run(context.Background(), "printf hello; printf oops >&2; exit 7", dir, 10*time.Second, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", res.ExitCode)
	}
	if !strings.Contains(res.Output, "hello") || !strings.Contains(res.Output, "oops") {
		t.Errorf("Output = %q, want both stdout and stderr", res.Output)
	}
	if res.TimedOut || res.Truncated {
		t.Errorf("flags = timed_out:%v truncated:%v", res.TimedOut, res.Truncated)
	}
	if res.OK() {
		t.Error("OK() is true for a failing command")
	}

	// pwd -P is the physical path, which is what cmd.Dir actually chdir'd to.
	res, err = Run(context.Background(), "pwd -P", dir, 10*time.Second, 1000)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(res.Output); got != want {
		t.Errorf("pwd = %q, want %q", got, want)
	}
	if !res.OK() {
		t.Error("OK() is false for a successful command")
	}
}

func TestRunStdinIsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash is not available on Windows")
	}
	res, err := Run(context.Background(), "read x; echo eof", t.TempDir(), 10*time.Second, 100)
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "eof\n" {
		t.Errorf("Output = %q, want %q", res.Output, "eof\n")
	}
}

func TestRunBoundsOutputButKeepsDraining(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash is not available on Windows")
	}
	// A child far larger than the cap must still finish: discarding is not the
	// same as stopping, and a full pipe would wedge the command.
	res, err := Run(context.Background(), "yes x | head -c 200000", t.TempDir(), 15*time.Second, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Output) != 50 {
		t.Errorf("len(Output) = %d, want 50", len(res.Output))
	}
	if !res.Truncated {
		t.Error("Truncated is false")
	}
}

func TestRunTimeoutKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are Unix-specific here")
	}
	dir := t.TempDir()
	// The background grandchild outlives the deadline unless the whole process
	// group is killed.
	res, err := Run(context.Background(), "(sleep 2; touch escaped) & wait", dir, 1*time.Second, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("TimedOut is false; result %+v", res)
	}
	if res.ExitCode != -9 {
		t.Errorf("ExitCode = %d, want -9 (SIGKILL)", res.ExitCode)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := filepath.EvalSymlinks(filepath.Join(dir, "escaped")); err == nil {
		t.Error("grandchild survived the timeout: the process group was not killed")
	}
}

func TestRunHonoursCallerCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash is not available on Windows")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	_, err := Run(ctx, "sleep 30", t.TempDir(), 30*time.Second, 1000)
	if err != context.Canceled {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestRunRejectsEmptyCommand(t *testing.T) {
	if _, err := Run(context.Background(), "", t.TempDir(), time.Second, 100); err == nil {
		t.Fatal("empty command accepted")
	}
}
