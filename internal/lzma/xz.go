package lzma

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

var xzMagic = []byte{0xFD, '7', 'z', 'X', 'Z', 0x00}

const filterLZMA2 = 0x21

// XZDecoder decodes an xz stream that is never finished: a stream header, a
// single block header with one LZMA2 filter, then LZMA2 chunks delivered in
// pieces. This is the framing produced by liblzma's stream encoder when it
// is only ever flushed with LZMA_SYNC_FLUSH.
type XZDecoder struct {
	maxHistory int
	headerDone bool
	lz         *LZMA2Decoder
}

// NewXZDecoder returns a decoder that retains at most maxHistory bytes of
// LZMA2 dictionary.
func NewXZDecoder(maxHistory int) *XZDecoder {
	return &XZDecoder{maxHistory: maxHistory}
}

// Decode consumes all of in and fills out exactly.
func (x *XZDecoder) Decode(in, out []byte) error {
	if !x.headerDone {
		n, err := x.readHeaders(in)
		if err != nil {
			return err
		}
		in = in[n:]
		x.headerDone = true
	}
	n, err := x.lz.Decode(in, out)
	if err != nil {
		return err
	}
	if n != len(in) {
		return fmt.Errorf("%w: %d unused input bytes after section", ErrCorrupt, len(in)-n)
	}
	return nil
}

func (x *XZDecoder) readHeaders(in []byte) (int, error) {
	// Stream header: magic, two flag bytes, CRC32 of the flags. The check
	// type is irrelevant because the block is never finished.
	if len(in) < 12 || !bytes.Equal(in[:6], xzMagic) {
		return 0, fmt.Errorf("%w: missing xz stream header", ErrCorrupt)
	}
	if in[6] != 0 || in[7] > 0x0F || crc32.ChecksumIEEE(in[6:8]) != binary.LittleEndian.Uint32(in[8:12]) {
		return 0, fmt.Errorf("%w: invalid xz stream flags", ErrCorrupt)
	}
	p := in[12:]
	if len(p) == 0 || p[0] == 0 {
		return 0, fmt.Errorf("%w: missing xz block header", ErrCorrupt)
	}
	size := (int(p[0]) + 1) * 4
	if len(p) < size {
		return 0, fmt.Errorf("%w: truncated xz block header", ErrCorrupt)
	}
	hdr := p[:size]
	if crc32.ChecksumIEEE(hdr[:size-4]) != binary.LittleEndian.Uint32(hdr[size-4:]) {
		return 0, fmt.Errorf("%w: xz block header checksum mismatch", ErrCorrupt)
	}
	dictSize, err := parseBlockHeader(hdr[1 : size-4])
	if err != nil {
		return 0, err
	}
	x.lz = NewLZMA2Decoder(dictSize, x.maxHistory)
	return 12 + size, nil
}

// parseBlockHeader parses the block flags, optional sizes and filter flags,
// returning the LZMA2 dictionary size.
func parseBlockHeader(h []byte) (uint64, error) {
	flags := h[0]
	h = h[1:]
	if flags&0x3C != 0 {
		return 0, fmt.Errorf("%w: reserved xz block flags set", ErrCorrupt)
	}
	if flags&0x03 != 0 {
		return 0, fmt.Errorf("%w: xz filter chain with %d filters", ErrUnsupported, flags&0x03+1)
	}
	for _, present := range []bool{flags&0x40 != 0, flags&0x80 != 0} {
		if !present {
			continue
		}
		var err error
		if _, h, err = readXZVarint(h); err != nil {
			return 0, err
		}
	}
	id, h, err := readXZVarint(h)
	if err != nil {
		return 0, err
	}
	if id != filterLZMA2 {
		return 0, fmt.Errorf("%w: xz filter %#x", ErrUnsupported, id)
	}
	n, h, err := readXZVarint(h)
	if err != nil {
		return 0, err
	}
	if n != 1 || len(h) < 1 {
		return 0, fmt.Errorf("%w: invalid lzma2 filter properties", ErrCorrupt)
	}
	dictSize, ok := lzma2DictSize(h[0])
	if !ok {
		return 0, fmt.Errorf("%w: invalid lzma2 dictionary size %#x", ErrCorrupt, h[0])
	}
	for _, b := range h[1:] {
		if b != 0 {
			return 0, fmt.Errorf("%w: non-zero xz block header padding", ErrCorrupt)
		}
	}
	return dictSize, nil
}

func readXZVarint(p []byte) (uint64, []byte, error) {
	var v uint64
	for i := 0; i < 9 && i < len(p); i++ {
		b := p[i]
		v |= uint64(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			if b == 0 && i > 0 {
				break
			}
			return v, p[i+1:], nil
		}
	}
	return 0, nil, fmt.Errorf("%w: invalid xz integer", ErrCorrupt)
}

func lzma2DictSize(b byte) (uint64, bool) {
	if b > 40 {
		return 0, false
	}
	if b == 40 {
		return 0xFFFFFFFF, true
	}
	return uint64(2|b&1) << (b/2 + 11), true
}
