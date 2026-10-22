package lzma

import "fmt"

// LZMA2Decoder decodes an LZMA2 chunk sequence that arrives in pieces. Each
// piece passed to Decode must consist of whole chunks.
type LZMA2Decoder struct {
	dict          *dict
	state         state
	needDictReset bool
	needProps     bool
}

// NewLZMA2Decoder returns a decoder for a stream that declared dictSize. At
// most maxHistory bytes of history are retained; a stream that refers further
// back fails with ErrLimit.
func NewLZMA2Decoder(dictSize uint64, maxHistory int) *LZMA2Decoder {
	return &LZMA2Decoder{
		dict:          newDict(dictSize, maxHistory),
		needDictReset: true,
		needProps:     true,
	}
}

// Decode decodes chunks from in until out is full and returns the number of
// input bytes consumed. Output must end on a chunk boundary.
func (z *LZMA2Decoder) Decode(in, out []byte) (int, error) {
	ip, op := 0, 0
	for op < len(out) {
		if ip >= len(in) {
			return ip, fmt.Errorf("%w: lzma2 input ends after %d of %d bytes", ErrCorrupt, op, len(out))
		}
		c := in[ip]
		switch {
		case c == 0:
			return ip, fmt.Errorf("%w: lzma2 end marker before %d bytes of output", ErrCorrupt, len(out))
		case c == 1 || c >= 0xE0:
			z.needDictReset = false
			z.dict.reset()
			if c == 1 {
				z.needProps = true
			}
		case z.needDictReset:
			return ip, fmt.Errorf("%w: lzma2 stream does not start with a dictionary reset", ErrCorrupt)
		}

		if c < 0x80 {
			if c > 2 {
				return ip, fmt.Errorf("%w: invalid lzma2 control byte %#x", ErrCorrupt, c)
			}
			if len(in)-ip < 3 {
				return ip, fmt.Errorf("%w: truncated lzma2 chunk header", ErrCorrupt)
			}
			size := int(in[ip+1])<<8 | int(in[ip+2]) + 1
			ip += 3
			if size > len(in)-ip || size > len(out)-op {
				return ip, fmt.Errorf("%w: lzma2 uncompressed chunk overruns its section", ErrCorrupt)
			}
			z.dict.write(in[ip : ip+size])
			copy(out[op:], in[ip:ip+size])
			ip += size
			op += size
			continue
		}

		hdr := 5
		if c >= 0xC0 {
			hdr = 6
		}
		if len(in)-ip < hdr {
			return ip, fmt.Errorf("%w: truncated lzma2 chunk header", ErrCorrupt)
		}
		usize := int(c&0x1F)<<16 | int(in[ip+1])<<8 | int(in[ip+2]) + 1
		csize := int(in[ip+3])<<8 | int(in[ip+4]) + 1
		switch {
		case c >= 0xC0:
			p, ok := decodeProps(in[ip+5])
			if !ok {
				return ip, fmt.Errorf("%w: invalid lzma2 properties %#x", ErrCorrupt, in[ip+5])
			}
			z.state.setProps(p)
			z.needProps = false
		case z.needProps:
			return ip, fmt.Errorf("%w: lzma2 chunk lacks required properties", ErrCorrupt)
		case c >= 0xA0:
			z.state.reset()
		}
		ip += hdr
		if csize > len(in)-ip || usize > len(out)-op {
			return ip, fmt.Errorf("%w: lzma2 chunk overruns its section", ErrCorrupt)
		}
		ok, limited := z.state.decodeChunk(z.dict, in[ip:ip+csize], out[op:op+usize])
		if limited {
			return ip, fmt.Errorf("%w: match distance exceeds %d retained bytes", ErrLimit, z.dict.cap)
		}
		if !ok {
			return ip, fmt.Errorf("%w: invalid lzma2 chunk data", ErrCorrupt)
		}
		ip += csize
		op += usize
	}
	return ip, nil
}
