package artifacts

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var ErrOutsideRoot = errors.New("artifact path is outside configured root")

const defaultMaxBytes int64 = 64 * 1024

type Reader struct {
	Root     string
	MaxBytes int64
}

type Tail struct {
	Content   string `json:"content"`
	SizeBytes int64  `json:"size_bytes"`
	Truncated bool   `json:"truncated"`
}

func (r Reader) ReadTail(path string, requestedBytes int64) (Tail, error) {
	root, err := filepath.Abs(r.Root)
	if err != nil {
		return Tail{}, fmt.Errorf("resolve artifact root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Tail{}, fmt.Errorf("resolve artifact root: %w", err)
	}
	resolvedPath, err := filepath.Abs(path)
	if err != nil {
		return Tail{}, fmt.Errorf("resolve artifact path: %w", err)
	}
	resolvedPath, err = filepath.EvalSymlinks(resolvedPath)
	if err != nil {
		return Tail{}, fmt.Errorf("resolve artifact path: %w", err)
	}
	relative, err := filepath.Rel(root, resolvedPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Tail{}, ErrOutsideRoot
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return Tail{}, fmt.Errorf("inspect artifact: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Tail{}, fmt.Errorf("artifact is not a regular file")
	}
	maxBytes := r.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	if requestedBytes <= 0 || requestedBytes > maxBytes {
		requestedBytes = maxBytes
	}
	start := info.Size() - requestedBytes
	if start < 0 {
		start = 0
	}
	file, err := os.Open(resolvedPath)
	if err != nil {
		return Tail{}, fmt.Errorf("open artifact: %w", err)
	}
	defer file.Close()
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return Tail{}, fmt.Errorf("seek artifact: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, requestedBytes))
	if err != nil {
		return Tail{}, fmt.Errorf("read artifact: %w", err)
	}
	truncated := start > 0
	if truncated {
		// start may land mid-rune; drop the dangling continuation bytes so the
		// tail begins on a valid UTF-8 boundary instead of being replaced with U+FFFD.
		skip := 0
		for skip < len(data) && !utf8.RuneStart(data[skip]) {
			skip++
		}
		data = data[skip:]
	}
	return Tail{Content: string(data), SizeBytes: info.Size(), Truncated: truncated}, nil
}
