// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package protoscan_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/protoscan/protoscan"
)

const smallMaxTokenSize = 256 // Much smaller for more efficient testing.

// Test white space table matches the Unicode definition.
func TestSpace(t *testing.T) {
	for r := rune(0); r <= utf8.MaxRune; r++ {
		if protoscan.IsSpace(r) != unicode.IsSpace(r) {
			t.Fatalf("white space property disagrees: %#U should be %t", r, unicode.IsSpace(r))
		}
	}
}

var scanTests = []string{
	"",
	"a",
	"¼",
	"☹",
	"\x81",   // UTF-8 error
	"\uFFFD", // correctly encoded RuneError
	"abcdefgh",
	"abc def\n\t\tgh    ",
	"abc¼☹\x81\uFFFD日本語\x82abc",
}

func TestScanByte(t *testing.T) {
	for n, test := range scanTests {
		buf := strings.NewReader(test)
		s := protoscan.Protoscan{
			Reader: buf,
			Split:  protoscan.ScanBytes,
		}
		var i int
		for i = 0; s.Scan(); i++ {
			if s.Tokens()[0] != test[i] {
				t.Errorf("#%d: %d: expected %q got %q", n, i, test, s.Tokens()[0])
			}
		}
		if i != len(test) {
			t.Errorf("#%d: termination expected at %d; got %d", n, len(test), i)
		}
		err := s.Err()
		if err != nil {
			t.Errorf("#%d: %v", n, err)
		}
	}
}

// Test that the rune splitter returns same sequence of runes (not bytes) as for range string.
func TestScanRune(t *testing.T) {
	for n, test := range scanTests {
		buf := strings.NewReader(test)
		s := protoscan.Protoscan{
			Reader: buf,
			Split:  protoscan.ScanRunes,
		}
		var i, runeCount int
		var expect rune
		// Use a string range loop to validate the sequence of runes.
		for i, expect = range string(test) {
			if !s.Scan() {
				break
			}
			runeCount++
			got, _ := utf8.DecodeRune(s.Tokens())
			if got != expect {
				t.Errorf("#%d: %d: expected %q got %q", n, i, expect, got)
			}
		}
		if s.Scan() {
			t.Errorf("#%d: scan ran too long, got %q", n, s.Tokens())
		}
		testRuneCount := utf8.RuneCountInString(test)
		if runeCount != testRuneCount {
			t.Errorf("#%d: termination expected at %d; got %d", n, testRuneCount, runeCount)
		}
		err := s.Err()
		if err != nil {
			t.Errorf("#%d: %v", n, err)
		}
	}
}

func BenchmarkScanRune(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		test := scanTests[len(scanTests)-1]
		buf := strings.NewReader(test)
		s := protoscan.Protoscan{
			Reader: buf,
			Split:  protoscan.ScanRunes,
		}
		for s.Scan() {
		}
	}
}

var wordScanTests = []string{
	"",
	" ",
	"\n",
	"a",
	" a ",
	"abc def",
	" abc def ",
	" abc\tdef\nghi\rjkl\fmno\vpqr\u0085stu\u00a0\n",
}

// Test that the word splitter returns the same data as strings.Fields.
func TestScanWords(t *testing.T) {
	for n, test := range wordScanTests {
		buf := strings.NewReader(test)
		s := protoscan.Protoscan{
			Reader: buf,
			Split:  protoscan.ScanWords,
		}
		words := strings.Fields(test)
		var wordCount int
		for wordCount = 0; wordCount < len(words); wordCount++ {
			if !s.Scan() {
				break
			}
			got := string(s.Tokens())
			if got != words[wordCount] {
				t.Errorf("#%d: %d: expected %q got %q", n, wordCount, words[wordCount], got)
			}
		}
		if s.Scan() {
			t.Errorf("#%d: scan ran too long, got %q", n, s.Tokens())
		}
		if len(words)-wordCount > 1 || wordCount-len(words) > 0 {
			t.Errorf("#%d: termination expected at %d; got %d", n, len(words), wordCount)
		} else if wordCount-len(words) == 1 && string(s.Gaps()) != words[len(words)-1] {
			t.Errorf("#%d: unexpected last word, expected %q; got %q", n, words[len(words)-1], s.Gaps())
		}
		err := s.Err()
		if err != nil {
			t.Errorf("#%d: %v", n, err)
		}
	}
}

