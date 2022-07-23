// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package protoscan is a protocol scanner.
package protoscan

import (
	"bytes"
	"errors"
	"io"
	"sync"
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
	reader    io.Reader // The reader provided by the client.
	split     SplitFunc // The function to split the tokens.
	buffer    []byte    // Buffer used as argument to Split.
	maxBuffer int       // The maximum size used to buffer a token. The actual maximum token size may be smaller as the buffer may need to include, for instance, a newline.
	token     []byte    // Last token generated by a call to Scan. The underlying array may point to data that will be overwritten by a subsequent call to Scan. It does no allocation.
	err       error     // Sticky error.
	start     int       // Number of bytes from the beginning of the buffer by which the carriage is shifted.
	end       int       // Number of bytes that been read from the reader and then buffered.
	empties   int       // Count of successive empty tokens.
}

// SplitFunc is the signature of the split function used to tokenize the
// input. The arguments are an initial slice of the remaining unprocessed data
// and a flag, atEOF.
// The return values are the hint number of bytes to read more from reader
// and the number of bytes to advance the input plus an error, if any.
//
// Scanning stops if the function returns an error, in which case some of
// the input may be discarded.
//
// Otherwise, the Protoscan advances the input. If any token present,
// the Protoscan holds its in Token field.
// If the split function hints more data to read, the Protoscan reads more data
// and continues scanning; if there is no more data --if atEOF was true--
// the Protoscan returns. If the data does not yet hold a complete token,
// a SplitFunc can return hint as number of bytes which must read (N, 0)
// to signal the Protoscan to read more data from the reader into the slice
// and try again with a longer slice starting at the same point in the input.
type SplitFunc func(data []byte, atEOF bool) (hint int, advance int, token []byte, err error)

