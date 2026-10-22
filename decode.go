package godelta

import (
	"context"
	"errors"
	"hash/adler32"
	"io"
	"math"

	"github.com/PeronGH/godelta/internal/lzma"
)

// Decoder applies a single delta. Create one with NewDecoder, which reads the
// file header, then call Decode once.
type Decoder struct {
	dr     *deltaReader
	hdr    Header
	opts   Options
	used   bool
	raw    [3][]byte
	dec    [3][]byte
	second [3]*lzma.XZDecoder
}

// NewDecoder reads the VCDIFF header from delta and returns a Decoder for the
// rest of it. The delta is read forward only, in a single pass.
func NewDecoder(delta io.Reader, opts *Options) (*Decoder, error) {
	d := &Decoder{dr: newDeltaReader(delta), opts: opts.withDefaults()}
	var err error
	if d.hdr, err = readHeader(d.dr, d.opts.MaxWindowSize); err != nil {
		return nil, err
	}
	return d, nil
}

// Header returns the delta's file header.
func (d *Decoder) Header() Header { return d.hdr }

// Decode applies a single delta to src (srcSize bytes) and streams the target
// to dst window by window. src may be nil if the delta does not copy from a
// source. It returns the number of target bytes written.
func Decode(ctx context.Context, src io.ReaderAt, srcSize int64, delta io.Reader, dst io.Writer, opts *Options) (int64, error) {
	d, err := NewDecoder(delta, opts)
	if err != nil {
		return 0, err
	}
	return d.Decode(ctx, src, srcSize, dst)
}

// Decode decodes the remaining windows. See the package-level Decode.
func (d *Decoder) Decode(ctx context.Context, src io.ReaderAt, srcSize int64, dst io.Writer) (int64, error) {
	if d.used {
		return 0, errors.New("godelta: Decoder.Decode called twice")
	}
	d.used = true
	o := &d.opts
	sc := newSourceCache(ctx, src, srcSize, o)
	hist := targetHistory{limit: o.TargetHistory}
	var spare []byte
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		w, err := readWindowHeader(d.dr, o.MaxWindowSize)
		if err == io.EOF {
			return written, nil
		}
		if err != nil {
			return written, err
		}
		if w.tgtLen > math.MaxInt64-written {
			return written, d.dr.err(ErrCorrupt, "total target length overflows")
		}
		if w.ind&winTarget != 0 && w.cpyOff+w.cpyLen > written {
			return written, d.dr.err(ErrCorrupt, "VCD_TARGET segment [%d,%d) extends past the decoded target (%d bytes)", w.cpyOff, w.cpyOff+w.cpyLen, written)
		}
		data, inst, addr, err := d.readSections(&w)
		if err != nil {
			return written, err
		}

		tgt := spare
		spare = nil
		if int64(cap(tgt)) < w.tgtLen {
			tgt = make([]byte, w.tgtLen)
		}
		tgt = tgt[:w.tgtLen]

		var seg func(p []byte, off int64) error
		switch {
		case w.ind&winSource != 0:
			seg = sc.copyAt
		case w.ind&winTarget != 0:
			seg = hist.copyAt
		}
		if err := d.emit(&w, data, inst, addr, tgt, seg); err != nil {
			return written, err
		}
		if w.ind&winAdler32 != 0 && !o.SkipChecksum {
			if sum := adler32.Checksum(tgt); sum != w.adler {
				return written, d.dr.err(ErrChecksum, "target window Adler-32 %08x, expected %08x", sum, w.adler)
			}
		}
		if _, err := dst.Write(tgt); err != nil {
			return written, err
		}
		spare = hist.add(written, tgt)
		written += w.tgtLen
		if o.Progress != nil {
			o.Progress(written, d.dr.off)
		}
	}
}

// sectionLimit bounds section sizes relative to the target window. With the
// default code table every instruction produces at least one byte and needs at
// most a few bytes of instruction and address data, so valid deltas stay well
// below it.
func sectionLimit(tgtLen int64) int64 {
	return 3*tgtLen + 1<<16
}

// readSections reads the data, instruction and address sections of w and
// undoes secondary compression.
func (d *Decoder) readSections(w *windowHeader) (data, inst, addr []byte, err error) {
	lens := [3]int64{w.dataLen, w.instLen, w.addrLen}
	names := [3]string{"data", "instruction", "address"}
	limit := sectionLimit(w.tgtLen)
	for i, n := range lens {
		if n > limit {
			return nil, nil, nil, d.dr.err(ErrLimit, "%s section length %d is implausible for a %d-byte window", names[i], n, w.tgtLen)
		}
		if d.raw[i], err = d.dr.readInto(d.raw[i], n); err != nil {
			return nil, nil, nil, err
		}
	}
	var out [3][]byte
	for i := range out {
		out[i] = d.raw[i]
		if w.delInd&(1<<i) == 0 {
			continue
		}
		if out[i], err = d.decodeSecondary(i, names[i], limit); err != nil {
			return nil, nil, nil, err
		}
	}
	return out[0], out[1], out[2], nil
}

