// Whitebox: exercises buffer.readObject's nesting-depth guard directly
// (newBuffer, buffer are unexported), and decryptString/decryptStream/
// cryptKey directly, since none of the fixtures elsewhere in this package
// carry real, independently-derivable RC4/AES key material to drive them
// through the public API.

package pdf

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rc4" //nolint:gosec // G503: RC4 is a PDF spec-mandated legacy encryption filter this library must decrypt
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestReadObjectDepthLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		build      func(depth int) string
		wantPanic  bool
		wantSubstr string
	}{
		{
			name: "nested obj tokens panic",
			build: func(depth int) string {
				return strings.Repeat("0 0 obj\n", depth)
			},
			wantPanic:  true,
			wantSubstr: "maximum depth",
		},
		{
			name: "nested dicts panic",
			build: func(depth int) string {
				return strings.Repeat("<< /A ", depth) + "null" + strings.Repeat(" >>", depth)
			},
			wantPanic: true,
		},
		{
			name: "nested arrays panic",
			build: func(depth int) string {
				return strings.Repeat("[ ", depth) + "null" + strings.Repeat(" ]", depth)
			},
			wantPanic: true,
		},
	}

	// Craft payloads of deeply nested tokens that would previously cause
	// unbounded recursion and a fatal stack overflow. With the depth limit
	// in place this must produce a recoverable panic (caught here) instead
	// of crashing the process.
	const depth = maxObjectDepth + 100

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBuffer(strings.NewReader(tt.build(depth)), 0)
			b.allowEOF = true

			panicked := false
			var panicMsg string
			func() {
				defer func() {
					if r := recover(); r != nil {
						panicked = true
						if err, ok := r.(error); ok {
							panicMsg = err.Error()
						}
					}
				}()
				b.readObject()
			}()

			if panicked != tt.wantPanic {
				t.Fatalf("panicked = %v, want %v", panicked, tt.wantPanic)
			}
			if tt.wantSubstr != "" && !strings.Contains(panicMsg, tt.wantSubstr) {
				t.Fatalf("panic message = %q, want it to contain %q", panicMsg, tt.wantSubstr)
			}
		})
	}
}

// TestReadObjectNormalDepth checks that a moderately nested structure, well
// under the limit, still parses without hitting it.
func TestReadObjectNormalDepth(t *testing.T) {
	t.Parallel()

	const depth = 50
	payload := strings.Repeat("<< /A ", depth) + "(hello)" + strings.Repeat(" >>", depth)

	b := newBuffer(strings.NewReader(payload), 0)
	b.allowEOF = true

	panicked := false
	var obj object
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		obj = b.readObject()
	}()

	if panicked {
		t.Fatal("unexpected panic from moderately nested dicts")
	}
	if obj == nil {
		t.Fatal("expected non-nil object from moderately nested dict")
	}
}

// TestLargeXrefTableGrows checks that a table bigger than the preallocation
// cap is still built in full, by growing as entries are actually read.
// Capping the preallocation must not cap the table. This reads r.xref
// directly, which is unexported.
func TestLargeXrefTableGrows(t *testing.T) {
	t.Parallel()

	const n = maxXrefPrealloc + 5000

	var entries bytes.Buffer
	for range n {
		entries.Write([]byte{1, 9, 0}) // type 1, offset 9, generation 0
	}
	data := xrefStreamPDF(fmt.Sprintf("/Size %d /W [1 1 1]", n), entries.String())

	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := len(r.xref); got != n {
		t.Errorf("xref table has %d entries, want %d", got, n)
	}
}

// TestCompressedXrefStreamManyObjects is why the table is not bounded by the
// file length. An xref stream is compressed, so a file of a few hundred
// bytes can legitimately describe a hundred thousand objects, and a
// length-derived bound would reject it. This reads r.xref directly, which
// is unexported.
func TestCompressedXrefStreamManyObjects(t *testing.T) {
	t.Parallel()

	const n = 100000

	var raw bytes.Buffer
	for range n {
		raw.Write([]byte{1, 9, 0})
	}
	var comp bytes.Buffer
	zw := zlib.NewWriter(&comp)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	hdr := fmt.Sprintf("/Size %d /W [1 1 1] /Filter /FlateDecode", n)
	data := xrefStreamPDF(hdr, comp.String())
	if len(data) > 8192 {
		t.Fatalf("test file grew to %d bytes; it needs to stay far smaller than %d objects", len(data), n)
	}

	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("NewReader on a %d-byte file describing %d objects: %v", len(data), n, err)
	}
	if got := len(r.xref); got != n {
		t.Errorf("xref table has %d entries, want %d", got, n)
	}
}