// slowReader is a reader that returns only a few bytes at a time, to test the incremental
// reads in Scanner.Scan.
type slowReader struct {
	max int
	buf io.Reader
}

func (sr *slowReader) Read(s []byte) (n int, err error) {
	if len(s) > sr.max {
		s = s[0:sr.max]
	}
	return sr.buf.Read(s)
}

// genLine writes to buf a predictable but non-trivial line of text of length
// n, including the terminal newline and an occasional carriage return.
// If addNewline is false, the \r and \n are not emitted.
func genLine(buf *bytes.Buffer, lineNum, n int, addNewline bool) {
	buf.Reset()
	doCR := lineNum%5 == 0
	if doCR {
		n--
	}
	for i := 0; i < n-1; i++ { // Stop early for \n.
		c := 'a' + byte(lineNum+i)
		if c == '\n' || c == '\r' { // Don't confuse us.
			c = 'N'
		}
		buf.WriteByte(c)
	}
	if addNewline {
		if doCR {
			buf.WriteByte('\r')
		}
		buf.WriteByte('\n')
	}
}

// Test the line splitter, including some carriage returns but no long lines.
func TestScanLongLines(t *testing.T) {
	// Build a buffer of lots of line lengths up to but not exceeding smallMaxTokenSize.
	tmp := new(bytes.Buffer)
	buf := new(bytes.Buffer)
	lineNum := 0
	j := 0
	for i := 0; i < 2*smallMaxTokenSize; i++ {
		genLine(tmp, lineNum, j, true)
		if j < smallMaxTokenSize {
			j++
		} else {
			j--
		}
		buf.Write(tmp.Bytes())
		lineNum++
	}
	s := protoscan.Protoscan{
		Reader:    &slowReader{1, buf},
		Split:     protoscan.ScanLines,
		MaxBuffer: smallMaxTokenSize,
	}
	j = 0
	for lineNum := 0; s.Scan(); lineNum++ {
		genLine(tmp, lineNum, j, false)
		if j < smallMaxTokenSize {
			j++
		} else {
			j--
		}
		line := tmp.String() // We use the string-valued token here, for variety.
		if string(s.Tokens()) != line {
			t.Errorf("%d: bad line: %d %d\n%.100q\n%.100q\n", lineNum, len(s.Tokens()), len(line), s.Tokens(), line)
		}
	}
	err := s.Err()
	if err != nil {
		t.Fatal(err)
	}
}

// Test that the line splitter errors out on a long line.
func TestScanLineTooLong(t *testing.T) {
	const smallMaxTokenSize = 256 // Much smaller for more efficient testing.
	// Build a buffer of lots of line lengths up to but not exceeding smallMaxTokenSize.
	tmp := new(bytes.Buffer)
	buf := new(bytes.Buffer)
	lineNum := 0
	j := 0
	for i := 0; i < 2*smallMaxTokenSize; i++ {
		genLine(tmp, lineNum, j, true)
		j++
		buf.Write(tmp.Bytes())
		lineNum++
	}
	s := protoscan.Protoscan{
		Reader:    &slowReader{3, buf},
		Split:     protoscan.ScanLines,
		MaxBuffer: smallMaxTokenSize,
	}
	j = 0
	for lineNum := 0; s.Scan(); lineNum++ {
		genLine(tmp, lineNum, j, false)
		if j < smallMaxTokenSize {
			j++
		} else {
			j--
		}
		line := tmp.Bytes()
		if !bytes.Equal(s.Tokens(), line) {
			t.Errorf("%d: bad line: %d %d\n%.100q\n%.100q\n", lineNum, len(s.Tokens()), len(line), s.Tokens(), line)
		}
	}
	err := s.Err()
	if err != protoscan.TooLong {
		t.Fatalf("expected ErrTooLong; got %s", err)
	}
}

