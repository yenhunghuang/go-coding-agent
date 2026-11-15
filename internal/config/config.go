// Package config turns command-line arguments and environment variables into
// one explicit Config value. There is no framework and no global state: the
// value is built here and passed down the call chain.
package config

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// DefaultBaseURL points at a local Bifrost gateway. Any OpenAI-compatible
// Chat Completions endpoint works; the client only appends /chat/completions.
const DefaultBaseURL = "http://localhost:8080/v1"

// KnownTools is the closed set of tools this teaching agent implements.
var KnownTools = []string{"read", "write", "edit", "bash"}

// Config is the whole program's configuration. Every layer receives it
// explicitly instead of reading environment variables on its own.
type Config struct {
	Prompt    string   // single user request
	Workspace string   // absolute, symlink-resolved directory
	BaseURL   string   // OpenAI-compatible base URL
	Model     string   // provider/model as configured in the gateway
	APIKey    string   // optional Bearer token
	Tools     []string // enabled tool names, in request order
	MaxTurns  int      // model requests, 1..100
	Stream    bool     // stream deltas instead of one JSON response
}

// Allows reports whether a tool may be seen by the model and executed.
func (c *Config) Allows(name string) bool {
	return slices.Contains(c.Tools, name)
}

const usage = `Usage: go run ./cmd/agent [flags] 'prompt'

  -workspace PATH   working directory (default .)
  -tools NAMES      read,write,edit,bash (or a teaching subset)
  -model MODEL      or OPENAI_MODEL
  -base-url URL     or OPENAI_BASE_URL (default ` + DefaultBaseURL + `)
  -max-turns N      model requests, 1..100 (default 20)
  -stream=false     disable streaming; one JSON response per turn

OPENAI_API_KEY supplies an optional Bearer token. bash is not a sandbox.`

// Parse builds a Config from args, validating everything up front so the rest
// of the program can assume the values are sane.
func Parse(args []string) (*Config, error) {
	c := &Config{
		Workspace: ".",
		BaseURL:   getenv("OPENAI_BASE_URL", DefaultBaseURL),
		Model:     os.Getenv("OPENAI_MODEL"),
		APIKey:    os.Getenv("OPENAI_API_KEY"),
		Tools:     slices.Clone(KnownTools),
		MaxTurns:  20,
		Stream:    true,
	}
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	// Silence flag's own output: main reports the error once, and --help is
	// printed to stdout by Describe.
	fs.SetOutput(io.Discard)
	var toolList string
	fs.StringVar(&c.Workspace, "workspace", c.Workspace, "working directory")
	fs.StringVar(&c.BaseURL, "base-url", c.BaseURL, "OpenAI-compatible base URL")
	fs.StringVar(&c.Model, "model", c.Model, "model name")
	fs.IntVar(&c.MaxTurns, "max-turns", c.MaxTurns, "model requests, 1..100")
	fs.StringVar(&toolList, "tools", "", "comma separated tools (default all)")
	fs.BoolVar(&c.Stream, "stream", c.Stream, "stream deltas to stderr")
	if err := fs.Parse(args); err != nil {
		return nil, err // flag.ErrHelp means --help was requested
	}
	switch rest := fs.Args(); len(rest) {
	case 0:
		c.Prompt = ""
	case 1:
		c.Prompt = rest[0]
	default:
		return nil, fmt.Errorf("quote the prompt as one shell argument")
	}
	if toolList != "" {
		c.Tools = nil
		for _, name := range strings.Split(toolList, ",") {
			if !slices.Contains(KnownTools, name) {
				return nil, fmt.Errorf("-tools accepts %s", strings.Join(KnownTools, ","))
			}
			if !c.Allows(name) {
				c.Tools = append(c.Tools, name)
			}
		}
	}
	if strings.TrimSpace(c.Prompt) == "" {
		return nil, fmt.Errorf("provide a prompt; use --help for usage")
	}
	if strings.TrimSpace(c.Model) == "" {
		return nil, fmt.Errorf("set OPENAI_MODEL or -model")
	}
	if c.MaxTurns < 1 || c.MaxTurns > 100 {
		return nil, fmt.Errorf("-max-turns must be 1..100")
	}
	root, err := filepath.Abs(c.Workspace)
	if err != nil {
		return nil, err
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return nil, fmt.Errorf("-workspace must be a directory: %w", err)
	}
	if info, err := os.Stat(root); err != nil {
		return nil, err
	} else if !info.IsDir() {
		return nil, fmt.Errorf("-workspace must be a directory")
	}
	c.Workspace = root
	return c, nil
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// Describe writes the usage text; main uses it when flag returns ErrHelp.
func Describe(w io.Writer) {
	fmt.Fprintln(w, usage)
}