func (d *Decoder) decodeSecondary(i int, name string, limit int64) ([]byte, error) {
	switch d.hdr.Secondary {
	case SecondaryLZMA:
	case SecondaryNone:
		return nil, d.dr.err(ErrCorrupt, "compressed %s section without a secondary compressor", name)
	case SecondaryDJW:
		return nil, d.dr.unsupported("DJW secondary compression")
	case SecondaryFGK:
		return nil, d.dr.unsupported("FGK secondary compression")
	default:
		return nil, d.dr.unsupported("secondary compressor " + d.hdr.Secondary.String())
	}
	sr := sliceReader{p: d.raw[i]}
	size, ok := sr.varint()
	if !ok || size == 0 {
		return nil, d.dr.err(ErrCorrupt, "invalid decompressed size of %s section", name)
	}
	if size > uint64(limit) {
		return nil, d.dr.err(ErrLimit, "decompressed %s section size %d is implausible", name, size)
	}
	if uint64(cap(d.dec[i])) < size {
		d.dec[i] = make([]byte, size)
	}
	out := d.dec[i][:size]
	if d.second[i] == nil {
		d.second[i] = lzma.NewXZDecoder(d.opts.MaxSecondaryHistory)
	}
	if err := d.second[i].Decode(d.raw[i][sr.i:], out); err != nil {
		e := d.dr.err(ErrCorrupt, "%s section", name)
		switch {
		case errors.Is(err, lzma.ErrLimit):
			e.Kind = ErrLimit
		case errors.Is(err, lzma.ErrUnsupported):
			e.Kind = ErrUnsupported
			e.Feature = "LZMA stream format"
		}
		e.Err = err
		return nil, e
	}
	return out, nil
}

// emit executes the window's instructions into tgt. seg copies from the
// window's source or target segment.
func (d *Decoder) emit(w *windowHeader, data, inst, addr []byte, tgt []byte, seg func([]byte, int64) error) error {
	var cache addrCache
	is := sliceReader{p: inst}
	as := sliceReader{p: addr}
	di := uint64(0)
	cpyLen := uint64(w.cpyLen)
	tgtLen := uint64(len(tgt))
	var pos uint64
	for !is.done() {
		code := inst[is.i]
		is.i++
		for _, h := range defaultCodeTable[code] {
			if h.typ == instNoop {
				continue
			}
			size := uint64(h.size)
			if size == 0 {
				var ok bool
				if size, ok = is.varint(); !ok {
					return d.dr.err(ErrCorrupt, "truncated instruction size")
				}
			}
			if size > tgtLen-pos {
				return d.dr.err(ErrCorrupt, "instruction overflows the %d-byte target window", tgtLen)
			}
			switch h.typ {
			case instAdd:
				if size > uint64(len(data))-di {
					return d.dr.err(ErrCorrupt, "ADD exceeds the data section")
				}
				copy(tgt[pos:pos+size], data[di:di+size])
				di += size
				pos += size
			case instRun:
				if di >= uint64(len(data)) {
					return d.dr.err(ErrCorrupt, "RUN exceeds the data section")
				}
				b := data[di]
				di++
				p := tgt[pos : pos+size]
				for i := range p {
					p[i] = b
				}
				pos += size
			case instCopy:
				here := cpyLen + pos
				a, ok := decodeAddr(&as, &cache, h.mode, here)
				if !ok {
					return d.dr.err(ErrCorrupt, "invalid COPY address")
				}
				if a >= here {
					return d.dr.err(ErrCorrupt, "COPY address %d is not before the current position %d", a, here)
				}
				if a < cpyLen {
					n := min(size, cpyLen-a)
					if err := seg(tgt[pos:pos+n], w.cpyOff+int64(a)); err != nil {
						return d.segErr(w, err)
					}
					pos += n
					a += n
					size -= n
				}
				// Copy from the current target window. The ranges may
				// overlap, which repeats the most recent bytes.
				t := a - cpyLen
				for size > 0 {
					n := min(size, pos-t)
					copy(tgt[pos:pos+n], tgt[t:t+n])
					pos += n
					t += n
					size -= n
				}
			}
		}
	}
	switch {
	case pos != tgtLen:
		return d.dr.err(ErrCorrupt, "instructions produce %d bytes, window declares %d", pos, tgtLen)
	case di != uint64(len(data)):
		return d.dr.err(ErrCorrupt, "%d unused bytes in the data section", uint64(len(data))-di)
	case !as.done():
		return d.dr.err(ErrCorrupt, "%d unused bytes in the address section", len(addr)-as.i)
	}
	return nil
}

func decodeAddr(as *sliceReader, cache *addrCache, mode byte, here uint64) (uint64, bool) {
	var a uint64
	switch {
	case mode == 0:
		v, ok := as.varint()
		if !ok {
			return 0, false
		}
		a = v
	case mode == 1:
		v, ok := as.varint()
		if !ok || v > here {
			return 0, false
		}
		a = here - v
	case mode < 2+nearModes:
		v, ok := as.varint()
		if !ok {
			return 0, false
		}
		a = cache.near[mode-2] + v
		if a < v {
			return 0, false
		}
	default:
		b, ok := as.byte()
		if !ok {
			return 0, false
		}
		a = cache.same[int(mode-2-nearModes)*256+int(b)]
	}
	cache.update(a)
	return a, true
}

func (d *Decoder) segErr(w *windowHeader, err error) error {
	var re rangeError
	var se *sourceReadError
	switch {
	case w.ind&winTarget != 0:
		e := d.dr.err(ErrLimit, "")
		e.Msg = err.Error()
		return e
	case errors.As(err, &re):
		e := d.dr.err(ErrSourceRange, "")
		e.Msg = re.Error()
		return e
	case errors.As(err, &se) && errors.Is(se.err, io.ErrUnexpectedEOF):
		e := d.dr.err(ErrSourceRange, "source is shorter than its declared size")
		e.Err = se.err
		return e
	case errors.As(err, &se):
		return se.err
	}
	return err
}
