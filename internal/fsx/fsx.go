// Package fsx keeps file access inside one workspace and bounded in size.
// It is deliberately small: path checks, text reads and text writes.
package fsx

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// MaxTextBytes is the largest file this agent will read or write.
const MaxTextBytes = 1 << 20 // 1 MiB

// ErrTooLarge means a file crossed MaxTextBytes.
var ErrTooLarge = errors.New("file exceeds size limit")

// ResolvePath maps a model-supplied path onto the workspace and refuses to
// leave it. Symlinks are resolved before the check, so a link inside the
// workspace cannot point outside it.
func ResolvePath(workspace, path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", errors.New("expected nonempty path without NUL")
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(path))
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	} else {
		// The file may not exist yet: resolve the parent so a symlinked
		// directory is still caught before anything is written.
		dir, dirErr := filepath.EvalSymlinks(filepath.Dir(target))
		if dirErr != nil {
			return "", errors.New("parent directory must already exist")
		}
		target = filepath.Join(dir, filepath.Base(target))
	}
	if target == root || !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", errors.New("path must stay inside workspace")
	}
	if info, err := os.Stat(target); err == nil && !info.Mode().IsRegular() {
		return "", errors.New("path must be a regular file")
	}
	return target, nil
}

// ReadText reads at most max bytes of UTF-8 text. io.LimitReader means a huge
// file never lands in memory: reading max+1 bytes is enough to detect it.
func ReadText(path string, max int64) (string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("expected an existing regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > max {
		return "", ErrTooLarge
	}
	if strings.ContainsRune(string(data), 0) {
		return "", errors.New("binary files are not supported")
	}
	if !utf8.Valid(data) {
		return "", errors.New("file is not valid UTF-8")
	}
	return string(data), nil
}

// WriteText replaces a file's content in one call. The parent directory must
// already exist, which keeps "create a directory" an explicit bash step.
func WriteText(path, content string, max int64) error {
	if int64(len(content)) > max {
		return ErrTooLarge
	}
	if strings.ContainsRune(content, 0) {
		return errors.New("expected text without NUL")
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return errors.New("parent directory must already exist")
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// CountLines matches the read tool's definition: LF separates lines, a
// trailing newline does not add an empty last line, and empty input has none.
func CountLines(content string) int {
	if content == "" {
		return 0
	}
	n := strings.Count(content, "\n") + 1
	if strings.HasSuffix(content, "\n") {
		n--
	}
	return n
}
