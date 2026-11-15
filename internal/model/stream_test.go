package model

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sseServer replays a fixed event stream, which is how a real gateway sends
// deltas: one JSON object per "data:" line, then a final "[DONE]".
func sseServer(t *testing.T, events ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if accept := r.Header.Get("Accept"); accept != "text/event-stream" {
			t.Errorf("Accept = %q, want text/event-stream", accept)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			io.WriteString(w, "data: "+event+"\n\n")
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStreamAssemblesContentAndToolCalls(t *testing.T) {
	srv := sseServer(t,
		`{"choices":[{"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"delta":{"content":"Hel"}}]}`,
		`{"choices":[{"delta":{"content":"lo"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"read"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","type":"function","function":{"name":"bash","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":9}}`,
	)

	var deltas []string
	msg, usage, err := New(srv.URL, "").Stream(
		context.Background(), "m", nil, nil,
		func(delta string) error {
			deltas = append(deltas, delta)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "") != "Hello" {
		t.Errorf("deltas = %v", deltas)
	}
	if usage.PromptTokens != 7 || usage.CompletionTokens != 9 {
		t.Errorf("usage = %+v", usage)
	}
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", msg.ToolCalls)
	}
	first := msg.ToolCalls[0]
	if first.ID != "c1" || first.Function.Name != "read" ||
		first.Function.Arguments != `{"path":"a"}` {
		t.Errorf("first call = %+v", first)
	}
	if msg.ToolCalls[1].ID != "c2" || msg.ToolCalls[1].Function.Name != "bash" {
		t.Errorf("second call = %+v", msg.ToolCalls[1])
	}
}

func TestStreamFinalAnswer(t *testing.T) {
	srv := sseServer(t,
		`{"choices":[{"delta":{"content":"all good"},"finish_reason":"stop"}]}`,
	)
	msg, _, err := New(srv.URL, "").Stream(context.Background(), "m", nil, nil, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "all good" || len(msg.ToolCalls) != 0 {
		t.Errorf("message = %+v", msg)
	}
}

func TestStreamStopsWhenTheCallbackFails(t *testing.T) {
	srv := sseServer(t,
		`{"choices":[{"delta":{"content":"one"}}]}`,
		`{"choices":[{"delta":{"content":"two"}}]}`,
	)
	want := errors.New("writer stopped")
	_, _, err := New(srv.URL, "").Stream(context.Background(), "m", nil, nil, func(string) error {
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want the callback's error", err)
	}
}

func TestStreamAppliesTheSameRules(t *testing.T) {
	// A stream that ends mid-tool-call is rejected exactly like a broken JSON
	// response: one set of completeness rules, two transports.
	srv := sseServer(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"read"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"length"}]}`,
	)
	_, _, err := New(srv.URL, "").Stream(context.Background(), "m", nil, nil, func(string) error { return nil })
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("error = %v, want ErrProtocol", err)
	}
}

func TestStreamBoundsResponseSize(t *testing.T) {
	srv := sseServer(t,
		`{"choices":[{"delta":{"content":"`+strings.Repeat("x", 4096)+`"}}]}`,
	)
	c := New(srv.URL, "")
	c.MaxResponse = 512
	_, _, err := c.Stream(context.Background(), "m", nil, nil, func(string) error { return nil })
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}
}
