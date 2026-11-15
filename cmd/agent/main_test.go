package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yenhunghuang/go-coding-agent/internal/model"
)

// mockModel answers with one tool call and then a final answer, so the test
// covers the whole path: flags, registry, HTTP, tools, output streams.
func mockModel(t *testing.T) *httptest.Server {
	t.Helper()
	replies := []string{
		string(model.ReplyJSON(model.Message{
			Role: "assistant",
			ToolCalls: []model.ToolCall{{
				ID:       "c1",
				Type:     "function",
				Function: model.FunctionCall{Name: "write", Arguments: `{"path":"hello.txt","content":"hi\n"}`},
			}},
		}, "tool_calls")),
		string(model.ReplyJSON(model.Message{Role: "assistant", Content: "CLI works"}, "stop")),
	}
	index := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Accept") == "text/event-stream" {
			t.Error("the mock only speaks JSON; pass -stream=false")
		}
		w.Header().Set("Content-Type", "application/json")
		if index < len(replies) {
			io.WriteString(w, replies[index])
			index++
			return
		}
		http.Error(w, `{"error":"script exhausted"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunWritesFilesAndPrintsTheAnswer(t *testing.T) {
	workspace := t.TempDir()
	srv := mockModel(t)
	t.Setenv("OPENAI_API_KEY", "test-key")

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"-workspace", workspace,
		"-base-url", srv.URL + "/v1",
		"-model", "test/model",
		"-tools", "read,write",
		"-stream=false",
		"create hello.txt",
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}
	if stdout.String() != "CLI works\n" {
		t.Errorf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "[tool] write") {
		t.Errorf("stderr = %q", stderr.String())
	}
	got, err := os.ReadFile(filepath.Join(workspace, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi\n" {
		t.Errorf("hello.txt = %q", got)
	}
}

func TestRunStreamsByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"all \"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"good\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"-workspace", t.TempDir(),
		"-base-url", srv.URL,
		"-model", "test/model",
		"-tools", "read",
		"say all good",
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}
	if stdout.String() != "all good\n" {
		t.Errorf("stdout = %q", stdout.String())
	}
	if stderr.String() != "all good" {
		t.Errorf("stderr deltas = %q", stderr.String())
	}
}

func TestRunHelpExitsCleanly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"--help"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run --help: %v", err)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestRunReportsBadFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"-max-turns", "0", "hello"}, strings.NewReader(""), &stdout, &stderr); err == nil {
		t.Fatal("run accepted -max-turns 0")
	}
}