// Test that the line splitter handles a final line without a newline.
func testNoNewline(text string, lines []string, t *testing.T) {
	buf := strings.NewReader(text)
	s := protoscan.Protoscan{
		Reader: &slowReader{7, buf},
		Split:  protoscan.ScanLines,
	}
	for lineNum := 0; s.Scan(); lineNum++ {
		line := lines[lineNum]
		if string(s.Tokens()) != line {
			t.Errorf("%d: bad line: %d %d\n%.100q\n%.100q\n", lineNum, len(s.Tokens()), len(line), s.Tokens(), line)
		}
	}
	err := s.Err()
	if err != nil {
		t.Fatal(err)
	}
}

// Test that the line splitter handles a final line without a newline.
func TestScanLineNoNewline(t *testing.T) {
	const text = "abcdefghijklmn\nopqrstuvwxyz"
	lines := []string{
		"abcdefghijklmn",
		"opqrstuvwxyz",
	}
	testNoNewline(text, lines, t)
}

// Test that the line splitter handles a final line with a carriage return but no newline.
func TestScanLineReturnButNoNewline(t *testing.T) {
	const text = "abcdefghijklmn\nopqrstuvwxyz\r"
	lines := []string{
		"abcdefghijklmn",
		"opqrstuvwxyz",
	}
	testNoNewline(text, lines, t)
}

// Test that the line splitter handles a final empty line.
func TestScanLineEmptyFinalLine(t *testing.T) {
	const text = "abcdefghijklmn\nopqrstuvwxyz\n\n"
	lines := []string{
		"abcdefghijklmn",
		"opqrstuvwxyz",
		"",
	}
	testNoNewline(text, lines, t)
}

// Test that the line splitter handles a final empty line with a carriage return but no newline.
func TestScanLineEmptyFinalLineWithCR(t *testing.T) {
	const text = "abcdefghijklmn\nopqrstuvwxyz\n\r"
	lines := []string{
		"abcdefghijklmn",
		"opqrstuvwxyz",
		"",
	}
	testNoNewline(text, lines, t)
}

var testError = errors.New("testError")

// Test the correct error is returned when the split function errors out.
func TestSplitError(t *testing.T) {
	// Create a split function that delivers a little data, then a predictable error.
	numSplits := 0
	const okCount = 7
	errorSplit := func(data []byte, atEOF bool) (int, int, []byte, []int, []byte, error) {
		if atEOF {
			panic("didn't get enough data")
		}
		if len(data) == 0 {
			return 1, 0, nil, nil, nil, nil
		}
		if numSplits >= okCount {
			return 0, 0, nil, nil, nil, testError
		}
		numSplits++
		return 0, 1, data[0:1], nil, nil, nil
	}
	// Read the data.
	const text = "abcdefghijklmnopqrstuvwxyz"
	buf := strings.NewReader(text)
	s := protoscan.Protoscan{
		Reader: &slowReader{1, buf},
		Split:  errorSplit,
	}
	var i int
	for i = 0; s.Scan(); i++ {
		if len(s.Tokens()) != 1 || text[i] != s.Tokens()[0] {
			t.Errorf("#%d: expected %q got %q", i, text[i], s.Tokens()[0])
		}
	}
	// Check correct termination location and error.
	if i != okCount {
		t.Errorf("unexpected termination; expected %d tokens got %d", okCount, i)
	}
	err := s.Err()
	if err != testError {
		t.Fatalf("expected %q got %v", testError, err)
	}
}

// Test that an EOF is overridden by a user-generated scan error.
func TestErrAtEOF(t *testing.T) {
	s := protoscan.Protoscan{Reader: strings.NewReader("1 2 33")}
	// This splitter will fail on last entry, after s.err==EOF.
	split := func(data []byte, atEOF bool) (int, int, []byte, []int, []byte, error) {
		hint, advance, tokens, _, _, err := protoscan.ScanWords(data, atEOF)
		if len(tokens) > 1 {
			if s.ErrOrEOF() != io.EOF {
				t.Fatal("not testing EOF")
			}
			err = testError
		}
		return hint, advance, tokens, nil, nil, err
	}
	s.Split = split
	for s.Scan() {
	}
	if s.Err() != testError {
		t.Fatal("wrong error:", s.Err())
	}
}

