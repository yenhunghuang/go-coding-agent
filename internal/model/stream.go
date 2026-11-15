package model

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// chunk is one server-sent event payload in a streamed response.
type chunk struct {
	Choices []struct {
		Delta struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// Stream runs one turn as a stream of deltas and returns the assembled message.
//
// onDelta is a callback rather than a channel: the reader is a single goroutine
// and the caller stops the turn by returning an error. That keeps cancellation
// in one place (ctx) instead of spreading it across select statements.
func (c *Client) Stream(
	ctx context.Context,
	modelName string,
	messages []Message,
	schemas []Schema,
	onDelta func(string) error,
) (*Message, Usage, error) {
	resp, err := c.post(ctx, newRequest(modelName, messages, schemas, true))
	if err != nil {
		return nil, Usage{}, err
	}
	defer resp.Body.Close()

	var (
		usage   Usage
		finish  string
		content strings.Builder
		calls   = map[int]*ToolCall{}
		order   []int
		fed     int64
	)

	// ReadString, not bufio.Scanner: Scanner caps line length, and a single
	// tool-call delta can be longer than the default 64 KiB token.
	reader := bufio.NewReader(resp.Body)
	for {
		line, readErr := reader.ReadString('\n')
		fed += int64(len(line))
		if fed > c.MaxResponse {
			return nil, Usage{}, ErrTooLarge
		}
		if payload, ok := sseData(line); ok {
			if payload == "[DONE]" {
				break
			}
			var event chunk
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				return nil, Usage{}, fmt.Errorf("%w: %v", ErrProtocol, err)
			}
			if event.Usage != nil {
				usage = *event.Usage
			}
			for _, ch := range event.Choices {
				if ch.FinishReason != "" {
					finish = ch.FinishReason
				}
				if ch.Delta.Content != "" {
					content.WriteString(ch.Delta.Content)
					if err := onDelta(ch.Delta.Content); err != nil {
						return nil, Usage{}, err
					}
				}
				for _, delta := range ch.Delta.ToolCalls {
					call, started := calls[delta.Index]
					if !started {
						call = &ToolCall{Type: "function"}
						calls[delta.Index] = call
						order = append(order, delta.Index)
					}
					if delta.ID != "" {
						call.ID = delta.ID
					}
					if delta.Type != "" {
						call.Type = delta.Type
					}
					call.Function.Name += delta.Function.Name
					call.Function.Arguments += delta.Function.Arguments
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			if ctx.Err() != nil {
				return nil, Usage{}, ctx.Err()
			}
			return nil, Usage{}, fmt.Errorf("model stream: %w", readErr)
		}
	}

	var assembled []ToolCall
	for _, index := range order {
		assembled = append(assembled, *calls[index])
	}
	if finish == "" {
		// Some gateways end the stream without a final finish_reason. Infer it
		// from what arrived, then let validate apply the same rules as before.
		switch {
		case len(assembled) > 0:
			finish = "tool_calls"
		case content.Len() > 0:
			finish = "stop"
		}
	}
	msg, usage, err := validate(&response{
		Choices: []choice{{
			Message: Message{
				Role:      "assistant",
				Content:   content.String(),
				ToolCalls: assembled,
			},
			FinishReason: finish,
		}},
		Usage: usage,
	})
	if err != nil {
		return nil, Usage{}, err
	}
	return msg, usage, nil
}

// sseData extracts the payload of one "data:" line. Comments, event names and
// blank separator lines return ok == false.
func sseData(line string) (string, bool) {
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "data:")), true
}
