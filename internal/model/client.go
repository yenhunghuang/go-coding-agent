package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is one HTTP client plus the limits that apply to every request.
// Construct it once and pass it around; it is safe for concurrent use.
type Client struct {
	BaseURL     string
	APIKey      string
	HTTP        *http.Client
	MaxResponse int64
}

// New builds a client for an OpenAI-compatible base URL. The URL keeps whatever
// prefix the service needs; only /chat/completions is appended.
func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL:     baseURL,
		APIKey:      apiKey,
		HTTP:        &http.Client{Timeout: 120 * time.Second},
		MaxResponse: 4 << 20, // 4 MiB
	}
}

func newRequest(modelName string, messages []Message, schemas []Schema, stream bool) *request {
	return &request{
		Model:    modelName,
		Messages: messages,
		Tools:    schemas,
		Stream:   stream,
	}
}

// post sends the request and returns an open body on status 200. The caller
// owns the body and must close it. Any other status is closed here and
// reported as an HTTPError.
func (c *Client) post(ctx context.Context, body *request) (*http.Response, error) {
	if !strings.HasPrefix(c.BaseURL, "http://") &&
		!strings.HasPrefix(c.BaseURL, "https://") {
		return nil, errors.New("base URL must use http:// or https://")
	}
	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if body.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		// Report the cancellation the caller asked for, not the transport's
		// wording for it.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("model request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Drain a little so the connection can be reused, then drop the body.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		return nil, &HTTPError{Status: resp.StatusCode}
	}
	return resp, nil
}

// Complete runs one turn as a single JSON response.
func (c *Client) Complete(
	ctx context.Context,
	modelName string,
	messages []Message,
	schemas []Schema,
) (*Message, Usage, error) {
	resp, err := c.post(ctx, newRequest(modelName, messages, schemas, false))
	if err != nil {
		return nil, Usage{}, err
	}
	defer resp.Body.Close()

	// Read one byte past the limit: that is enough to tell "exactly at the
	// limit" from "over the limit" without buffering an unbounded body.
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.MaxResponse+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, Usage{}, ctxErr
		}
		return nil, Usage{}, fmt.Errorf("model response: %w", err)
	}
	if int64(len(data)) > c.MaxResponse {
		return nil, Usage{}, ErrTooLarge
	}
	var decoded response
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, Usage{}, fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	return validate(&decoded)
}
