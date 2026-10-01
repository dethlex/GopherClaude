// Package logfile is a size-rotating log destination: the agent runs for
// months under launchd, which never rotates what it redirects, and a noisy
// warning once grew the log to 129 MB.
package logfile

import (
	"fmt"
	"os"
	"strconv"
	"sync"
)

const fileMode = 0o644

// Writer appends to path and, when the file would exceed maxBytes, renames
// it to path.1 (shifting older generations up, dropping the one past keep)
// and starts a new file. Safe for concurrent use.
type Writer struct {
	path     string
	maxBytes int64
	keep     int

	mu   sync.Mutex
	file *os.File
	size int64
}

// Open opens (or creates) the log at path. keep is the number of rotated
// generations retained besides the live file.
func Open(path string, maxBytes int64, keep int) (*Writer, error) {
	if maxBytes <= 0 || keep < 0 {
		return nil, fmt.Errorf("logfile: invalid limits max=%d keep=%d", maxBytes, keep)
	}
	w := &Writer{path: path, maxBytes: maxBytes, keep: keep}
	if err := w.open(); err != nil {
		return nil, err
	}

	return w, nil
}

func (w *Writer) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, fileMode)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()

		return err
	}
	w.file, w.size = f, info.Size()

	return nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			// Keep logging into the oversized file rather than lose lines.
			fmt.Fprintf(os.Stderr, "logfile: rotate %s: %v\n", w.path, err)
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)

	return n, err
}

// rotate shifts path.(keep-1) → path.keep, …, path → path.1 and reopens.
func (w *Writer) rotate() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	if w.keep == 0 {
		if err := os.Remove(w.path); err != nil {
			return err
		}

		return w.open()
	}
	for i := w.keep - 1; i >= 1; i-- {
		if err := os.Rename(w.generation(i), w.generation(i+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(w.path, w.generation(1)); err != nil {
		return err
	}

	return w.open()
}

func (w *Writer) generation(i int) string {
	return w.path + "." + strconv.Itoa(i)
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.file.Close()
}