// TestDecryptString covers both branches of decryptString: RC4 (a
// symmetric stream cipher, so decrypting data XORed with the same key
// recovers it) and AES-CBC (decryptString only decrypts, so the fixture
// encrypts with a matching CBC encrypter first), plus the two malformed-
// input panics on the AES path (ciphertext shorter than one block, and a
// length that isn't a whole number of blocks).
func TestDecryptString(t *testing.T) {
	t.Parallel()

	key := []byte("0123456789abcdef")
	ptr := objptr{7, 0}

	t.Run("rc4", func(t *testing.T) {
		t.Parallel()

		plain := "hello, rc4"
		objKey := cryptKey(key, false, ptr)
		c, err := rc4.NewCipher(objKey) //nolint:gosec // G405: exercising the library's own PDF RC4 decrypt path
		if err != nil {
			t.Fatalf("rc4.NewCipher: %v", err)
		}
		cipherBytes := []byte(plain)
		c.XORKeyStream(cipherBytes, cipherBytes)

		got := decryptString(key, false, ptr, string(cipherBytes))
		if got != plain {
			t.Errorf("decryptString(rc4) = %q, want %q", got, plain)
		}
	})

	t.Run("aes", func(t *testing.T) {
		t.Parallel()

		plain := "0123456789abcdef" // exactly one AES block, so no padding to reason about
		objKey := cryptKey(key, true, ptr)
		block, err := aes.NewCipher(objKey)
		if err != nil {
			t.Fatalf("aes.NewCipher: %v", err)
		}
		iv := bytes.Repeat([]byte{0x01}, aes.BlockSize)
		enc := make([]byte, len(plain))
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(enc, []byte(plain))
		ciphertext := string(iv) + string(enc)

		got := decryptString(key, true, ptr, ciphertext)
		if got != plain {
			t.Errorf("decryptString(aes) = %q, want %q", got, plain)
		}
	})

	t.Run("aes ciphertext shorter than block size panics", func(t *testing.T) {
		t.Parallel()

		defer func() {
			if recover() == nil {
				t.Error("decryptString: no panic on short AES ciphertext, want one")
			}
		}()
		decryptString(key, true, ptr, "short")
	})

	t.Run("aes ciphertext not a block multiple panics", func(t *testing.T) {
		t.Parallel()

		defer func() {
			if recover() == nil {
				t.Error("decryptString: no panic on misaligned AES ciphertext, want one")
			}
		}()
		// One full IV block plus a partial data block.
		decryptString(key, true, ptr, strings.Repeat("x", aes.BlockSize+3))
	})
}

// TestDecryptStreamRC4 covers decryptStream's RC4 branch (the AES branch is
// already exercised end to end by TestAESEncryptMetadataFalse). RC4 is a
// symmetric stream cipher, so running the same key over ciphertext produced
// by manually XORing recovers the plaintext.
func TestDecryptStreamRC4(t *testing.T) {
	t.Parallel()

	key := []byte("0123456789abcdef")
	ptr := objptr{3, 0}
	plain := "hello, stream"

	objKey := cryptKey(key, false, ptr)
	c, err := rc4.NewCipher(objKey) //nolint:gosec // G405: exercising the library's own PDF RC4 decrypt path
	if err != nil {
		t.Fatalf("rc4.NewCipher: %v", err)
	}
	cipherBytes := []byte(plain)
	c.XORKeyStream(cipherBytes, cipherBytes)

	rd := decryptStream(key, false, ptr, bytes.NewReader(cipherBytes))
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("reading decrypted stream: %v", err)
	}
	if string(got) != plain {
		t.Errorf("decryptStream(rc4) = %q, want %q", got, plain)
	}
}
