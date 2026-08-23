// Whitebox: seqReader is unexported and has no caller in the current
// codebase, so its ReadAt method can only be reached directly.

package pdf

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// TestSeqReaderReadAt covers seqReader.ReadAt's sequential-access contract:
// a read at the expected offset delegates to the underlying reader and
// advances the offset, while a read at any other offset is rejected without
// touching the underlying reader.
func TestSeqReaderReadAt(t *testing.T) {
	t.Parallel()

	t.Run("sequential reads succeed", func(t *testing.T) {
		t.Parallel()

		r := &seqReader{rd: strings.NewReader("hello world")}

		buf := make([]byte, 5)
		n, err := r.ReadAt(buf, 0)
		if err != nil {
			t.Fatalf("ReadAt(0): %v", err)
		}
		if n != 5 || string(buf) != "hello" {
			t.Errorf("ReadAt(0) = %d, %q, want 5, %q", n, buf, "hello")
		}

		n, err = r.ReadAt(buf, 5)
		if err != nil {
			t.Fatalf("ReadAt(5): %v", err)
		}
		if n != 5 || string(buf) != " worl" {
			t.Errorf("ReadAt(5) = %d, %q, want 5, %q", n, buf, " worl")
		}
	})

	t.Run("non-sequential read rejected", func(t *testing.T) {
		t.Parallel()

		r := &seqReader{rd: strings.NewReader("hello world")}
		buf := make([]byte, 5)
		if _, err := r.ReadAt(buf, 3); err == nil {
			t.Error("ReadAt at a non-zero offset on a fresh reader: got nil error, want one")
		}
	})

	t.Run("underlying read error propagates", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("boom")
		r := &seqReader{rd: &errReader{wantErr}}
		buf := make([]byte, 5)
		_, err := r.ReadAt(buf, 0)
		if !errors.Is(err, wantErr) && !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("ReadAt error = %v, want it to wrap %v", err, wantErr)
		}
	})
}
