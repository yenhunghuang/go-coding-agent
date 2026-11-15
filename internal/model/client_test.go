package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type captured struct {
	path string
	auth string
	body request
}

// mockModel is the smallest useful stand-in for a gateway: it replays a script
// and records what the client actually put on the wire.
func mockModel(t *testing.T, status int, replies ...string) (*httptest.Server, *[]captured) {
	t.Helper()
	var got []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded request
		_ = json.Unmarshal(body, &decoded)
		got = append(got, captured{
			path: r.URL.Path,
			auth: r.Header.Get("Authorization"),
			body: decoded,
		})
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"message":"private gateway detail"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if index := len(got) - 1; index < len(replies) {
			io.WriteString(w, replies[index])
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestCompleteSendsAnOpenAIRequest(t *testing.T) {
	srv, got := mockModel(t, 0, string(ReplyJSON(
		Message{Role: "assistant", Content: "done"}, "stop",
	)))
	client := New(srv.URL+"/v1", "test-key")

	msg, usage, err := client.Complete(
		context.Background(),
		"test/model",
		[]Message{{Role: "user", Content: "hi"}},
		[]Schema{{Type: "function", Function: FunctionSpec{Name: "read"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "done" {
		t.Errorf("Content = %q", msg.Content)
	}
	if usage.PromptTokens != 0 {
		t.Errorf("usage = %+v", usage)
	}
	if len(*got) != 1 {
		t.Fatalf("requests = %d, want 1", len(*got))
	}
	sent := (*got)[0]
	if sent.path != "/v1/chat/completions" {
		t.Errorf("path = %q", sent.path)
	}
	if sent.auth != "Bearer test-key" {
		t.Errorf("authorization = %q", sent.auth)
	}
	if sent.body.Model != "test/model" || sent.body.Stream {
		t.Errorf("body = %+v", sent.body)
	}
	if len(sent.body.Messages) != 1 || len(sent.body.Tools) != 1 {
		t.Errorf("body = %+v", sent.body)
	}
}

func TestCompleteRejectsBrokenResponses(t *testing.T) {
	cases := map[string]string{
		"no choices":           `{"choices":[]}`,
		"wrong role":           string(ReplyJSON(Message{Role: "user", Content: "x"}, "stop")),
		"no final text":        string(ReplyJSON(Message{Role: "assistant"}, "stop")),
		"stopped early":        string(ReplyJSON(Message{Role: "assistant", Content: "partial"}, "length")),
		"tool call without id": string(ReplyJSON(Message{Role: "assistant", ToolCalls: []ToolCall{{Type: "function", Function: FunctionCall{Name: "read"}}}}, "tool_calls")),
		"duplicate tool id": string(ReplyJSON(Message{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "a", Type: "function", Function: FunctionCall{Name: "read"}},
			{ID: "a", Type: "function", Function: FunctionCall{Name: "read"}},
		}}, "tool_calls")),
		"truncated tool call": string(ReplyJSON(Message{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "a", Type: "function", Function: FunctionCall{Name: "read"}},
		}}, "length")),
		"invalid json": `{"choices":`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _ := mockModel(t, 0, body)
			client := New(srv.URL, "")
			if _, _, err := client.Complete(context.Background(), "m", nil, nil); !errors.Is(err, ErrProtocol) {
				t.Fatalf("error = %v, want ErrProtocol", err)
			}
		})
	}
}

func TestHTTPErrorDoesNotLeakTheResponseBody(t *testing.T) {
	srv, _ := mockModel(t, http.StatusUnauthorized)
	client := New(srv.URL, "")
	_, _, err := client.Complete(context.Background(), "m", nil, nil)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v, want *HTTPError", err)
	}
	if httpErr.Status != http.StatusUnauthorized {
		t.Errorf("status = %d", httpErr.Status)
	}
	if strings.Contains(err.Error(), "private gateway detail") {
		t.Errorf("error leaked the body: %v", err)
	}
}

func TestCompleteBoundsResponseSize(t *testing.T) {
	srv, _ := mockModel(t, 0, string(ReplyJSON(
		Message{Role: "assistant", Content: strings.Repeat("x", 4096)}, "stop",
	)))
	client := New(srv.URL, "")
	client.MaxResponse = 512
	if _, _, err := client.Complete(context.Background(), "m", nil, nil); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}
}

func TestCompleteHonoursCancellation(t *testing.T) {
	srv, _ := mockModel(t, 0, string(ReplyJSON(Message{Role: "assistant", Content: "x"}, "stop")))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := client(srv).Complete(ctx, "m", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func client(srv *httptest.Server) *Client { return New(srv.URL, "") }
