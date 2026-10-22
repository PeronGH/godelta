package godelta

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
)

// deltaReader reads the delta in a single forward pass and tracks the offset
// and current window for error reporting.
type deltaReader struct {
	r      *bufio.Reader
	off    int64
	window int64
}

func newDeltaReader(r io.Reader) *deltaReader {
	return &deltaReader{r: bufio.NewReaderSize(r, 64<<10), window: -1}
}

func (d *deltaReader) err(kind error, format string, args ...any) *Error {
	return &Error{Kind: kind, Window: d.window, Offset: d.off, Msg: fmt.Sprintf(format, args...)}
}

func (d *deltaReader) unsupported(feature string) *Error {
	return &Error{Kind: ErrUnsupported, Feature: feature, Window: d.window, Offset: d.off}
}

// ioErr converts a read error. Running out of delta input means the delta is
// truncated; other errors come from the caller's reader and pass through.
func (d *deltaReader) ioErr(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return &Error{Kind: ErrCorrupt, Window: d.window, Offset: d.off, Msg: "truncated delta", Err: io.ErrUnexpectedEOF}
	}
	return err
}

func (d *deltaReader) readByte() (byte, error) {
	b, err := d.r.ReadByte()
	if err != nil {
		return 0, d.ioErr(err)
	}
	d.off++
	return b, nil
}

func (d *deltaReader) readVarint(what string) (uint64, error) {
	var v uint64
	for {
		b, err := d.readByte()
		if err != nil {
			return 0, err
		}
		if v > math.MaxUint64>>7 {
			return 0, d.err(ErrCorrupt, "%s overflows 64 bits", what)
		}
		v = v<<7 | uint64(b&0x7F)
		if b&0x80 == 0 {
			return v, nil
		}
	}
}

// readSize reads a varint that must fit in an int64.
func (d *deltaReader) readSize(what string) (int64, error) {
	v, err := d.readVarint(what)
	if err != nil {
		return 0, err
	}
	if v > math.MaxInt64 {
		return 0, d.err(ErrCorrupt, "%s %d out of range", what, v)
	}
	return int64(v), nil
}

func (d *deltaReader) readFull(p []byte) error {
	n, err := io.ReadFull(d.r, p)
	d.off += int64(n)
	if err != nil {
		return d.ioErr(err)
	}
	return nil
}

// readInto reads n bytes into buf, reusing its capacity. Memory grows with
// the data actually read, so a bogus length cannot force a huge allocation.
func (d *deltaReader) readInto(buf []byte, n int64) ([]byte, error) {
	const step = 1 << 20
	buf = buf[:0]
	for int64(len(buf)) < n {
		k := int(min(n-int64(len(buf)), step))
		buf = slices.Grow(buf, k)
		if err := d.readFull(buf[len(buf) : len(buf)+k]); err != nil {
			return nil, err
		}
		buf = buf[:len(buf)+k]
	}
	return buf, nil
}

func (d *deltaReader) skip(n int64) error {
	for n > 0 {
		k, err := d.r.Discard(int(min(n, 1<<30)))
		d.off += int64(k)
		n -= int64(k)
		if err != nil {
			return d.ioErr(err)
		}
	}
	return nil
}

// sliceReader parses varints from a decoded section.
type sliceReader struct {
	p []byte
	i int
}

func (s *sliceReader) varint() (uint64, bool) {
	var v uint64
	for s.i < len(s.p) {
		b := s.p[s.i]
		s.i++
		if v > math.MaxUint64>>7 {
			return 0, false
		}
		v = v<<7 | uint64(b&0x7F)
		if b&0x80 == 0 {
			return v, true
		}
	}
	return 0, false
}

func (s *sliceReader) byte() (byte, bool) {
	if s.i >= len(s.p) {
		return 0, false
	}
	b := s.p[s.i]
	s.i++
	return b, true
}

func (s *sliceReader) done() bool { return s.i == len(s.p) }

func sizeofVarint(v uint64) int64 {
	n := int64(1)
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

func appendVarint(b []byte, v uint64) []byte {
	var tmp [10]byte
	i := len(tmp) - 1
	tmp[i] = byte(v & 0x7F)
	for v >>= 7; v != 0; v >>= 7 {
		i--
		tmp[i] = byte(v&0x7F) | 0x80
	}
	return append(b, tmp[i:]...)
}
