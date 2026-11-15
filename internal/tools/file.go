package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/yenhunghuang/go-coding-agent/internal/config"
	"github.com/yenhunghuang/go-coding-agent/internal/fsx"
)

// readTool returns a bounded slice of a text file with 1-based line numbers.
type readTool struct{ cfg *config.Config }

func (t readTool) Name() string { return "read" }

// ReadResult is the read tool's entire output.
type ReadResult struct {
	Output     string `json:"output"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated"`
}

func (t readTool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		Limit     int    `json:"limit"`
		MaxOutput int    `json:"max_output"`
	}
	// Set the defaults first, then decode: an omitted key keeps its default,
	// and tools.json advertises the same numbers to the model.
	args.StartLine, args.Limit, args.MaxOutput = 1, 200, 16000
	if err := decode(raw, &args); err != nil {
		return nil, err
	}
	if err := requireText("path", args.Path); err != nil {
		return nil, err
	}
	if err := checkRange("start_line", args.StartLine, 1, 1_000_000); err != nil {
		return nil, err
	}
	if err := checkRange("limit", args.Limit, 1, 10_000); err != nil {
		return nil, err
	}
	if err := checkRange("max_output", args.MaxOutput, 1, 100_000); err != nil {
		return nil, err
	}
	path, err := fsx.ResolvePath(t.cfg.Workspace, args.Path)
	if err != nil {
		return nil, err
	}
	content, err := fsx.ReadText(path, fsx.MaxTextBytes)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(content, "\n")
	total := fsx.CountLines(content)
	start := args.StartLine - 1
	end := min(total, start+args.Limit)
	truncated := end < total

	var out strings.Builder
	used := 0
	for i := start; i < end; i++ {
		line := strconv.Itoa(i+1) + ": " + lines[i] + "\n"
		// Ranging over a string yields runes, that is Unicode code points, so
		// the cap can never cut a Chinese character in half.
		for _, r := range line {
			if used == args.MaxOutput {
				truncated = true
				break
			}
			out.WriteRune(r)
			used++
		}
		if truncated && used == args.MaxOutput {
			break
		}
	}
	return ReadResult{Output: out.String(), TotalLines: total, Truncated: truncated}, nil
}

// writeTool creates or fully overwrites a text file.
type writeTool struct{ cfg *config.Config }

func (t writeTool) Name() string { return "write" }

// WriteResult reports how much was written.
type WriteResult struct {
	BytesWritten int `json:"bytes_written"`
}

func (t writeTool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(raw, &args); err != nil {
		return nil, err
	}
	if err := requireText("path", args.Path); err != nil {
		return nil, err
	}
	path, err := fsx.ResolvePath(t.cfg.Workspace, args.Path)
	if err != nil {
		return nil, err
	}
	// Empty content is allowed: truncating a file to nothing is a legitimate
	// edit. Directories are not created, so "mkdir" stays an explicit bash call.
	if err := fsx.WriteText(path, args.Content, fsx.MaxTextBytes); err != nil {
		return nil, err
	}
	return WriteResult{BytesWritten: len(args.Content)}, nil
}

// editTool replaces exactly one occurrence of old_text. Validate first, mutate
// second: every rejection happens before the file is touched.
type editTool struct{ cfg *config.Config }

func (t editTool) Name() string { return "edit" }

// EditResult reports the number of replacements, which is always one.
type EditResult struct {
	Replacements int `json:"replacements"`
}

func (t editTool) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := decode(raw, &args); err != nil {
		return nil, err
	}
	if err := requireText("path", args.Path); err != nil {
		return nil, err
	}
	if err := requireText("old_text", args.OldText); err != nil {
		return nil, err
	}
	path, err := fsx.ResolvePath(t.cfg.Workspace, args.Path)
	if err != nil {
		return nil, err
	}
	content, err := fsx.ReadText(path, fsx.MaxTextBytes)
	if err != nil {
		return nil, err
	}
	first := strings.Index(content, args.OldText)
	if first < 0 {
		return nil, errors.New("old_text not found; read the file before editing")
	}
	// Resume one byte after the match, not after its end: in "aaa" the two
	// occurrences of "aa" overlap, and that is still ambiguous.
	if strings.Index(content[first+1:], args.OldText) >= 0 {
		return nil, errors.New("old_text is ambiguous; include more surrounding text")
	}
	updated := strings.Replace(content, args.OldText, args.NewText, 1)
	if err := fsx.WriteText(path, updated, fsx.MaxTextBytes); err != nil {
		return nil, err
	}
	return EditResult{Replacements: 1}, nil
}
