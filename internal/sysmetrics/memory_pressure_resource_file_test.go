package sysmetrics

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestReadResourceFileCompleteBoundaries(t *testing.T) {
	for _, size := range []int{0, 1, 511, 512, 513, 1024, 4095, 4096, 4097, 65537, 128 << 10} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			payload := bytes.Repeat([]byte{'a', '\\', '0', '4', '0', '\n'}, (size+5)/6)[:size]
			path := filepath.Join(t.TempDir(), "resource")
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readResourceFile(path)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("size=%d read=%d err=%v", size, len(got), err)
			}
		})
	}
	if _, err := readResourceFile(t.TempDir()); err == nil {
		t.Fatal("directory error was normalized as EOF")
	}
}

func TestReadResourceFileReturnsPartialNonEOFError(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var readerOnce, writerOnce sync.Once
	closeReader := func() { readerOnce.Do(func() { _ = reader.Close() }) }
	closeWriter := func() { writerOnce.Do(func() { _ = writer.Close() }) }
	t.Cleanup(closeReader)
	t.Cleanup(closeWriter)
	if err := reader.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := writer.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	payload := []byte("resource-prefix\n")
	type writeResult struct {
		n   int
		err error
	}
	result := make(chan writeResult, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		n, err := writer.Write(payload)
		// Keep the writer open after this goroutine joins: the reader must
		// return the complete prefix and its non-EOF read-deadline error.
		result <- writeResult{n, err}
	}()
	t.Cleanup(func() {
		closeReader()
		closeWriter()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("resource pipe writer did not join")
		}
	})
	got, readErr := readResourceFileContents(reader)
	if readErr == nil || errors.Is(readErr, io.EOF) || !errors.Is(readErr, os.ErrDeadlineExceeded) || !bytes.Equal(got, payload) {
		t.Fatalf("partial read bytes=%d/%d nonEOFerror=%v", len(got), len(payload), readErr)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("resource pipe writer did not join")
	}
	written := <-result
	if written.err != nil || written.n != len(payload) {
		t.Fatalf("pipe write=%d/%d err=%v", written.n, len(payload), written.err)
	}
}
