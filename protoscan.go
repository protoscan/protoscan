// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package protoscan is a protocol scanner.
package protoscan

import (
	"bytes"
	"errors"
	"io"
	"unicode/utf8"
)

// Protoscan provides a convenient interface for reading data such as a ISO 8583
// messages or FIX messages. Successive calls to the Scan method will step
// through a 'tokens' typically read from a TCP connection. The specification
// of a token is defined by a split function of type SplitFunc. For example
// token may contains FIX message or size of the ISO 8583 message and message
// itself or only the ISO 8583 message. The client may instead provide a custom
// split function.
//
// Scanning stops unrecoverably at EOF, the first I/O error, or a token too
// large to fit in the buffer of token which exceeds maximum size allowed by
// the MaxBuffer. When a scan stops, the reader may have advanced arbitrarily
// far past the last token, for example in case of the invalid or inconsistent
// or incomplete messages which may resides in head or tail of the data stream.
//
type Protoscan struct {
	Reader    io.Reader // The reader provided by the client.
	Split     SplitFunc // The function to split the tokens.
	Buffer    []byte    // Buffer used as argument to Split.
	MaxBuffer int       // The maximum size used to buffer a token. The actual maximum token size may be smaller as the buffer may need to include, for instance, a newline.
	Tokens    []byte    // Last tokens copied by Split.
	Indexes   []int     // Last tokens indexes.
	Gaps      []byte    // Last skipped bytes copied by Split. Gaps intend to holds buffered and unprocessed bytes for example if EOF occurs.
	err       error     // Sticky error.
	start     int       // Number of bytes from the beginning of the buffer by which the carriage is shifted.
	end       int       // Number of bytes that been read from the reader and then buffered.
	empties   int       // Count of successive empty tokens.
}

// SplitFunc is the signature of the split function used to tokenize the
// input. The arguments are an initial slice of the remaining unprocessed data
// and a destination slice of tokens and destination slice of tokens indexes
// and a destination slice of gaps bytes and a flag, atEOF.
// The return values are the hint number of bytes to read more from reader
// and the number of bytes to advance the input plus an error, if any.
//
// Scanning stops if the function returns an error, in which case some of
// the input may be discarded.
//
// Otherwise, the Protoscan advances the input. If any token present,
// the Protoscan holds its in Tokens field and returns.
// If the split function hints more data to read, the Protoscan reads more data
// and continues scanning; if there is no more data --if atEOF was true--
// the Protoscan returns. If the data does not yet hold a complete token,
// a SplitFunc can return hint as number of bytes which must read (N, 0)
// to signal the Protoscan to read more data from the reader into the slice
// and try again with a longer slice starting at the same point in the input.
//
// The Gaps field intend to holds buffered and unprocessed bytes
// that bytes copied to Gaps field by a SplitFunc for example if EOF occurs.
type SplitFunc func(data []byte, tokens *[]byte, indexes *[]int, gaps *[]byte, atEOF bool) (hint int, advance int, err error)

// Errors returned by Protoscan.
var (
	TooLong         = errors.New("protoscan: token too long")
	NegativeAdvance = errors.New("protoscan: SplitFunc returns negative advance count of the data input")
	AdvanceTooFar   = errors.New("protoscan: SplitFunc returns advance count beyond input")
	BadReadCount    = errors.New("protoscan: Read returned impossible count")
	NegativeHint    = errors.New("protoscan: SplitFunc hinted negative size of the token")
	NoProgress      = errors.New("protoscan: too many scans without progressing")
)

// FinalToken is a special sentinel error value. It is intended to be
// returned by a Split function to indicate that the token being delivered
// with the error is the last token and scanning should stop after this one.
// After FinalToken is received by Scan, scanning stops with no error.
// The value is useful to stop processing early or when it is necessary to
// deliver a final empty token. One could achieve the same behavior
// with a custom error value but providing one here is tidier.
// See the emptyFinalToken example for a use of this value.
var FinalToken = errors.New("final token")

// maxBuffer is the default maximum size for the buffer.
const maxBuffer = 64 * 1024

// Err returns the first non-EOF error that was encountered by the Protoscan.
func (s Protoscan) Err() error {
	if s.err == io.EOF || s.err == FinalToken {
		return nil
	}
	return s.err
}

// maxConsecutiveIdling is the number of allowed consecutive empty reads
// or consecutive empty scans without progressing.
const maxConsecutiveIdling = 1000

