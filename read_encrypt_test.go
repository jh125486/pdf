// This file is a mutation-coverage workstream (WS6: encryption parameter
// validation) kept separate so it can be reviewed and merged independently
// of other in-flight mutation-coverage work. It exercises initEncrypt's
// boundary checks in read.go: the key-length range, the /V check, the R<2
// and R>4 bounds, the /O and /U length check, and (best-effort) the
// R==2/R>=3 split in both the 50-round key-strengthening loop and the
// u-computation.
//
// TestInitEncryptV4Checks in read_test.go already covers /Length 41/8/256
// and the /V variants (including every okayV4 sub-check); this file adds
// only the additional boundary cases it does not already cover.
package pdf_test

import (
	"bytes"
	"crypto/md5" //nolint:gosec // required by the PDF 32000-1:2008 Standard Security Handler algorithm under test, same as read.go
	"crypto/rc4" //nolint:gosec // required by the PDF 32000-1:2008 Standard Security Handler algorithm under test, same as read.go
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jh125486/pdf"
)

// wsEncryptID is the fixed /ID first element encryptedPDF bakes into every
// fixture it builds (see encryptedPDF's idClause). initEncrypt reads only
// this first ID element, so it is also the ID wsEncryptComputeU must hash
// against for an accept-path fixture built through encryptedPDF to work.
var wsEncryptID = []byte("0123456789ABCDEF")

// wsEncryptPasswordPad is the PDF 32000-1:2008 Algorithm 2 step (a) padding
// string (Table 3.2 in older spec editions), reproduced here so this
// blackbox test file can compute a real /U value without access to
// read.go's unexported passwordPad.
var wsEncryptPasswordPad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// TestInitEncryptKeyLengthBoundaries covers the /Length range check in
// initEncrypt: `n%8 != 0 || n > 128 || n < 40`. Values inside the accepted
// range (including the 40-bit default when /Length is absent) must reach
// past this check to the eventual password comparison and fail there
// instead, proving the boundary itself let them through.
func TestInitEncryptKeyLengthBoundaries(t *testing.T) {
	t.Parallel()

	garbage := strings.Repeat("X", 32)
	tests := []struct {
		name         string
		lengthClause string
		wantKeyLen   string // non-empty: substring of the key-length error
	}{
		{"40 accepted", "/Length 40 ", ""},
		{"128 accepted", "/Length 128 ", ""},
		{"absent defaults to 40, accepted", "", ""},
		{"32 too small", "/Length 32 ", "32-bit encryption key"},
		{"136 too large", "/Length 136 ", "136-bit encryption key"},
		{"44 not a multiple of 8", "/Length 44 ", "44-bit encryption key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encrypt := fmt.Sprintf("<< /Filter /Standard /V 2 %s/R 3 /O (%s) /U (%s) >>",
				tt.lengthClause, garbage, garbage)
			data := encryptedPDF(encrypt, false)
			_, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
			if err == nil {
				t.Fatal("NewReader on an encrypted PDF with no usable key material: got nil error, want one")
			}
			if tt.wantKeyLen == "" {
				if strings.Contains(err.Error(), "bit encryption key") {
					t.Errorf("NewReader error = %q, want it to NOT be the key-length error", err)
				}
				if !errors.Is(err, pdf.ErrInvalidPassword) {
					t.Errorf("NewReader error = %v, want it to be ErrInvalidPassword", err)
				}
				return
			}
			if !strings.Contains(err.Error(), tt.wantKeyLen) {
				t.Errorf("NewReader error = %q, want it to contain %q", err, tt.wantKeyLen)
			}
		})
	}
}

// TestInitEncryptRevisionBoundaries covers the R<2 and R>4 bound checks,
// which use two differently-worded errors ("malformed" for too low,
// "unsupported" for too high).
func TestInitEncryptRevisionBoundaries(t *testing.T) {
	t.Parallel()

	garbage := strings.Repeat("X", 32)
	tests := []struct {
		name       string
		r          int
		wantErrSub string
	}{
		{"R=0 malformed, too low", 0, "malformed PDF: encryption revision R=0"},
		{"R=1 malformed, too low", 1, "malformed PDF: encryption revision R=1"},
		{"R=5 unsupported, too high", 5, "unsupported PDF: encryption revision R=5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encrypt := fmt.Sprintf("<< /Filter /Standard /V 2 /R %d /O (%s) /U (%s) >>", tt.r, garbage, garbage)
			data := encryptedPDF(encrypt, false)
			_, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
			if err == nil {
				t.Fatal("NewReader with an out-of-range /R: got nil error, want one")
			}
			if !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Errorf("NewReader error = %q, want it to contain %q", err, tt.wantErrSub)
			}
		})
	}
}