// Test for issue <https://github.com/golang/go/issues/5268>.
type alwaysError struct{}

func (alwaysError) Read(s []byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestNonEOFWithEmptyRead(t *testing.T) {
	s := protoscan.Protoscan{
		Reader: alwaysError{},
		Split:  protoscan.ScanLines,
	}
	for s.Scan() {
		t.Fatal("read should fail")
	}
	err := s.Err()
	if err != io.ErrUnexpectedEOF {
		t.Errorf("unexpected error: %v", err)
	}
}

// Test that Scan finishes if we have endless empty reads.
type endlessZeros struct{}

func (endlessZeros) Read(s []byte) (int, error) {
	return 0, nil
}

func TestBadReader(t *testing.T) {
	s := protoscan.Protoscan{
		Reader: endlessZeros{},
		Split:  protoscan.ScanLines,
	}
	for s.Scan() {
		t.Fatal("read should fail")
	}
	err := s.Err()
	if err != io.ErrNoProgress {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestScanWordsExcessiveWhiteSpace(t *testing.T) {
	const words = "lorem ipsum"
	s := strings.Repeat(" ", 4*smallMaxTokenSize) + words
	scan := protoscan.Protoscan{
		Reader:    strings.NewReader(s),
		Split:     protoscan.ScanWords,
		MaxBuffer: 4*smallMaxTokenSize + len(strings.Fields(words)[0]) + 1,
	}
	if !scan.Scan() {
		t.Fatalf("scan failed: %v", scan.Err())
	}
	if string(scan.Tokens()) != strings.Fields(words)[0] {
		t.Fatalf("unexpected token, expected: %q, received: %q", strings.Fields(words)[0], scan.Tokens())
	}
}

// Test that empty tokens, including at end of line or end of file, are found by the scanner.
// Issue 8672: Could miss final empty token.

func commaSplit(data []byte, atEOF bool) (int, int, []byte, []int, []byte, error) {
	for i := 0; i < len(data); i++ {
		if data[i] == ',' {
			return 0, i + 1, data[:i], nil, nil, nil
		}
	}
	if atEOF && len(data) > 0 {
		return 0, len(data), data, nil, nil, protoscan.FinalToken
	}
	if atEOF {
		return 0, 0, nil, nil, nil, nil
	}
	return 1, 0, nil, nil, nil, nil
}

func testEmptyTokens(t *testing.T, text string, values []string) {
	s := protoscan.Protoscan{
		Reader: strings.NewReader(text),
		Split:  commaSplit,
	}
	var i int
	for i = 0; s.Scan(); i++ {
		if i >= len(values) {
			t.Fatalf("got %d fields, expected %d", i+1, len(values))
		}
		if string(s.Tokens()) != values[i] {
			t.Errorf("%d: expected %q got %q", i, values[i], s.Tokens())
		}
	}
	if i != len(values) {
		t.Fatalf("got %d fields, expected %d", i, len(values))
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyTokens(t *testing.T) {
	testEmptyTokens(t, "1,2,3,", []string{"1", "2", "3"})
}

func TestWithNoEmptyTokens(t *testing.T) {
	testEmptyTokens(t, "1,2,3", []string{"1", "2", "3"})
}

func loopAtEOFSplit(data []byte, atEOF bool) (int, int, []byte, []int, []byte, error) {
	if len(data) > 0 {
		return 0, 1, data[:1], nil, nil, nil
	}
	return 0, 0, data, nil, nil, nil
}

func TestDontLoopForever(t *testing.T) {
	s := protoscan.Protoscan{
		Reader: strings.NewReader("abc"),
		Split:  loopAtEOFSplit,
	}
	for count := 0; s.Scan(); count++ {
		if count > 1000 {
			t.Fatal("looping")
		}
	}
	if s.Err() != protoscan.NoProgress {
		t.Fatal("after scan:", s.Err())
	}
}

func TestBlankLines(t *testing.T) {
	s := protoscan.Protoscan{
		Reader: strings.NewReader(strings.Repeat("\n", 1000)),
		Split:  protoscan.ScanLines,
	}
	for count := 0; s.Scan(); count++ {
		if count > 2000 {
			t.Fatal("looping")
		}
	}
	if s.Err() != nil {
		t.Fatal("after scan:", s.Err())
	}
}

type countdown int

func (c *countdown) split(data []byte, atEOF bool) (int, int, []byte, []int, []byte, error) {
	if len(data) == 0 {
		return 1, 0, nil, nil, nil, nil
	}
	if *c > 0 {
		*c--
		return 0, 1, data[:1], nil, nil, nil
	}
	return 0, 0, nil, nil, nil, nil
}

// Check that the looping-at-EOF check doesn't trigger for merely empty tokens.
func TestEmptyLinesOK(t *testing.T) {
	c := countdown(10000)
	s := protoscan.Protoscan{
		Reader: strings.NewReader(strings.Repeat("\n", 10000)),
		Split:  c.split,
	}
	for s.Scan() {
	}
	if s.Err() != nil {
		t.Fatal("after scan:", s.Err())
	}
	if c != 0 {
		t.Fatalf("stopped with %d left to process", c)
	}
}

// Make sure we can read a huge token if a big enough buffer is provided.
func TestHugeBuffer(t *testing.T) {
	text := strings.Repeat("x", 2*protoscan.MaxBuffer)
	s := protoscan.Protoscan{
		Reader:    strings.NewReader(text + "\n"),
		Split:     protoscan.ScanLines,
		Buffer:    make([]byte, 100),
		MaxBuffer: 2*protoscan.MaxBuffer + 1,
	}
	for s.Scan() {
		if string(s.Tokens()) != text {
			t.Errorf("scan got incorrect token of length %d", len(s.Tokens()))
		}
	}
	if s.Err() != nil {
		t.Fatal("after scan:", s.Err())
	}
}

// negativeEOFReader returns an invalid -1 at the end, as though it
// were wrapping the read system call.
type negativeEOFReader int

func (r *negativeEOFReader) Read(s []byte) (int, error) {
	if *r > 0 {
		c := int(*r)
		if c > len(s) {
			c = len(s)
		}
		for i := 0; i < c; i++ {
			s[i] = 'a'
		}
		s[c-1] = '\n'
		*r -= negativeEOFReader(c)
		return c, nil
	}
	return -1, io.EOF
}

// Test that the scanner doesn't panic and returns ErrBadReadCount
// on a reader that returns a negative count of bytes read
// (issue https://github.com/golang/go/issues/38053).
func TestNegativeEOFReader(t *testing.T) {
	r := negativeEOFReader(10)
	s := protoscan.Protoscan{
		Reader: &r,
		Split:  protoscan.ScanLines,
	}
	c := 0
	var l []string
	for s.Scan() {
		c++
		l = append(l, fmt.Sprintf("%q", s.Tokens()))
		if c > 10 {
			t.Errorf("read too many lines: %d, %v", c, l)
			break
		}
	}
	if got, want := s.Err(), protoscan.BadReadCount; got != want {
		t.Errorf("Err: got %v, want %v", got, want)
	}
}

// largeReader returns an invalid count that is larger than the number
// of bytes requested.
type largeReader struct{}

func (largeReader) Read(s []byte) (int, error) {
	return len(s) + 1, nil
}

// Test that the scanner doesn't panic and returns ErrBadReadCount
// on a reader that returns an impossibly large count of bytes read
// (issue https://github.com/golang/go/issues/38053).
func TestLargeReader(t *testing.T) {
	s := protoscan.Protoscan{
		Reader: largeReader{},
		Split:  protoscan.ScanLines,
	}
	for s.Scan() {
	}
	if got, want := s.Err(), protoscan.BadReadCount; got != want {
		t.Errorf("Err: got %v, want %v", got, want)
	}
}
