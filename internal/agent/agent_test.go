package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yenhunghuang/go-coding-agent/internal/config"
	"github.com/yenhunghuang/go-coding-agent/internal/model"
	"github.com/yenhunghuang/go-coding-agent/internal/tools"
)

// requestView is what the mock model inspects about each incoming request.
type requestView struct {
	Model    string          `json:"model"`
	Messages []model.Message `json:"messages"`
	Tools    []model.Schema  `json:"tools"`
	Stream   bool            `json:"stream"`
}

// mockModel replays a script of Chat Completions bodies and records every
// request, so a test can assert on the conversation the model really saw.
func mockModel(t *testing.T, replies ...string) (*httptest.Server, *[]requestView) {
	t.Helper()
	var seen []requestView
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var view requestView
		if err := json.Unmarshal(body, &view); err != nil {
			t.Errorf("request body is not valid: %v", err)
		}
		seen = append(seen, view)
		if index := len(seen) - 1; index < len(replies) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, replies[index])
			return
		}
		http.Error(w, `{"error":"script exhausted"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// mockStream replays a fixed event stream instead of JSON bodies.
func mockStream(t *testing.T, events ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			io.WriteString(w, "data: "+event+"\n\n")
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func call(name, args string) model.ToolCall {
	return model.ToolCall{
		ID:       "call-" + name,
		Type:     "function",
		Function: model.FunctionCall{Name: name, Arguments: args},
	}
}

func toolCallsReply(calls ...model.ToolCall) string {
	return string(model.ReplyJSON(
		model.Message{Role: "assistant", ToolCalls: calls}, "tool_calls",
	))
}

func finalReply(text string) string {
	return string(model.ReplyJSON(
		model.Message{Role: "assistant", Content: text}, "stop",
	))
}

// setup wires a real registry and client against the mock model. Nothing here
// is stubbed except the model itself: the tools, files and processes are real.
func setup(t *testing.T, srv *httptest.Server, enabled ...string) (*config.Config, *tools.Registry) {
	t.Helper()
	cfg := &config.Config{
		Prompt:    "Make and test a file",
		Workspace: t.TempDir(),
		BaseURL:   srv.URL + "/v1",
		Model:     "test/model",
		APIKey:    "test-key",
		Tools:     enabled,
		MaxTurns:  20,
	}
	if len(enabled) == 0 {
		cfg.Tools = config.KnownTools
	}
	registry, err := tools.NewRegistry(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, registry
}

func TestRunDrivesAFourToolLoop(t *testing.T) {
	srv, seen := mockModel(t,
		toolCallsReply(call("write", `{"path":"a","content":"old"}`)),
		toolCallsReply(
			call("read", `{"path":"a"}`),
			call("edit", `{"path":"a","old_text":"old","new_text":"new"}`),
		),
		toolCallsReply(call("bash", `{"command":"test \"$(cat a)\" = new && echo passed"}`)),
		finalReply("Finished"),
	)
	cfg, registry := setup(t, srv)
	var progress bytes.Buffer

	answer, err := Run(context.Background(), cfg, model.New(cfg.BaseURL, cfg.APIKey), registry, &progress)
	if err != nil {
		t.Fatal(err)
	}
	if answer != "Finished" {
		t.Errorf("answer = %q", answer)
	}
	got, err := os.ReadFile(filepath.Join(cfg.Workspace, "a"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("workspace file = %q, want %q", got, "new")
	}

	if len(*seen) != 4 {
		t.Fatalf("model requests = %d, want 4", len(*seen))
	}
	last := (*seen)[3]
	if last.Model != "test/model" || last.Stream {
		t.Errorf("request = %+v", last)
	}
	if len(last.Tools) != 4 {
		t.Errorf("schemas sent = %d, want 4", len(last.Tools))
	}
	// The history must pair every tool result with the call that asked for it.
	var ids []string
	var outputs []string
	for _, msg := range last.Messages {
		if msg.Role != "tool" {
			continue
		}
		ids = append(ids, msg.ToolCallID)
		var env struct {
			OK     bool   `json:"ok"`
			Error  string `json:"error"`
			Result struct {
				Output string `json:"output"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(msg.Content), &env); err != nil {
			t.Fatalf("tool message is not JSON: %v", err)
		}
		if !env.OK {
			t.Errorf("tool %s failed: %s", msg.ToolCallID, env.Error)
		}
		outputs = append(outputs, env.Result.Output)
	}
	// The bash result is what proves the edit really happened on disk.
	if len(outputs) > 0 && !strings.Contains(outputs[len(outputs)-1], "passed") {
		t.Errorf("last tool output = %q", outputs[len(outputs)-1])
	}
	want := []string{"call-write", "call-read", "call-edit", "call-bash"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("tool_call_ids = %v, want %v", ids, want)
	}
	for _, marker := range []string{"[tool] write", "[tool] read", "[tool] edit", "[tool] bash"} {
		if !strings.Contains(progress.String(), marker) {
			t.Errorf("progress is missing %q:\n%s", marker, progress.String())
		}
	}
}