// TestInitEncryptRevisionInRangeReachesPasswordCheck covers R==2, R==3, and
// R==4 all running past the revision check to the password comparison
// (ErrInvalidPassword via errors.Is, not a revision error), proving every
// revision in the accepted range is let through rather than just one.
func TestInitEncryptRevisionInRangeReachesPasswordCheck(t *testing.T) {
	t.Parallel()

	garbage := strings.Repeat("X", 32)
	for _, r := range []int{2, 3, 4} {
		t.Run(fmt.Sprintf("R=%d", r), func(t *testing.T) {
			t.Parallel()

			encrypt := fmt.Sprintf("<< /Filter /Standard /V 2 /R %d /O (%s) /U (%s) >>", r, garbage, garbage)
			data := encryptedPDF(encrypt, false)
			_, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
			if err == nil {
				t.Fatal("NewReader with garbage O/U and no password: got nil error, want one")
			}
			if !errors.Is(err, pdf.ErrInvalidPassword) {
				t.Errorf("NewReader error = %v, want it to be ErrInvalidPassword (proves R=%d reached the password check)", err, r)
			}
		})
	}
}

// TestInitEncryptVersionBoundaries covers the /V check: 1 and 2 are
// accepted outright (reaching the password check), while 0 and 5 are
// rejected by the same "unsupported PDF: encryption version" error used for
// an unsupported V=4 configuration.
func TestInitEncryptVersionBoundaries(t *testing.T) {
	t.Parallel()

	garbage := strings.Repeat("X", 32)
	tests := []struct {
		name       string
		v          int
		wantAccept bool
	}{
		{"V=1 accepted", 1, true},
		{"V=2 accepted", 2, true},
		{"V=0 rejected", 0, false},
		{"V=5 rejected", 5, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encrypt := fmt.Sprintf("<< /Filter /Standard /V %d /R 3 /O (%s) /U (%s) >>", tt.v, garbage, garbage)
			data := encryptedPDF(encrypt, false)
			_, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
			if err == nil {
				t.Fatal("NewReader on an encrypted PDF with no usable key material: got nil error, want one")
			}
			if tt.wantAccept {
				if !errors.Is(err, pdf.ErrInvalidPassword) {
					t.Errorf("NewReader error = %v, want it to be ErrInvalidPassword (proves V=%d was accepted)", err, tt.v)
				}
				return
			}
			wantSub := fmt.Sprintf("unsupported PDF: encryption version V=%d", tt.v)
			if !strings.Contains(err.Error(), wantSub) {
				t.Errorf("NewReader error = %q, want it to contain %q", err, wantSub)
			}
		})
	}
}

// TestInitEncryptOAndULength covers the `len(O) != 32 || len(U) != 32`
// check: too-short, too-long, and absent each hit the "missing O= or U="
// error, while exactly 32 bytes for both gets past it to the password
// check.
func TestInitEncryptOAndULength(t *testing.T) {
	t.Parallel()

	ok32 := strings.Repeat("X", 32)
	tests := []struct {
		name       string
		oClause    string
		uClause    string
		wantAccept bool
	}{
		{"O 31 bytes too short", fmt.Sprintf("/O (%s)", strings.Repeat("X", 31)), fmt.Sprintf("/U (%s)", ok32), false},
		{"U 33 bytes too long", fmt.Sprintf("/O (%s)", ok32), fmt.Sprintf("/U (%s)", strings.Repeat("X", 33)), false},
		{"O absent", "", fmt.Sprintf("/U (%s)", ok32), false},
		{"U absent", fmt.Sprintf("/O (%s)", ok32), "", false},
		{"both exactly 32 bytes", fmt.Sprintf("/O (%s)", ok32), fmt.Sprintf("/U (%s)", ok32), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encrypt := fmt.Sprintf("<< /Filter /Standard /V 2 /R 3 %s %s >>", tt.oClause, tt.uClause)
			data := encryptedPDF(encrypt, false)
			_, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
			if err == nil {
				t.Fatal("NewReader on an encrypted PDF with no usable key material: got nil error, want one")
			}
			if tt.wantAccept {
				if !errors.Is(err, pdf.ErrInvalidPassword) {
					t.Errorf("NewReader error = %v, want it to be ErrInvalidPassword (proves the O/U length check passed)", err)
				}
				return
			}
			wantSub := "missing O= or U="
			if !strings.Contains(err.Error(), wantSub) {
				t.Errorf("NewReader error = %q, want it to contain %q", err, wantSub)
			}
		})
	}
}