// Scan advances the Protoscan to the next tokens, which will then be
// available through the Tokens filed which indexed by Indexes field.
// Also Gaps field may contains last skipped and unprocessed bytes
// that's buffered for example if EOF occurs.
//
// The underlying slice of tokens or slice of gaps may point to data that
// will be overwritten by a subsequent call to Scan. It does no allocation.
//
// It return false when the scan stops,
// either by reaching the end of the input or an error.
// After Scan returns false, the Err method will return any error that
// occurred during scanning, except that if it was io.EOF, Err
// will return nil.
func (s *Protoscan) Scan() bool {
	if s.MaxBuffer == 0 {
		s.MaxBuffer = maxBuffer
	}
	if s.err == FinalToken {
		return false
	}
	// Loop until we have a tokens.
	for {
		s.Tokens = s.Tokens[:0]
		s.Indexes = s.Indexes[:0]
		s.Gaps = s.Gaps[:0]
		hint, advance, err := s.Split(s.Buffer[s.start:s.end], &s.Tokens, &s.Indexes, &s.Gaps, s.err == io.EOF)
		if err != nil {
			s.setErr(err)
			return err == FinalToken
		}
		if s.err != nil {
			return false
		}
		err = s.advance(advance)
		if err != nil {
			s.setErr(err)
			return false
		}
		s.start += advance
		if len(s.Indexes) != 0 && advance > 0 {
			s.empties = 0
			return true
		} else if advance > 0 {
			s.empties = 0
		} else {
			s.empties++
			if s.empties > maxConsecutiveIdling {
				s.setErr(NoProgress)
				return false
			}
		}
		// Shift data to beginning of buffer if there's lots of empty space
		// or space is needed.
		if s.start > 0 && (s.end == len(s.Buffer) || s.start > len(s.Buffer)/2) {
			copy(s.Buffer, s.Buffer[s.start:s.end])
			s.end -= s.start
			s.start = 0
		}
		err = s.hint(hint)
		if err != nil {
			s.setErr(err)
			return false
		}
		claim := s.end + hint
		// Is the buffer cannot holds the token of the hinted size? If so, resize.
		if len(s.Buffer) < claim {
			s.Buffer = append(s.Buffer, make([]byte, claim-len(s.Buffer))...)
		}
		// Finally we can read some input. Make sure we don't get stuck with
		// a misbehaving Reader. Officially we don't need to do this, but let's
		// be extra careful: Protoscan is for safe, simple jobs.
		for s.end < claim {
			n, err := s.Reader.Read(s.Buffer[s.end:claim])
			if n < 0 || len(s.Buffer)-s.end < n {
				s.setErr(BadReadCount)
				break
			}
			s.end += n
			if err != nil {
				s.setErr(err)
				break
			}
			if n > 0 {
				s.empties = 0
				break
			}
			s.empties++
			if s.empties > maxConsecutiveIdling {
				s.setErr(io.ErrNoProgress)
				break
			}
		}
	}
}

// advance validates moving of the carriage forward on n bytes of the buffer.
// It reports whether the advance was legal.
func (s Protoscan) advance(n int) error {
	if n < 0 {
		return NegativeAdvance
	}
	if s.start+n > s.end {
		return AdvanceTooFar
	}
	return nil
}

// hint validates hint.
func (s Protoscan) hint(n int) error {
	if n < 0 {
		return NegativeHint
	}
	// Guarantee no buffer overflow.
	const maxInt = int(^uint(0) >> 1)
	if s.end+n > s.MaxBuffer || s.end+n > maxInt {
		return TooLong
	}
	return nil
}

// setErr records the first error encountered.
func (s *Protoscan) setErr(err error) {
	if s.err == nil || s.err == io.EOF {
		s.err = err
	}
}

// Split functions:

// ScanBytes is a split function for a Protoscan that copies each byte as a token.
func ScanBytes(data []byte, tokens *[]byte, indexes *[]int, _ *[]byte, atEOF bool) (int, int, error) {
	if len(data) > 0 {
		*tokens = append(*tokens, data[0])
		*indexes = append(*indexes, 1)
		return 0, 1, nil
	}
	if atEOF {
		return 0, 0, nil
	}
	return 1, 0, nil
}

var errorRune = []byte(string(utf8.RuneError))

