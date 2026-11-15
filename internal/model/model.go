// Package model talks to an OpenAI-compatible Chat Completions endpoint.
// One request shape, one set of completeness rules, two transports: a single
// JSON response or a stream of deltas.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
)

// MaxToolCalls bounds how many tools one turn may request.
const MaxToolCalls = 16

// ErrProtocol marks a response that does not make sense: no choices, a missing
// tool call ID, a truncated answer. These are fatal for the run because the
// agent cannot recover from a model that breaks the contract. Tool failures,
// by contrast, are reported to the model so it can try again.
var ErrProtocol = errors.New("model protocol error")

// ErrTooLarge means the response crossed the client's size limit.
var ErrTooLarge = errors.New("model response exceeds size limit")

// Message is one entry in the conversation. Content is always sent as a string
// so tool messages and assistant messages stay uniform.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is one function the model asked us to run.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall carries the tool name and its JSON-encoded arguments. The
// arguments stay a string here; the tool layer decodes them.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Schema is what the model sees of a tool. The parameters stay raw JSON so
// tools.json can be edited without touching Go types.
type Schema struct {
	Type     string       `json:"type"`
	Function FunctionSpec `json:"function"`
}

// FunctionSpec is the function half of a Schema.
type FunctionSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Usage is the token accounting the gateway reports for one turn.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type request struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Schema  `json:"tools"`
	Stream   bool      `json:"stream"`
}

type response struct {
	Choices []choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type choice struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// HTTPError reports a non-2xx status without echoing the response body, which
// may contain gateway internals or credentials.
type HTTPError struct {
	Status int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf(
		"model request failed: HTTP %d; check endpoint, model and credentials",
		e.Status,
	)
}

// validate applies the completeness rules shared by both transports.
func validate(resp *response) (*Message, Usage, error) {
	if len(resp.Choices) == 0 {
		return nil, Usage{}, fmt.Errorf("%w: model returned no choices", ErrProtocol)
	}
	choice := resp.Choices[0]
	msg := choice.Message
	if msg.Role != "assistant" {
		return nil, Usage{}, fmt.Errorf("%w: expected assistant message", ErrProtocol)
	}
	if len(msg.ToolCalls) > 0 {
		if choice.FinishReason != "tool_calls" {
			return nil, Usage{}, fmt.Errorf(
				"%w: incomplete tool response; no tools executed", ErrProtocol,
			)
		}
		if len(msg.ToolCalls) > MaxToolCalls {
			return nil, Usage{}, fmt.Errorf(
				"%w: too many tool calls (maximum %d)", ErrProtocol, MaxToolCalls,
			)
		}
		seen := make(map[string]bool, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			if call.Type != "function" {
				return nil, Usage{}, fmt.Errorf("%w: expected function tool call", ErrProtocol)
			}
			if call.ID == "" {
				return nil, Usage{}, fmt.Errorf("%w: missing tool call ID", ErrProtocol)
			}
			if seen[call.ID] {
				return nil, Usage{}, fmt.Errorf("%w: duplicate tool call ID", ErrProtocol)
			}
			seen[call.ID] = true
		}
		return &msg, resp.Usage, nil
	}
	if choice.FinishReason != "stop" {
		return nil, Usage{}, fmt.Errorf(
			"%w: model stopped before completing the task (%s)",
			ErrProtocol, choice.FinishReason,
		)
	}
	if msg.Content == "" {
		return nil, Usage{}, fmt.Errorf("%w: model returned no final text", ErrProtocol)
	}
	return &msg, resp.Usage, nil
}

// ReplyJSON builds a canned Chat Completions body. Tests use it as the mock
// model's script, which keeps the fixtures as small as the assertions.
func ReplyJSON(message Message, finish string) []byte {
	body, _ := json.Marshal(response{
		Choices: []choice{{Message: message, FinishReason: finish}},
	})
	return body
}
