package config

import (
	"errors"
	"flag"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseFlags(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Parse([]string{
		"-workspace", dir,
		"-model", "test/model",
		"-base-url", "http://127.0.0.1:9/v1",
		"-tools", "read,write",
		"-max-turns", "3",
		"-stream=false",
		"fix the failing test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Prompt != "fix the failing test" {
		t.Errorf("Prompt = %q", cfg.Prompt)
	}
	if cfg.Model != "test/model" || cfg.MaxTurns != 3 || cfg.Stream {
		t.Errorf("cfg = %+v", cfg)
	}
	if !cfg.Allows("read") || !cfg.Allows("write") || cfg.Allows("bash") {
		t.Errorf("Tools = %v", cfg.Tools)
	}
}

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse([]string{"-model", "m", "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxTurns != 20 || !cfg.Stream {
		t.Errorf("cfg = %+v", cfg)
	}
	if !slices.Equal(cfg.Tools, KnownTools) {
		t.Errorf("Tools = %v, want %v", cfg.Tools, KnownTools)
	}
	if !strings.HasPrefix(cfg.BaseURL, "http") {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	// The workspace is resolved, so later path checks compare absolute paths.
	// filepath.IsAbs, not a "/" prefix: an absolute path on Windows starts
	// with a drive letter.
	if !filepath.IsAbs(cfg.Workspace) {
		t.Errorf("Workspace = %q is not absolute", cfg.Workspace)
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	cases := map[string][]string{
		"no prompt":         {"-model", "m"},
		"two positional":    {"-model", "m", "one", "two"},
		"no model":          {"hello"},
		"unknown tool":      {"-model", "m", "-tools", "read,rm", "hello"},
		"max-turns zero":    {"-model", "m", "-max-turns", "0", "hello"},
		"max-turns large":   {"-model", "m", "-max-turns", "101", "hello"},
		"unknown flag":      {"-model", "m", "-nope", "hello"},
		"workspace missing": {"-model", "m", "-workspace", "/no/such/directory", "hello"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			// Clear the environment so "no model" really means no model.
			t.Setenv("OPENAI_MODEL", "")
			if _, err := Parse(args); err == nil {
				t.Fatalf("Parse(%v) succeeded", args)
			}
		})
	}
}

func TestHelpIsNotAnError(t *testing.T) {
	_, err := Parse([]string{"--help"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("error = %v, want flag.ErrHelp", err)
	}
}
