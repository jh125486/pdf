// Mutation-coverage workstream (WS13), kept in its own file so it can be
// reviewed/merged independently of other in-flight test additions.
//
// Whitebox: alphaReader, newAlphaReader, and checkASCII85 are unexported, so
// exercising the ASCII85 byte-classification helper and the tilde/end-marker
// state machine in alphaReader.Read directly requires package-internal
// access; there is no exported entry point that lets a blackbox test drive
// these code paths precisely (byte-for-byte) from outside the package.

package pdf

import (
	"errors"
	"testing"
)

func TestCheckASCII85(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   byte
		want byte
	}{
		{name: "low boundary accepted: '!' (33)", in: '!', want: '!'},
		{name: "just below low boundary rejected: ' ' (32)", in: ' ', want: 0},
		{name: "high boundary accepted: 'u' (117)", in: 'u', want: 'u'},
		{name: "just above high boundary rejected: 'v' (118)", in: 'v', want: 0},
		{name: "tilde is special-cased to 1", in: '~', want: 1},
		{name: "'>' is treated as an ordinary in-range byte", in: '>', want: '>'},
		{name: "out-of-range byte 0x00 rejected", in: 0x00, want: 0},
		{name: "out-of-range byte 0xFF rejected", in: 0xFF, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := checkASCII85(tt.in); got != tt.want {
				t.Errorf("checkASCII85(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// wsA85ErrReader is a minimal io.Reader that writes some bytes into p (to
// simulate a partial read) and then returns a non-nil, non-EOF error, so we
// can verify alphaReader.Read's error-propagation behavior.
type wsA85ErrReader struct {
	data []byte // bytes to copy into p before returning the error
	err  error
}

func (r *wsA85ErrReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	return n, r.err
}

// wsA85ZeroReader always reports a zero-length, error-free read regardless
// of the size of p, letting us exercise alphaReader.Read's zero-length path
// deterministically.
type wsA85ZeroReader struct{}

func (wsA85ZeroReader) Read(p []byte) (int, error) {
	return 0, nil
}

func TestAlphaReaderRead(t *testing.T) {
	t.Parallel()

	t.Run("genuine ~> end marker mid-buffer stops copying and zero-fills the rest", func(t *testing.T) {
		t.Parallel()

		in := []byte("AB~>CD")
		r := newAlphaReader(wsA85Reader(in))
		p := make([]byte, len(in))
		n, err := r.Read(p)

		if err != nil {
			t.Fatalf("Read() error = %v, want nil", err)
		}
		if n != len(in) {
			t.Fatalf("Read() n = %d, want %d (underlying read count passes through unchanged)", n, len(in))
		}
		want := []byte{'A', 'B', 0, 0, 0, 0}
		for i := range want {
			if p[i] != want[i] {
				t.Errorf("p[%d] = %v, want %v (full output %v)", i, p[i], want[i], p)
			}
		}
	})

	t.Run("bare '>' with no preceding '~' is an ordinary byte, passed through unchanged", func(t *testing.T) {
		t.Parallel()

		in := []byte("A>B")
		r := newAlphaReader(wsA85Reader(in))
		p := make([]byte, len(in))
		n, err := r.Read(p)

		if err != nil {
			t.Fatalf("Read() error = %v, want nil", err)
		}
		if n != len(in) {
			t.Fatalf("Read() n = %d, want %d", n, len(in))
		}
		if string(p) != string(in) {
			t.Errorf("p = %q, want %q (bare '>' must not trigger end-of-data)", p, in)
		}
	})

	t.Run("bare '~' alone sets pending state without stopping the copy", func(t *testing.T) {
		t.Parallel()

		in := []byte("~")
		r := newAlphaReader(wsA85Reader(in))
		p := make([]byte, len(in))
		n, err := r.Read(p)

		if err != nil {
			t.Fatalf("Read() error = %v, want nil", err)
		}
		if n != len(in) {
			t.Fatalf("Read() n = %d, want %d", n, len(in))
		}
		// checkASCII85('~') == 1, and the Read loop only writes buf[i] when
		// char > 1, so the lone tilde byte itself is zeroed, not passed
		// through, and no break occurs since '>' never appears.
		if p[0] != 0 {
			t.Errorf("p[0] = %v, want 0", p[0])
		}
	})

	t.Run("'~' followed by a non-'>' byte is not an immediate end marker, but the pending tilde state is sticky", func(t *testing.T) {
		t.Parallel()

		// The Read loop's `tilda` flag is never reset back to false once
		// set, so a '~' anywhere earlier in the buffer makes ANY later '>'
		// (not just an immediately-adjacent one) trigger the end-of-data
		// break. Verify both halves of that behavior in one buffer:
		//  - '~' followed immediately by 'A' (not '>') does NOT stop the copy.
		//  - the non-adjacent '>' two bytes later still triggers the break.
		in := []byte("~AB>C")
		r := newAlphaReader(wsA85Reader(in))
		p := make([]byte, len(in))
		n, err := r.Read(p)

		if err != nil {
			t.Fatalf("Read() error = %v, want nil", err)
		}
		if n != len(in) {
			t.Fatalf("Read() n = %d, want %d", n, len(in))
		}
		// index 0 '~' -> zeroed (char==1, not >1)
		// index 1 'A' -> passed through (65), no break since char != '>'
		// index 2 'B' -> passed through (66)
		// index 3 '>' -> tilda is still true from index 0, so this breaks
		//                before writing buf[3]
		// index 4 'C' -> never reached, stays zero
		want := []byte{0, 'A', 'B', 0, 0}
		for i := range want {
			if p[i] != want[i] {
				t.Errorf("p[%d] = %v, want %v (full output %v)", i, p[i], want[i], p)
			}
		}
	})

	t.Run("bytes outside the ASCII85 alphabet are zero-filled; valid bytes pass through unchanged", func(t *testing.T) {
		t.Parallel()

		in := []byte{'A', 0x00, 'B', 0xFF, 'C'}
		r := newAlphaReader(wsA85Reader(in))
		p := make([]byte, len(in))
		n, err := r.Read(p)

		if err != nil {
			t.Fatalf("Read() error = %v, want nil", err)
		}
		if n != len(in) {
			t.Fatalf("Read() n = %d, want %d", n, len(in))
		}
		want := []byte{'A', 0, 'B', 0, 'C'}
		for i := range want {
			if p[i] != want[i] {
				t.Errorf("p[%d] = %v, want %v (full output %v)", i, p[i], want[i], p)
			}
		}
	})

	t.Run("underlying read error is propagated unchanged, with the raw partial bytes left in p", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("boom")
		src := &wsA85ErrReader{data: []byte("AB"), err: wantErr}
		r := newAlphaReader(src)
		p := make([]byte, 4)
		n, err := r.Read(p)

		if !errors.Is(err, wantErr) {
			t.Fatalf("Read() error = %v, want %v", err, wantErr)
		}
		if n != 2 {
			t.Fatalf("Read() n = %d, want 2", n)
		}
		// On the error path, alphaReader.Read returns immediately after the
		// underlying Read call, before any ASCII85 filtering/zeroing runs,
		// so p keeps whatever the underlying reader wrote into it raw.
		want := []byte{'A', 'B', 0, 0}
		for i := range want {
			if p[i] != want[i] {
				t.Errorf("p[%d] = %v, want %v (full output %v)", i, p[i], want[i], p)
			}
		}
	})

	t.Run("zero-length read does not panic and returns 0, nil", func(t *testing.T) {
		t.Parallel()

		r := newAlphaReader(wsA85ZeroReader{})
		p := make([]byte, 0)
		n, err := r.Read(p)

		if err != nil {
			t.Fatalf("Read() error = %v, want nil", err)
		}
		if n != 0 {
			t.Fatalf("Read() n = %d, want 0", n)
		}
	})
}

// bytesReader adapts a []byte into a fresh io.Reader per call so each
// subtest gets its own independent, fully-buffered source (a single Read
// call returns the whole slice), keeping the alphaReader.Read behavior
// under test isolated from any buffering quirks of a shared reader type.
type wsA85BytesReader struct {
	data []byte
	done bool
}

func wsA85Reader(data []byte) *wsA85BytesReader {
	return &wsA85BytesReader{data: data}
}

func (r *wsA85BytesReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, nil
	}
	n := copy(p, r.data)
	r.done = true
	return n, nil
}
