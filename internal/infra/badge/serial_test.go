package badge

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type recordingWriter struct{ chunks [][]byte }

func (r *recordingWriter) Write(p []byte) (int, error) {
	r.chunks = append(r.chunks, append([]byte(nil), p...))

	return len(p), nil
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("port gone") }

// The badge's USB receive ring holds 512 bytes and drops the rest, so a
// frame goes out in pieces with a pause between them, never after the last.
func TestWriteChunked(t *testing.T) {
	data := []byte(strings.Repeat("x", 300))
	w := &recordingWriter{}
	pauses := 0

	if err := writeChunked(w, data, 128, func() { pauses++ }); err != nil {
		t.Fatal(err)
	}

	if len(w.chunks) != 3 || len(w.chunks[0]) != 128 || len(w.chunks[1]) != 128 || len(w.chunks[2]) != 44 {
		sizes := make([]int, 0, len(w.chunks))
		for _, c := range w.chunks {
			sizes = append(sizes, len(c))
		}

		t.Errorf("chunk sizes = %v, want [128 128 44]", sizes)
	}

	if pauses != 2 {
		t.Errorf("pauses = %d, want 2 (between chunks only)", pauses)
	}

	if !bytes.Equal(bytes.Join(w.chunks, nil), data) {
		t.Error("chunks do not reassemble into the frame")
	}

	if frameChunkSize*2 > 512 {
		t.Errorf("frameChunkSize = %d: two chunks must fit the badge's 512-byte ring", frameChunkSize)
	}
}

func TestWriteChunkedShortFrame(t *testing.T) {
	w := &recordingWriter{}
	pauses := 0

	if err := writeChunked(w, []byte("CC7|0|0\n"), 128, func() { pauses++ }); err != nil {
		t.Fatal(err)
	}

	if len(w.chunks) != 1 || pauses != 0 {
		t.Errorf("short frame: chunks=%d pauses=%d, want 1/0", len(w.chunks), pauses)
	}
}

func TestWriteChunkedReportsWriteError(t *testing.T) {
	if err := writeChunked(failingWriter{}, []byte("CC7|0|0\n"), 128, func() {}); err == nil {
		t.Error("writeChunked = nil error, want the port's")
	}
}
