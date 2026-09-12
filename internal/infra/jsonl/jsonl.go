// Package jsonl reads the append-only JSON-lines files the assistants keep
// (transcripts, rollouts): a bounded tail for heuristics, and a cursor that
// parses only what was appended since the last pass.
package jsonl

import (
	"bufio"
	"errors"
	"io"
	"os"
)

// Lines can be megabytes (tool outputs are inlined); a large buffer keeps
// the per-line syscalls down.
const readBufferSize = 256 * 1024

// Tail returns up to maxBytes from the end of the file. The first line of
// the result is usually partial; callers skip what does not parse.
func Tail(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	offset := info.Size() - maxBytes
	if offset < 0 {
		offset = 0
	}

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}

	return io.ReadAll(f)
}

// Cursor remembers how far an append-only JSONL file has been read.
type Cursor struct {
	Offset int64
}

// ReadNew hands every complete line appended since Offset to onLine and
// advances Offset past it. A trailing fragment without a newline (a line the
// writer is still producing) waits for the next call. A file smaller than
// Offset was rewritten: onReset fires (when not nil) so the caller can drop
// what it derived from the old content, and reading restarts at the top.
func (c *Cursor) ReadNew(path string, onLine func(line []byte), onReset func()) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if info.Size() < c.Offset {
		c.Offset = 0

		if onReset != nil {
			onReset()
		}
	}

	if info.Size() == c.Offset {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Seek(c.Offset, io.SeekStart); err != nil {
		return err
	}

	reader := bufio.NewReaderSize(f, readBufferSize)

	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return nil // a partial line: read it next time
		}

		if err != nil {
			return err
		}

		c.Offset += int64(len(line))

		onLine(line)
	}
}
