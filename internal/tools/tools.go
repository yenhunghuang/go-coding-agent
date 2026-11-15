// Package tools implements the four functions the model may call: read, write,
// edit and bash. Every tool follows the same three steps: validate arguments,
// perform one side effect, return a small typed result.
//
// The model-facing contract lives in tools.json next to the code, so a reader
// can see exactly what the model was told before reading the implementation.
package tools

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/yenhunghuang/go-coding-agent/internal/config"
	"github.com/yenhunghuang/go-coding-agent/internal/model"
)

//go:embed tools.json
var toolsJSON []byte

// Tool is one callable function. Arguments stay encoded until the tool itself
// decodes them, which keeps validation next to the values it protects.
type Tool interface {
	Name() string
	Call(ctx context.Context, args json.RawMessage) (any, error)
}

// Registry holds the enabled tools and the schemas to send with each request.
// Enabling a tool is the same decision in both directions: the model cannot see
// what cannot run, and a tool the model invents is rejected here.
type Registry struct {
	enabled map[string]Tool
	schemas []model.Schema
}

// NewRegistry pairs every enabled name in cfg with its schema and its
// implementation. A mismatch is a programming error, so it fails at startup
// rather than during a conversation.
func NewRegistry(cfg *config.Config) (*Registry, error) {
	var specs []model.Schema
	if err := json.Unmarshal(toolsJSON, &specs); err != nil {
		return nil, fmt.Errorf("tools.json: %w", err)
	}
	byName := make(map[string]model.Schema, len(specs))
	for _, spec := range specs {
		byName[spec.Function.Name] = spec
	}
	if len(byName) != len(specs) {
		return nil, fmt.Errorf("tools.json: duplicate tool names")
	}
	byImpl := make(map[string]Tool, len(config.KnownTools))
	for _, impl := range []Tool{
		readTool{cfg},
		writeTool{cfg},
		editTool{cfg},
		bashTool{cfg},
	} {
		byImpl[impl.Name()] = impl
	}
	r := &Registry{enabled: make(map[string]Tool, len(cfg.Tools))}
	for _, name := range cfg.Tools {
		spec, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("tools.json has no %q", name)
		}
		impl, ok := byImpl[name]
		if !ok {
			return nil, fmt.Errorf("no implementation for %q", name)
		}
		r.enabled[name] = impl
		r.schemas = append(r.schemas, spec)
	}
	return r, nil
}

// Schemas returns the tool definitions for the request, in enabled order.
func (r *Registry) Schemas() []model.Schema { return r.schemas }

// Execute runs one tool call and returns the JSON text stored in the tool
// message. It never returns an error: a failed tool is information for the
// model, and the {"ok":false,...} envelope is how it learns that.
func (r *Registry) Execute(ctx context.Context, name, arguments string) string {
	tool, ok := r.enabled[name]
	if !ok {
		return envelopeText(false, nil, "tool is not enabled: "+name)
	}
	result, err := tool.Call(ctx, json.RawMessage(arguments))
	if err != nil {
		return envelopeText(false, nil, err.Error())
	}
	return envelopeText(true, result, "")
}

// envelope is the single shape every tool result takes on the wire.
type envelope struct {
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

func envelopeText(ok bool, result any, reason string) string {
	payload, err := json.Marshal(envelope{OK: ok, Result: result, Error: reason})
	if err != nil {
		payload = []byte(`{"ok":false,"error":"cannot encode tool result"}`)
	}
	return string(payload)
}

// decode requires a JSON object with no unexpected keys. A bare "[...]" or a
// stray field is a mistake by the model, and saying so is more useful than
// quietly ignoring it.
func decode(raw json.RawMessage, target any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// checkRange validates an integer argument against the same bounds the schema
// advertises. Keeping both lists in sync is the price of validating twice; the
// benefit is that a hostile or confused model cannot exceed the limit.
func checkRange(name string, value, minimum, maximum int) error {
	if value < minimum || value > maximum {
		return fmt.Errorf(
			"argument out of range: %s (want %d..%d)", name, minimum, maximum,
		)
	}
	return nil
}

// requireText rejects an argument the schema marks as required and nonempty.
func requireText(name, value string) error {
	if value == "" {
		return fmt.Errorf("argument must not be empty: %s", name)
	}
	return nil
}
