package godelta

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
)

// Merged instruction types.
const (
	wAdd = iota
	wRun
	wCopySource // copies from the original source
	wCopyTarget // copies from earlier output of the same delta
)

// winst is an instruction positioned in a whole target. For wAdd and wRun,
// addr indexes adds; for copies it is an absolute source or target offset.
type winst struct {
	typ  byte
	size int64
	addr int64
	pos  int64
}

type wininfo struct {
	offset   int64
	length   int64
	adler    uint32
	checksum bool
}

// whole is the instruction stream of an entire delta, as used by
// xdelta3-merge.h.
type whole struct {
	inst   []winst
	adds   []byte
	wins   []wininfo
	length int64
}

func (w *whole) push(in winst) {
	if n := len(w.inst); n > 0 {
		last := &w.inst[n-1]
		if last.typ == in.typ && last.pos+last.size == in.pos {
			switch in.typ {
			case wAdd:
				if last.addr+last.size == in.addr {
					last.size += in.size
					return
				}
			case wCopySource:
				// Target copies are not joined: that could make one
				// cross a window boundary.
				if last.addr+last.size == in.addr {
					last.size += in.size
					return
				}
			}
		}
	}
	w.inst = append(w.inst, in)
}

// Merge combines deltas d1…dn, where each d(i+1) applies to the output of
// d(i), into a single delta that applies to d1's source. The output is
// plain VCDIFF (no secondary compression) that xdelta3 can decode; it has the
// windows of the last delta and keeps their checksums.
//
// Merge holds every delta's instructions and added data in memory.
func Merge(ctx context.Context, deltas []io.Reader, dst io.Writer, opts *Options) error {
	if len(deltas) == 0 {
		return errors.New("godelta: Merge needs at least one delta")
	}
	o := opts.withDefaults()
	var acc *whole
	for _, r := range deltas {
		w, err := readWhole(ctx, r, &o)
		if err != nil {
			return err
		}
		if acc == nil {
			acc = w
			continue
		}
		if acc, err = mergeWhole(ctx, acc, w); err != nil {
			return err
		}
	}
	return writeWhole(ctx, acc, dst)
}

// readWhole decodes a delta's instructions without applying them.
func readWhole(ctx context.Context, r io.Reader, o *Options) (*whole, error) {
	d, err := NewDecoder(r, o)
	if err != nil {
		return nil, err
	}
	w := &whole{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := readWindowHeader(d.dr, o.MaxWindowSize)
		if err == io.EOF {
			return w, nil
		}
		if err != nil {
			return nil, err
		}
		if h.ind&winTarget != 0 {
			return nil, d.dr.unsupported("merging VCD_TARGET windows")
		}
		data, inst, addr, err := d.readSections(&h)
		if err != nil {
			return nil, err
		}
		w.wins = append(w.wins, wininfo{offset: w.length, length: h.tgtLen, adler: h.adler, checksum: h.ind&winAdler32 != 0})
		if err := d.collect(w, &h, data, inst, addr); err != nil {
			return nil, err
		}
	}
}

