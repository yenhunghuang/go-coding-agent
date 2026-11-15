package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yenhunghuang/go-coding-agent/internal/config"
	"github.com/yenhunghuang/go-coding-agent/internal/model"
	"github.com/yenhunghuang/go-coding-agent/internal/tools"
)

// replModel answers every request with one line of text and records the
// messages it was sent, so a test can assert on the session the REPL built.
func replModel(t *testing.T, replies ...string) (*httptest.Server, *[][]model.Message) {
	t.Helper()
	var seen [][]model.Message
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Messages []model.Message `json:"messages"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("request body is not valid: %v", err)
		}
		seen = append(seen, request.Messages)
		index := len(seen) - 1
		if index < len(replies) {
			io.WriteString(w, string(model.ReplyJSON(
				model.Message{Role: "assistant", Content: replies[index]}, "stop",
			)))
			return
		}
		http.Error(w, `{"error":"script exhausted"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// failingModel answers the first request with a server error, then recovers.
func failingModel(t *testing.T) *httptest.Server {
	t.Helper()
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			http.Error(w, `{"error":"overloaded"}`, http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, string(model.ReplyJSON(
			model.Message{Role: "assistant", Content: "recovered"}, "stop",
		)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func replFor(t *testing.T, srv *httptest.Server) (*config.Config, *model.Client, *tools.Registry) {
	t.Helper()
	cfg := &config.Config{
		Workspace: t.TempDir(),
		BaseURL:   srv.URL + "/v1",
		Model:     "test/model",
		APIKey:    "test-key",
		Tools:     []string{"read"},
		MaxTurns:  5,
	}
	registry, err := tools.NewRegistry(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, model.New(cfg.BaseURL, cfg.APIKey), registry
}

func TestReplKeepsOneConversation(t *testing.T) {
	srv, seen := replModel(t, "first answer", "second answer")
	cfg, client, registry := replFor(t, srv)

	var stdout, stderr bytes.Buffer
	err := repl(context.Background(), cfg, client, registry,
		strings.NewReader("one\ntwo\n/quit\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("repl: %v\n%s", err, stderr.String())
	}
	if stdout.String() != "first answer\nsecond answer\n" {
		t.Errorf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "you>") {
		t.Errorf("no prompt was written: %q", stderr.String())
	}
	// The second request carries the first exchange, which is the whole point
	// of keeping the history between lines.
	second := (*seen)[1]
	if len(second) < 4 || second[2].Content != "first answer" {
		t.Errorf("second request = %+v", second)
	}
}

func TestReplClearForgetsTheConversation(t *testing.T) {
	srv, seen := replModel(t, "a1", "a2")
	cfg, client, registry := replFor(t, srv)

	var stdout, stderr bytes.Buffer
	if err := repl(context.Background(), cfg, client, registry,
		strings.NewReader("remember this\n/clear\nfresh\n/quit\n"), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 2 {
		t.Fatalf("model saw %d requests", len(*seen))
	}
	after := (*seen)[1]
	if len(after) != 2 || after[0].Role != "system" || after[1].Content != "fresh" {
		t.Errorf("after /clear the model saw %+v", after)
	}
}

func TestReplSurvivesAModelFailure(t *testing.T) {
	srv := failingModel(t)
	cfg, client, registry := replFor(t, srv)

	var stdout, stderr bytes.Buffer
	if err := repl(context.Background(), cfg, client, registry,
		strings.NewReader("ask\nask again\n/quit\n"), &stdout, &stderr); err != nil {
		t.Fatalf("a failed request should not end the session: %v", err)
	}
	if !strings.Contains(stderr.String(), "Error:") {
		t.Errorf("the failure was not reported: %q", stderr.String())
	}
	if stdout.String() != "recovered\n" {
		t.Errorf("stdout = %q", stdout.String())
	}
}

// interrupted is the whole interrupt policy, so it gets its own table: a
// cancelled turn keeps the session, a cancelled session does not.
func TestInterruptedOnlyEndsTheTurn(t *testing.T) {
	live, cancel := context.WithCancel(context.Background())
	defer cancel()
	dead, stop := context.WithCancel(context.Background())
	stop()

	cases := []struct {
		name    string
		err     error
		session context.Context
		want    bool
	}{
		{"turn cancelled, session alive", fmt.Errorf("turn 3: %w", context.Canceled), live, true},
		{"session shutting down", context.Canceled, dead, false},
		{"model protocol error", fmt.Errorf("turn 1: %w", model.ErrProtocol), live, false},
		{"turn limit", fmt.Errorf("turn limit reached (5)"), live, false},
		{"no error", nil, live, false},
	}
	for _, tc := range cases {
		if got := interrupted(tc.err, tc.session); got != tc.want {
			t.Errorf("%s: interrupted = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// No prompt argument means interactive mode, and the session must end cleanly
// on /quit rather than treating the missing prompt as an error.
func TestRunWithoutPromptOpensTheRepl(t *testing.T) {
	srv, _ := replModel(t, "hello from the repl")
	dir := t.TempDir()
	t.Setenv("OPENAI_API_KEY", "test-key")

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"-workspace", dir,
		"-base-url", srv.URL + "/v1",
		"-model", "test/model",
		"-stream=false",
	}, strings.NewReader("/quit\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "you>") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// With streaming on, the answer has already arrived as deltas on stderr, so
// the REPL must not print it a second time to stdout.
func TestReplStreamsDeltasAndDoesNotRepeatTheAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"streamed\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	cfg, client, registry := replFor(t, srv)
	cfg.Stream = true

	var stdout, stderr bytes.Buffer
	if err := repl(context.Background(), cfg, client, registry,
		strings.NewReader("say it\n/help\n/quit\n"), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want nothing: the answer already streamed", stdout.String())
	}
	if !strings.Contains(stderr.String(), "streamed") {
		t.Errorf("deltas missing: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Ctrl-C stops") {
		t.Errorf("/help did not print help: %q", stderr.String())
	}
}