// wsEncryptComputeU implements PDF 32000-1:2008 §7.6.3.3 Algorithm 2
// (computing an encryption key) followed by §7.6.3.4 Algorithm 4 (R==2) or
// the key-derivation portion of Algorithm 5 (R>=3, first 16 bytes only --
// the remaining 16 bytes of a real /U entry are unspecified padding that
// initEncrypt never inspects, since its check is bytes.HasPrefix) for a
// document opened with an empty user password. It is written directly
// against the spec text rather than by copying read.go's expression order,
// so that a fixture built from it exercises initEncrypt's own derivation
// instead of merely reproducing the same code twice.
func wsEncryptComputeU(r int, n int64, o string, p uint32, id []byte) string {
	// Algorithm 2, step (a): an empty password padded to 32 bytes is just
	// the padding string itself.
	padded := make([]byte, 32)
	copy(padded, wsEncryptPasswordPad)

	h := md5.New()     //nolint:gosec // spec-mandated hash for this algorithm
	h.Write(padded)    // step (b)
	h.Write([]byte(o)) // step (c)
	h.Write([]byte{    // step (d): P, low-order byte first (intentional truncation, matches read.go)
		byte(p), byte(p >> 8), byte(p >> 16), byte(p >> 24), //nolint:gosec
	})
	h.Write(id) // step (e)
	key := h.Sum(nil)

	if r >= 3 {
		// Step (h): 50 additional rounds hashing the first n/8 bytes.
		for range 50 {
			h.Reset()
			h.Write(key[:n/8])
			key = h.Sum(nil)
		}
		key = key[:n/8] // step (i)
	} else {
		key = key[:40/8]
	}

	c, err := rc4.NewCipher(key) //nolint:gosec // spec-mandated cipher for this algorithm
	if err != nil {
		panic(err)
	}

	if r == 2 {
		// Algorithm 4: RC4-encrypt the padding string directly with the
		// document's encryption key; the result is the full 32-byte U.
		u := make([]byte, 32)
		copy(u, wsEncryptPasswordPad)
		c.XORKeyStream(u, u)
		return string(u)
	}

	// Algorithm 5 steps (a)-(d): MD5 the padding string and the ID, RC4
	// encrypt with the key, then 19 further RC4 passes with the key XORed
	// byte-by-byte against the round number.
	h2 := md5.New() //nolint:gosec // spec-mandated hash for this algorithm
	h2.Write(wsEncryptPasswordPad)
	h2.Write(id)
	u := h2.Sum(nil)
	c.XORKeyStream(u, u)
	for i := 1; i <= 19; i++ {
		key1 := make([]byte, len(key))
		copy(key1, key)
		for j := range key1 {
			key1[j] ^= byte(i)
		}
		ci, err := rc4.NewCipher(key1) //nolint:gosec // spec-mandated cipher for this algorithm
		if err != nil {
			panic(err)
		}
		ci.XORKeyStream(u, u)
	}
	// Step (e): pad to 32 bytes; the trailing 16 are arbitrary and never
	// checked by initEncrypt (bytes.HasPrefix against the 16-byte u above).
	full := make([]byte, 32)
	copy(full, u)
	return string(full)
}

// TestInitEncryptAcceptPathEmptyPassword builds real, working encrypted PDF
// fixtures (via wsEncryptComputeU) for R==2 and R==3 with an empty user
// password, and confirms NewReader succeeds and returns a Reader that can
// actually be used. This is the accept path for the 50-round key-
// strengthening loop and the R==2/R>=3 u-computation split: a mutant that
// breaks either one flips these from success to ErrInvalidPassword.
func TestInitEncryptAcceptPathEmptyPassword(t *testing.T) {
	t.Parallel()

	o := string(bytes.Repeat([]byte{0x11}, 32))
	var p int32 = -44 // arbitrary permissions bitmask; only ever hashed, never checked

	tests := []struct {
		name   string
		r      int
		v      int
		length int64
	}{
		{"R=2, default 40-bit key", 2, 1, 40},
		{"R=3, 128-bit key", 3, 2, 128},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			u := wsEncryptComputeU(tt.r, tt.length, o, uint32(p), wsEncryptID) //nolint:gosec // intentional two's-complement reinterpretation, matches read.go's P conversion

			lengthClause := ""
			if tt.length != 40 {
				lengthClause = fmt.Sprintf("/Length %d ", tt.length)
			}
			encrypt := fmt.Sprintf("<< /Filter /Standard /V %d %s/R %d /O <%s> /U <%s> /P %d >>",
				tt.v, lengthClause, tt.r, hex.EncodeToString([]byte(o)), hex.EncodeToString([]byte(u)), p)
			data := encryptedPDF(encrypt, false)

			r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatalf("NewReader with a correctly-derived empty-password /U: %v", err)
			}
			if r == nil {
				t.Fatal("NewReader returned a nil Reader with a nil error")
			}
			if got := r.Trailer().Key("Root").Key("Type").Name(); got != "Catalog" {
				t.Errorf("Trailer Root /Type = %q, want %q", got, "Catalog")
			}
			if got := r.NumPage(); got != 1 {
				t.Errorf("NumPage() = %d, want 1", got)
			}
		})
	}
}

// Item 7 (R==4 with /EncryptMetadata false vs. R==3, proving the R>=4
// boundary on the Algorithm 2 step (f) 0xFFFFFFFF hash) is not covered
// here. wsEncryptComputeU's signature, as specified for this workstream,
// takes no EncryptMetadata parameter, and read.go's only R=4 accept-path
// fixture available for reuse (aesEncryptMetadataFalsePDFBase64 in
// read_test.go) has no R=3 counterpart to diff it against -- it is AES
// (V=4/AESV2) rather than RC4, so it isn't a drop-in R value swap either.
// Building a from-scratch, correct V=4/AESV2/CF accept-path fixture (with
// its own AES-encrypted content stream) to pair with it was judged out of
// proportion to this ticket's remaining scope; see the PR description.
