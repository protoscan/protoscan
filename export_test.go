// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package protoscan

// Exported for testing only.

var (
	MaxBuffer = maxBuffer
	IsSpace   = isSpace
)

// ErrOrEOF is like Err, but returns EOF. Used to test a corner case.
func (s *Protoscan) ErrOrEOF() error { return s.err }
