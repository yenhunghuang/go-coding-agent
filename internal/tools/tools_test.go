package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yenhunghuang/go-coding-agent/internal/config"
	"github.com/yenhunghuang/go-coding-agent/internal/fsx"
)

// setup builds a registry over a fresh workspace. Every test gets its own
// directory from t.TempDir, which is removed when the test finishes.
func setup(t *testing.T, enabled ...string) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{Workspace: dir, Tools: enabled, MaxTurns: 20}
	registry, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return registry, dir
}

func call(t *testing.T, registry *Registry, name, args string) envelope {
	t.Helper()
	var env envelope
	out := registry.Execute(context.Background(), name, args)
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("tool output is not JSON: %v (%s)", err, out)
	}
	return env
}

// succeed returns the result of a successful call as a generic map, so the
// assertions stay short without a mirror struct per tool.
func succeed(t *testing.T, registry *Registry, name, args string) map[string]any {
	t.Helper()
	env := call(t, registry, name, args)
	if !env.OK {
		t.Fatalf("%s(%s) failed: %s", name, args, env.Error)
	}
	payload, err := json.Marshal(env.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSchemasMatchEnabledTools(t *testing.T) {
	registry, _ := setup(t, "read", "bash")
	schemas := registry.Schemas()
	if len(schemas) != 2 {
		t.Fatalf("len(schemas) = %d, want 2", len(schemas))
	}
	if schemas[0].Function.Name != "read" || schemas[1].Function.Name != "bash" {
		t.Errorf("schemas = %v", schemas)
	}
	if len(schemas[0].Function.Parameters) == 0 {
		t.Error("read schema has no parameters")
	}
	if _, err := NewRegistry(&config.Config{Tools: []string{"nope"}}); err == nil {
		t.Error("an unknown tool name was accepted")
	}
}

func TestReadLines(t *testing.T) {
	registry, dir := setup(t, "read", "write")
	path := filepath.Join(dir, "a")
	if err := os.WriteFile(path, []byte("一二\nthree\nfour\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := succeed(t, registry, "read", `{"path":"a","start_line":2,"limit":1}`)
	if got["output"] != "2: three\n" {
		t.Errorf("output = %q", got["output"])
	}
	if got["total_lines"] != float64(3) {
		t.Errorf("total_lines = %v", got["total_lines"])
	}
	if got["truncated"] != true {
		t.Errorf("truncated = %v", got["truncated"])
	}

	// The cap counts code points, so a Chinese character is never cut in half.
	got = succeed(t, registry, "read", `{"path":"a","max_output":4}`)
	if got["output"] != "1: 一" || got["truncated"] != true {
		t.Errorf("max_output run = %v", got)
	}

	// Past the end of the file: empty output, and nothing was omitted.
	got = succeed(t, registry, "read", `{"path":"a","start_line":100}`)
	if got["output"] != "" || got["truncated"] != false {
		t.Errorf("past EOF = %v", got)
	}

	// Line counting is pinned for the edge cases that models hit often.
	for content, want := range map[string]float64{
		"":    0,
		"x":   1,
		"x\n": 1,
		"\n":  1,
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := succeed(t, registry, "read", `{"path":"a"}`)["total_lines"]; got != want {
			t.Errorf("total_lines for %q = %v, want %v", content, got, want)
		}
	}
}

func TestEditValidatesBeforeWriting(t *testing.T) {
	registry, dir := setup(t, "read", "write", "edit")
	path := filepath.Join(dir, "a")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	content := func() string {
		t.Helper()
		got, err := fsx.ReadText(path, fsx.MaxTextBytes)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	write("one\ntwo\n")
	succeed(t, registry, "edit", `{"path":"a","old_text":"two","new_text":"2"}`)
	if content() != "one\n2\n" {
		t.Errorf("after edit: %q", content())
	}

	// Deleting text is an empty replacement.
	write("one\ntwo\n")
	succeed(t, registry, "edit", `{"path":"a","old_text":"two\n","new_text":""}`)
	if content() != "one\n" {
		t.Errorf("after delete: %q", content())
	}

	// Unicode and CRLF survive: only the matched bytes change.
	write("你好\r\n世界\r\n")
	succeed(t, registry, "edit", `{"path":"a","old_text":"世界","new_text":"你好"}`)
	if content() != "你好\r\n你好\r\n" {
		t.Errorf("CRLF edit: %q", content())
	}

	// Ambiguity, including overlapping matches, must leave the file alone.
	for _, args := range []string{
		`{"path":"a","old_text":"aa","new_text":"x"}`, // "aaa" hides two matches
		`{"path":"a","old_text":"z","new_text":"x"}`,  // no match at all
		`{"path":"a","old_text":"","new_text":"x"}`,   // empty old_text
	} {
		write("aaa")
		if env := call(t, registry, "edit", args); env.OK {
			t.Errorf("%s unexpectedly succeeded", args)
		}
		if content() != "aaa" {
			t.Errorf("%s left the file as %q", args, content())
		}
	}
}

func TestRejectedCalls(t *testing.T) {
	registry, dir := setup(t, "read", "write", "edit", "bash")
	path := filepath.Join(dir, "a")
	if err := os.WriteFile(path, []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big"),
		[]byte(strings.Repeat("x", fsx.MaxTextBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct{ name, args, why string }{
		{"read", `{"path":"a","limit":true}`, "wrong type"},
		{"read", `{"path":"a","start_line":0}`, "below range"},
		{"read", `{"path":"a","max_output":100001}`, "above range"},
		{"read", `{"path":"a","extra":1}`, "unknown key"},
		{"read", `{"path":"missing"}`, "no such file"},
		{"read", `{"path":"."}`, "a directory"},
		{"read", `{"path":"big"}`, "over the size limit"},
		{"read", `[]`, "not an object"},
		{"read", `{`, "truncated JSON"},
		{"read", ``, "no arguments at all"},
		{"write", `{"path":"../escape","content":"x"}`, "escapes the workspace"},
		{"write", `{"path":"no/parent","content":"x"}`, "missing parent"},
		{"write", `{"path":"a","content":5}`, "content is not a string"},
		{"write", `{"path":"a","content":"a\u0000b"}`, "NUL in content"},
		{"write", fmt.Sprintf(`{"path":"a","content":%q}`,
			strings.Repeat("x", fsx.MaxTextBytes+1)), "content too large"},
		{"bash", `{"command":"true","timeout":0}`, "timeout below range"},
		{"bash", `{"command":""}`, "empty command"},
		{"nope", `{}`, "no such tool"},
	}
	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			if err := os.WriteFile(path, []byte("aaa"), 0o644); err != nil {
				t.Fatal(err)
			}
			if env := call(t, registry, tc.name, tc.args); env.OK {
				t.Fatalf("%s(%s) unexpectedly succeeded: %v", tc.name, tc.args, env.Result)
			}
			if got, _ := fsx.ReadText(path, fsx.MaxTextBytes); got != "aaa" {
				t.Fatalf("%s modified the file to %q", tc.why, got)
			}
		})
	}
}

func TestDisabledToolIsRejected(t *testing.T) {
	registry, _ := setup(t, "read")
	env := call(t, registry, "write", `{"path":"a","content":"x"}`)
	if env.OK {
		t.Fatal("write ran although it is disabled")
	}
	if !strings.Contains(env.Error, "not enabled") {
		t.Errorf("error = %q", env.Error)
	}
}

func TestBashReportsExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash is not available on Windows")
	}
	registry, _ := setup(t, "bash")
	got := succeed(t, registry, "bash", `{"command":"echo hi; exit 3"}`)
	if got["output"] != "hi\n" || got["exit_code"] != float64(3) {
		t.Errorf("result = %v", got)
	}
	if got["timed_out"] != false || got["truncated"] != false {
		t.Errorf("flags = %v", got)
	}
}