func New(r io.Reader, opts ...Option) *Protoscan {
	buf := *pool.Get().(*[]byte)
	buf = buf[:0]
	s := &Protoscan{
		reader: r,
		split:  ScanBytes,
		buffer: buf,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Option changes scanner.
type Option func(*Protoscan)

// WithSplit sets the function to split the tokens.
func WithSplit(split SplitFunc) Option {
	return func(s *Protoscan) { s.split = split }
}

// WithBuffer sets Buffer used as argument to Split.
func WithBuffer(buf []byte) Option {
	return func(s *Protoscan) { s.buffer = buf }
}

// WithMaxBuffer sets maximum size used to buffer a token.
func WithMaxBuffer(max int) Option {
	return func(s *Protoscan) { s.maxBuffer = max }
}

// Errors returned by Protoscan.
var (
	ErrTooLong         = errors.New("protoscan: token too long")
	ErrNegativeAdvance = errors.New("protoscan: SplitFunc returns negative advance count of the data input")
	ErrAdvanceTooFar   = errors.New("protoscan: SplitFunc returns advance count beyond input")
	ErrBadReadCount    = errors.New("protoscan: Read returned impossible count")
	ErrNegativeHint    = errors.New("protoscan: SplitFunc hinted negative size of the token")
	ErrNoProgress      = errors.New("protoscan: too many scans without progressing")
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

// Token returns the last token generated by a call to Scan.
func (s *Protoscan) Token() []byte {
	return s.token
}

// Err returns the first non-EOF error that was encountered by the Protoscan.
func (s *Protoscan) Err() error {
	if s.err == io.EOF || s.err == FinalToken {
		return nil
	}
	return s.err
}

// maxConsecutiveIdling is the number of allowed consecutive empty reads
// or consecutive empty scans without progressing.
const maxConsecutiveIdling = 1000

// Scan advances the Protoscan to the next token, which will then be
// available through the Token filed.
//
// The underlying token slice may point to data that
// will be overwritten by a subsequent call to Scan. It does no allocation.
//
// It return false when the scan stops,
// either by reaching the end of the input or an error.
// After Scan returns false, the Err method will return any error that
// occurred during scanning, except that if it was io.EOF, Err
// will return nil.
func (s *Protoscan) Scan() bool {
	if s.maxBuffer == 0 {
		s.maxBuffer = maxBuffer
	}
	if s.err == FinalToken {
		return false
	}
	// Loop until we have a token.
	for {
		hint, advance, token, err := s.split(s.buffer[s.start:s.end], s.err == io.EOF)
		s.token = token
		if err != nil {
			s.setErr(err)
			pool.Put(&s.buffer)
			return err == FinalToken
		}
		if err = s.advance(advance); err != nil {
			s.setErr(err)
			return false
		}
		s.start += advance
		if token != nil && advance > 0 {
			s.empties = 0
			return true
		} else if advance > 0 {
			s.empties = 0
		} else {
			s.empties++
			if s.empties > maxConsecutiveIdling {
				s.setErr(ErrNoProgress)
				return false
			}
		}
		if s.err != nil {
			return false
		}
		// Shift data to beginning of buffer if there's lots of empty space
		// or space is needed.
		if s.start > 0 && (s.end == len(s.buffer) || s.start > len(s.buffer)/2) {
			copy(s.buffer, s.buffer[s.start:s.end])
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
		if len(s.buffer) < claim {
			pool.Put(&s.buffer)
			s.buffer = append(s.buffer, make([]byte, claim-len(s.buffer))...)
		}
		// Finally we can read some input. Make sure we don't get stuck with
		// a misbehaving Reader. Officially we don't need to do this, but let's
		// be extra careful: Protoscan is for safe, simple jobs.
		for s.end < claim {
			n, err := s.reader.Read(s.buffer[s.end:claim])
			if n < 0 || len(s.buffer)-s.end < n {
				s.setErr(ErrBadReadCount)
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

var pool = sync.Pool{New: func() interface{} { return &[]byte{} }}

// advance validates moving of the carriage forward on n bytes of the buffer.
// It reports whether the advance was legal.
func (s *Protoscan) advance(n int) error {
	if n < 0 {
		return ErrNegativeAdvance
	}
	if s.start+n > s.end {
		return ErrAdvanceTooFar
	}
	return nil
}

// hint validates hint.
func (s *Protoscan) hint(n int) error {
	if n < 0 {
		return ErrNegativeHint
	}
	// Guarantee no buffer overflow.
	const maxInt = int(^uint(0) >> 1)
	if s.end+n > s.maxBuffer || s.end+n > maxInt {
		return ErrTooLong
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

// ScanBytes is a split function for a Protoscan that returns each byte as a token.
func ScanBytes(data []byte, atEOF bool) (int, int, []byte, error) {
	if len(data) > 0 {
		return 0, 1, data[:1], nil
	}
	if atEOF {
		return 0, 0, nil, nil
	}
	return 1, 0, nil, nil
}

var errorRune = []byte(string(utf8.RuneError))

// ScanRunes is a split function for a Protoscan that returns each
// UTF-8-encoded rune as a token.
//
// The sequence of runes returned is equivalent to that from a range loop
// over the input as a string, which means that erroneous UTF-8 encodings
// translate to U+FFFD = "\xef\xbf\xbd". Because of the Scan interface,
// this makes it impossible for the client to distinguish correctly encoded
// replacement runes from encoding errors.
func ScanRunes(data []byte, atEOF bool) (int, int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, 0, nil, nil
	}

	if len(data) == 0 {
		return 1, 0, nil, nil
	}

	// Fast path 1: ASCII.
	if data[0] < utf8.RuneSelf {
		return 0, 1, data[:1], nil
	}

	// Fast path 2: Correct UTF-8 decode without error.
	_, width := utf8.DecodeRune(data)
	if width > 1 {
		// It's a valid encoding. Width cannot be one for a correctly encoded
		// non-ASCII rune.
		return 0, width, data[0:width], nil
	}

	// We know it's an error: we have width==1 and implicitly r==utf8.RuneError.
	// Is the error because there wasn't a full rune to be decoded?
	// FullRune distinguishes correctly between erroneous and incomplete encodings.
	if !atEOF && !utf8.FullRune(data) {
		// Incomplete; get more bytes.
		return 1, 0, nil, nil
	}

	// We have a real UTF-8 encoding error. Return a properly encoded error rune
	// but advance only one byte. This matches the behavior of a range loop over
	// an incorrectly encoded string.
	return 0, 1, []byte(errorRune), nil
}

// ScanLines is a split function for a Protoscan that returns each line of
// text, stripped of any trailing end-of-line marker.
// The returned line may be empty. The end-of-line marker is one optional
// carriage return followed by one mandatory newline. In regular expression
// notation, it is `\r?\n`. The last non-empty line of input will be returned
// even if it has no newline.
func ScanLines(data []byte, atEOF bool) (int, int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, 0, nil, nil
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		// We have a full newline-terminated line.
		return 0, i + 1, dropCR(data[0:i]), nil
	}
	// If we're at EOF, we have a final, non-terminated line. Return it.
	if atEOF {
		return 0, len(data), dropCR(data), nil
	}
	// Request more data.
	return 1, 0, nil, nil
}

// dropCR drops a terminal \r from the data.
func dropCR(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\r' {
		return data[0 : len(data)-1]
	}
	return data
}

// ScanWords is a split function for a Protoscan that returns each
// space-separated word of text, with surrounding spaces deleted.
// It will never return an empty string. The definition of space is set by
// unicode.IsSpace.
func ScanWords(data []byte, atEOF bool) (int, int, []byte, error) {
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
			return 0, i + width, data[start:i], nil
		}
	}
	// If we're at EOF, we have a final, non-empty, non-terminated word. Return it.
	if atEOF && len(data) > start {
		return 0, len(data), data[start:], nil
	}
	// Request more data.
	return 1, 0, nil, nil
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