func TestToolFailuresComeBackToTheModel(t *testing.T) {
	// The model asks for a disabled tool. That is not fatal: the run continues,
	// and the next request carries the {"ok":false,...} explanation.
	srv, seen := mockModel(t,
		toolCallsReply(call("write", `{"path":"a","content":"x"}`)),
		finalReply("Recovered"),
	)
	cfg, registry := setup(t, srv, "read")
	var progress bytes.Buffer

	answer, err := Run(context.Background(), cfg, model.New(cfg.BaseURL, cfg.APIKey), registry, &progress)
	if err != nil {
		t.Fatal(err)
	}
	if answer != "Recovered" {
		t.Errorf("answer = %q", answer)
	}
	if _, err := os.Stat(filepath.Join(cfg.Workspace, "a")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a disabled tool wrote a file: %v", err)
	}
	if len(*seen) != 2 {
		t.Fatalf("model requests = %d, want 2", len(*seen))
	}
	second := (*seen)[1]
	if len(second.Tools) != 1 || second.Tools[0].Function.Name != "read" {
		t.Errorf("schemas sent = %+v", second.Tools)
	}
	var toolMessage model.Message
	for _, msg := range second.Messages {
		if msg.Role == "tool" {
			toolMessage = msg
		}
	}
	if !strings.Contains(toolMessage.Content, "not enabled") {
		t.Errorf("tool message = %q", toolMessage.Content)
	}
}

func TestRunStopsAtTheTurnLimit(t *testing.T) {
	srv, _ := mockModel(t,
		toolCallsReply(call("read", `{`)),
		toolCallsReply(call("read", `{`)),
	)
	cfg, registry := setup(t, srv, "read")
	cfg.MaxTurns = 2
	var progress bytes.Buffer

	_, err := Run(context.Background(), cfg, model.New(cfg.BaseURL, cfg.APIKey), registry, &progress)
	if err == nil || !strings.Contains(err.Error(), "turn limit") {
		t.Fatalf("error = %v, want a turn limit error", err)
	}
}

func TestRunReportsProtocolErrorsAsFatal(t *testing.T) {
	srv, _ := mockModel(t, `{"choices":[]}`)
	cfg, registry := setup(t, srv)
	_, err := Run(context.Background(), cfg, model.New(cfg.BaseURL, cfg.APIKey), registry, io.Discard)
	if !errors.Is(err, model.ErrProtocol) {
		t.Fatalf("error = %v, want ErrProtocol", err)
	}
	if !strings.Contains(err.Error(), "turn 1") {
		t.Errorf("error should say which turn failed: %v", err)
	}
}

func TestRunStreamsDeltas(t *testing.T) {
	srv := mockStream(t,
		`{"choices":[{"delta":{"content":"all "}}]}`,
		`{"choices":[{"delta":{"content":"good"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	)
	cfg, registry := setup(t, srv, "read")
	cfg.Stream = true
	var progress bytes.Buffer

	answer, err := Run(context.Background(), cfg, model.New(cfg.BaseURL, cfg.APIKey), registry, &progress)
	if err != nil {
		t.Fatal(err)
	}
	if answer != "all good" {
		t.Errorf("answer = %q", answer)
	}
	if progress.String() != "all good" {
		t.Errorf("deltas = %q", progress.String())
	}
}

func TestRunStopsWhenCancelled(t *testing.T) {
	srv, _ := mockModel(t, finalReply("never reached"))
	cfg, registry := setup(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, cfg, model.New(cfg.BaseURL, cfg.APIKey), registry, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