// collect appends the window's instructions to w. It validates them the same
// way emit does.
func (d *Decoder) collect(w *whole, h *windowHeader, data, inst, addr []byte) error {
	var cache addrCache
	is := sliceReader{p: inst}
	as := sliceReader{p: addr}
	di := uint64(0)
	cpyLen := uint64(h.cpyLen)
	tgtLen := uint64(h.tgtLen)
	start := w.length
	var pos uint64
	for !is.done() {
		code := inst[is.i]
		is.i++
		for _, hi := range defaultCodeTable[code] {
			if hi.typ == instNoop {
				continue
			}
			size := uint64(hi.size)
			if size == 0 {
				var ok bool
				if size, ok = is.varint(); !ok {
					return d.dr.err(ErrCorrupt, "truncated instruction size")
				}
			}
			if size > tgtLen-pos {
				return d.dr.err(ErrCorrupt, "instruction overflows the %d-byte target window", tgtLen)
			}
			if size == 0 {
				continue
			}
			at := start + int64(pos)
			switch hi.typ {
			case instAdd:
				if size > uint64(len(data))-di {
					return d.dr.err(ErrCorrupt, "ADD exceeds the data section")
				}
				w.push(winst{typ: wAdd, size: int64(size), addr: int64(len(w.adds)), pos: at})
				w.adds = append(w.adds, data[di:di+size]...)
				di += size
			case instRun:
				if di >= uint64(len(data)) {
					return d.dr.err(ErrCorrupt, "RUN exceeds the data section")
				}
				w.inst = append(w.inst, winst{typ: wRun, size: int64(size), addr: int64(len(w.adds)), pos: at})
				w.adds = append(w.adds, data[di])
				di++
			case instCopy:
				here := cpyLen + pos
				a, ok := decodeAddr(&as, &cache, hi.mode, here)
				if !ok {
					return d.dr.err(ErrCorrupt, "invalid COPY address")
				}
				if a >= here {
					return d.dr.err(ErrCorrupt, "COPY address %d is not before the current position %d", a, here)
				}
				rest := size
				if a < cpyLen {
					n := min(rest, cpyLen-a)
					w.push(winst{typ: wCopySource, size: int64(n), addr: h.cpyOff + int64(a), pos: at})
					at += int64(n)
					a += n
					rest -= n
				}
				if rest > 0 {
					w.inst = append(w.inst, winst{typ: wCopyTarget, size: int64(rest), addr: start + int64(a-cpyLen), pos: at})
				}
			}
			pos += size
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
	w.length += h.tgtLen
	return nil
}

// find returns the index of the instruction covering target offset off.
func (w *whole) find(off int64) int {
	return sort.Search(len(w.inst), func(i int) bool {
		return w.inst[i].pos+w.inst[i].size > off
	})
}

// mergeWhole applies in, whose source is src's target, to src and returns a
// whole that applies to src's source.
func mergeWhole(ctx context.Context, src, in *whole) (*whole, error) {
	out := &whole{wins: in.wins, length: in.length}
	type span struct {
		addr, size int64 // range of src's target
		// For a periodic (self-overlapping) copy: index of the src
		// instruction and output position where the span began.
		periodic int
		startOut int64
	}
	var stack []span
	var outPos int64
	winIdx := 0
	for n, ii := range in.inst {
		if n%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		switch ii.typ {
		case wAdd:
			out.push(winst{typ: wAdd, size: ii.size, addr: int64(len(out.adds)), pos: ii.pos})
			out.adds = append(out.adds, in.adds[ii.addr:ii.addr+ii.size]...)
			outPos = ii.pos + ii.size
			continue
		case wRun:
			out.inst = append(out.inst, winst{typ: wRun, size: ii.size, addr: int64(len(out.adds)), pos: ii.pos})
			out.adds = append(out.adds, in.adds[ii.addr])
			outPos = ii.pos + ii.size
			continue
		case wCopyTarget:
			out.push(ii)
			outPos = ii.pos + ii.size
			continue
		}
		if ii.addr+ii.size > src.length {
			return nil, fmt.Errorf("%w: merge: copy of [%d,%d) beyond the previous target (%d bytes)", ErrCorrupt, ii.addr, ii.addr+ii.size, src.length)
		}
		outPos = ii.pos
		stack = append(stack[:0], span{addr: ii.addr, size: ii.size, periodic: -1})
		for len(stack) > 0 {
			sp := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if sp.periodic >= 0 {
				// Bytes of a self-overlapping copy repeat with period d.
				// Once a full period has been emitted in the current
				// output window, copy the rest from the output itself.
				s := &src.inst[sp.periodic]
				d := s.pos - s.addr
				if sp.startOut < 0 {
					sp.startOut = outPos
				}
				for winIdx < len(out.wins)-1 && out.wins[winIdx].offset+out.wins[winIdx].length <= outPos {
					winIdx++
				}
				win := out.wins[winIdx]
				if outPos-d >= max(sp.startOut, win.offset) {
					n := min(sp.size, win.offset+win.length-outPos)
					out.push(winst{typ: wCopyTarget, size: n, addr: outPos - d, pos: outPos})
					outPos += n
					if n < sp.size {
						sp.addr += n
						sp.size -= n
						stack = append(stack, sp)
					}
					continue
				}
				off := (sp.addr - s.pos) % d
				n := min(sp.size, d-off)
				if n < sp.size {
					rest := sp
					rest.addr += n
					rest.size -= n
					stack = append(stack, rest)
				}
				stack = append(stack, span{addr: s.addr + off, size: n, periodic: -1})
				continue
			}

			i := src.find(sp.addr)
			s := &src.inst[i]
			off := sp.addr - s.pos
			take := min(sp.size, s.size-off)
			if take < sp.size {
				stack = append(stack, span{addr: sp.addr + take, size: sp.size - take, periodic: -1})
			}
			switch s.typ {
			case wRun:
				out.inst = append(out.inst, winst{typ: wRun, size: take, addr: int64(len(out.adds)), pos: outPos})
				out.adds = append(out.adds, src.adds[s.addr])
			case wAdd:
				out.push(winst{typ: wAdd, size: take, addr: int64(len(out.adds)), pos: outPos})
				out.adds = append(out.adds, src.adds[s.addr+off:s.addr+off+take]...)
			case wCopySource:
				out.push(winst{typ: wCopySource, size: take, addr: s.addr + off, pos: outPos})
			case wCopyTarget:
				if s.addr+s.size <= s.pos {
					stack = append(stack, span{addr: s.addr + off, size: take, periodic: -1})
				} else {
					stack = append(stack, span{addr: sp.addr, size: take, periodic: i, startOut: -1})
				}
				continue
			}
			outPos += take
		}
	}
	return out, nil
}

// writeWhole encodes w using the windows recorded in it.
func writeWhole(ctx context.Context, w *whole, dst io.Writer) error {
	if err := writeFileHeader(dst, nil); err != nil {
		return err
	}
	var enc windowEncoder
	var buf []byte
	i := 0
	var split int64 // bytes of w.inst[i] already emitted
	for _, win := range w.wins {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := win.offset + win.length
		// Find the source range copied by this window.
		var srcMin, srcMax int64 = -1, -1
		for j, sk := i, split; j < len(w.inst) && w.inst[j].pos+sk < end; j, sk = j+1, 0 {
			in := w.inst[j]
			if in.typ != wCopySource {
				continue
			}
			n := min(in.size-sk, end-(in.pos+sk))
			if srcMin < 0 || in.addr+sk < srcMin {
				srcMin = in.addr + sk
			}
			srcMax = max(srcMax, in.addr+sk+n)
		}
		if srcMin >= 0 {
			enc.reset(winSource, srcMin, srcMax-srcMin)
		} else {
			enc.reset(0, 0, 0)
		}
		for i < len(w.inst) && w.inst[i].pos+split < end {
			in := w.inst[i]
			n := min(in.size-split, end-(in.pos+split))
			switch in.typ {
			case wAdd:
				enc.add(w.adds[in.addr+split : in.addr+split+n])
			case wRun:
				enc.run(w.adds[in.addr], uint64(n))
			case wCopySource:
				enc.copy(uint64(in.addr+split-srcMin), uint64(n))
			case wCopyTarget:
				a := in.addr + split - win.offset
				if a < 0 {
					return fmt.Errorf("godelta: merge: target copy crosses a window boundary")
				}
				enc.copy(uint64(enc.cpyLen+a), uint64(n))
			}
			split += n
			if split == in.size {
				i++
				split = 0
			}
		}
		if enc.tgtLen != win.length {
			return fmt.Errorf("godelta: merge: window at %d has %d bytes, want %d", win.offset, enc.tgtLen, win.length)
		}
		enc.adler = win.adler
		buf = appendWindow(buf[:0], &enc, win.checksum)
		if _, err := dst.Write(buf); err != nil {
			return err
		}
	}
	return nil
}