// ScanRunes is a split function for a Protoscan that copies each
// UTF-8-encoded rune as a token. The sequence of runes copied is
// equivalent to that from a range loop over the input as a string, which
// means that erroneous UTF-8 encodings translate to U+FFFD = "\xef\xbf\xbd".
// Because of the Scan interface, this makes it impossible for the client to
// distinguish correctly encoded replacement runes from encoding errors.
func ScanRunes(data []byte, tokens *[]byte, indexes *[]int, _ *[]byte, atEOF bool) (int, int, error) {
	if atEOF && len(data) == 0 {
		return 0, 0, nil
	}

	if len(data) == 0 {
		return 1, 0, nil
	}

	// Fast path 1: ASCII.
	if data[0] < utf8.RuneSelf {
		*tokens = append(*tokens, data[0])
		*indexes = append(*indexes, 1)
		return 0, 1, nil
	}

	// Fast path 2: Correct UTF-8 decode without error.
	_, width := utf8.DecodeRune(data)
	if width > 1 {
		// It's a valid encoding. Width cannot be one for a correctly encoded
		// non-ASCII rune.
		*tokens = append(*tokens, data[0:width]...)
		*indexes = append(*indexes, width)
		return 0, width, nil
	}

	// We know it's an error: we have width==1 and implicitly r==utf8.RuneError.
	// Is the error because there wasn't a full rune to be decoded?
	// FullRune distinguishes correctly between erroneous and incomplete encodings.
	if !atEOF && !utf8.FullRune(data) {
		// Incomplete; get more bytes.
		return 1, 0, nil
	}

	// We have a real UTF-8 encoding error. Return a properly encoded error rune
	// but advance only one byte. This matches the behavior of a range loop over
	// an incorrectly encoded string.
	*tokens = append(*tokens, errorRune...)
	*indexes = append(*indexes, len(errorRune))
	return 0, 1, nil
}

// ScanLines is a split function for a Protoscan that copies each line of
// text, stripped of any trailing end-of-line marker. The copied line may
// be empty. The end-of-line marker is one optional carriage return followed
// by one mandatory newline. In regular expression notation, it is `\r?\n`.
// The last non-empty line of input will be copied even if it has no
// newline.
func ScanLines(data []byte, tokens *[]byte, indexes *[]int, _ *[]byte, atEOF bool) (int, int, error) {
	if atEOF && len(data) == 0 {
		return 0, 0, nil
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		// We have a full newline-terminated line.
		*tokens = append(*tokens, dropCR(data[0:i])...)
		*indexes = append(*indexes, len(dropCR(data[0:i])))
		return 0, i + 1, nil
	}
	// If we're at EOF, we have a final, non-terminated line. Return it.
	*tokens = append(*tokens, dropCR(data)...)
	*indexes = append(*indexes, len(dropCR(data)))
	if atEOF {
		return 0, len(data), nil
	}
	// Request more data.
	return 1, 0, nil
}

// dropCR drops a terminal \r from the data.
func dropCR(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\r' {
		return data[0 : len(data)-1]
	}
	return data
}

// ScanWords is a split function for a Protoscan that copies each
// space-separated word of text, with surrounding spaces deleted. It will
// never copy an empty string. The definition of space is set by
// unicode.IsSpace. If at EOF, and have non-empty, non-terminated word and it
// holded in the gaps.
func ScanWords(data []byte, tokens *[]byte, indexes *[]int, gaps *[]byte, atEOF bool) (int, int, error) {
	// Skip leading spaces.
	start := 0
	for width := 0; start < len(data); start += width {
		var r rune
		r, width = utf8.DecodeRune(data[start:])
		if !isSpace(r) {
			break
		}
	}
	// Scan until space, marking end of word.
	for width, i := 0, start; i < len(data); i += width {
		var r rune
		r, width = utf8.DecodeRune(data[i:])
		if isSpace(r) {
			*tokens = append(*tokens, data[start:i]...)
			*indexes = append(*indexes, len(data[start:i]))
			return 0, i + width, nil
		}
	}
	// If we're at EOF, we have a final, non-empty, non-terminated word. Copy it.
	if atEOF && len(data) > start {
		*tokens = append(*tokens, data[start:]...)
		*indexes = append(*indexes, len(data[start:]))
		return 0, len(data), nil
	}
	// Request more data.
	return 1, 0, nil
}

// isSpace reports whether the character is a Unicode white space character.
// We avoid dependency on the unicode package, but check validity of the implementation
// in the tests.
func isSpace(r rune) bool {
	if r <= '\u00FF' {
		// Obvious ASCII ones: \t through \r plus space. Plus two Latin-1 oddballs.
		switch r {
		case ' ', '\t', '\n', '\v', '\f', '\r':
			return true
		case '\u0085', '\u00A0':
			return true
		}
		return false
	}
	// High-valued ones.
	if '\u2000' <= r && r <= '\u200a' {
		return true
	}
	switch r {
	case '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000':
		return true
	}
	return false
}
