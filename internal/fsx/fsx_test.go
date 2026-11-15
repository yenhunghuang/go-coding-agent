package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePathStaysInsideWorkspace(t *testing.T) {
	// ResolvePath returns symlink-resolved paths, so the expectation has to be
	// resolved too. On macOS t.TempDir is /var/... but resolves to
	// /private/var/...; comparing the raw value would fail for every case.
	root := resolve(t, t.TempDir())
	outside := resolve(t, t.TempDir())

	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("real.txt", filepath.Join(root, "alias.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	cases := []struct {
		path    string
		wantErr string
	}{
		{"real.txt", ""},
		{"sub/nested.txt", ""}, // may not exist yet, but the parent does
		{"alias.txt", ""},      // a link that stays inside is fine
		{"", "expected nonempty path"},
		{".", "must stay inside workspace"},
		{"../escape", "must stay inside workspace"},
		{"sub/../../escape", "must stay inside workspace"},
		{"link/outside.txt", "must stay inside workspace"},
		{"no/parent.txt", "parent directory must already exist"},
		{"a\x00b", "without NUL"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got, err := ResolvePath(root, tc.path)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ResolvePath(%q): unexpected error %v", tc.path, err)
				}
				if !strings.HasPrefix(got, root) {
					t.Fatalf("ResolvePath(%q) = %q, outside %q", tc.path, got, root)
				}
				return
			}
			if err == nil {
				t.Fatalf("ResolvePath(%q) = %q, want error %q", tc.path, got, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ResolvePath(%q) error = %v, want %q", tc.path, err, tc.wantErr)
			}
		})
	}
}

func resolve(t *testing.T, path string) string {
	t.Helper()
	got, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestReadTextRejectsNonText(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name    string
		content []byte
		want    string
	}{
		{"binary", []byte("a\x00b"), "binary files are not supported"},
		{"invalid-utf8", []byte{0xff, 0xfe}, "not valid UTF-8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.name)
			if err := os.WriteFile(path, tc.content, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadText(path, MaxTextBytes); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ReadText error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReadTextSizeLimit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "big")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxTextBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadText(path, MaxTextBytes); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ReadText error = %v, want ErrTooLarge", err)
	}
}

func TestReadWriteRoundTrip(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note")
	if err := WriteText(path, "你好\n", MaxTextBytes); err != nil {
		t.Fatal(err)
	}
	got, err := ReadText(path, MaxTextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got != "你好\n" {
		t.Fatalf("ReadText = %q, want %q", got, "你好\n")
	}
	if err := WriteText(path, "a\x00b", MaxTextBytes); err == nil {
		t.Fatal("WriteText accepted content with NUL")
	}
	if err := WriteText(filepath.Join(root, "missing", "file"), "x", MaxTextBytes); err == nil {
		t.Fatal("WriteText created a missing parent directory")
	}
}

func TestCountLines(t *testing.T) {
	// The read tool's line numbering depends on this definition, so it is
	// pinned here rather than assumed.
	cases := []struct {
		content string
		want    int
	}{
		{"", 0},
		{"x", 1},
		{"x\n", 1},
		{"\n", 1},
		{"x\n\n", 2},
		{"a\r\nb\r\n", 2}, // CRLF keeps the carriage return inside the line
	}
	for _, tc := range cases {
		if got := CountLines(tc.content); got != tc.want {
			t.Errorf("CountLines(%q) = %d, want %d", tc.content, got, tc.want)
		}
	}
}
