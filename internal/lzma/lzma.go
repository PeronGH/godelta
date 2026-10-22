// Package lzma implements the parts of LZMA, LZMA2 and the xz container
// needed to decode xdelta3's LZMA secondary compression.
//
// Unlike a typical io.Reader based decompressor, the decoders here are fed
// whole input pieces and fill caller-sized outputs exactly. They never read
// ahead, keep their state across calls, and report malformed input as
// errors instead of panicking.
package lzma

import "errors"

var (
	// ErrCorrupt reports malformed compressed input.
	ErrCorrupt = errors.New("lzma: corrupt input")
	// ErrUnsupported reports valid input that this package does not decode,
	// such as xz filters other than LZMA2.
	ErrUnsupported = errors.New("lzma: unsupported input")
	// ErrLimit reports a match distance beyond the retained dictionary.
	ErrLimit = errors.New("lzma: dictionary limit exceeded")
)
