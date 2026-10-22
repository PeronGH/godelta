package godelta

import "io"

// windowEncoder builds one VCDIFF window from single instructions using the
// default code table. It does not use secondary compression.
type windowEncoder struct {
	ind    byte
	cpyOff int64
	cpyLen int64
	tgtLen int64
	adler  uint32
	data   []byte
	inst   []byte
	addr   []byte
	cache  addrCache
}

func (e *windowEncoder) reset(ind byte, cpyOff, cpyLen int64) {
	*e = windowEncoder{ind: ind, cpyOff: cpyOff, cpyLen: cpyLen, data: e.data[:0], inst: e.inst[:0], addr: e.addr[:0]}
}

func (e *windowEncoder) add(p []byte) {
	n := uint64(len(p))
	if n <= 17 {
		e.inst = append(e.inst, byte(1+n))
	} else {
		e.inst = appendVarint(append(e.inst, 1), n)
	}
	e.data = append(e.data, p...)
	e.tgtLen += int64(n)
}

func (e *windowEncoder) run(b byte, n uint64) {
	e.inst = appendVarint(append(e.inst, 0), n)
	e.data = append(e.data, b)
	e.tgtLen += int64(n)
}

// copy emits a COPY of n bytes from address a in the window's address space
// (copy segment followed by the target window).
func (e *windowEncoder) copy(a, n uint64) {
	here := uint64(e.cpyLen + e.tgtLen)
	mode, enc, same := e.encodeAddr(a, here)
	code := 19 + 16*mode
	if n >= 4 && n <= 18 {
		e.inst = append(e.inst, byte(code+int(n)-3))
	} else {
		e.inst = appendVarint(append(e.inst, byte(code)), n)
	}
	if same {
		e.addr = append(e.addr, byte(enc))
	} else {
		e.addr = appendVarint(e.addr, enc)
	}
	e.tgtLen += int64(n)
}

// encodeAddr picks the address mode with the smallest encoding, following
// xdelta3's encoder.
func (e *windowEncoder) encodeAddr(a, here uint64) (mode int, enc uint64, same bool) {
	defer e.cache.update(a)
	mode, enc = 0, a
	if here-a < enc {
		mode, enc = 1, here-a
	}
	for i, near := range e.cache.near {
		if a >= near && a-near < enc {
			mode, enc = 2+i, a-near
		}
	}
	if enc < 0x80 {
		return mode, enc, false
	}
	slot := a % (sameModes * 256)
	if e.cache.same[slot] == a {
		return 2 + nearModes + int(slot/256), slot % 256, true
	}
	return mode, enc, false
}

func appendWindow(b []byte, e *windowEncoder, checksum bool) []byte {
	ind := e.ind
	if checksum {
		ind |= winAdler32
	}
	b = append(b, ind)
	if ind&(winSource|winTarget) != 0 {
		b = appendVarint(b, uint64(e.cpyLen))
		b = appendVarint(b, uint64(e.cpyOff))
	}
	encLen := 1 + sizeofVarint(uint64(e.tgtLen)) + sizeofVarint(uint64(len(e.data))) +
		sizeofVarint(uint64(len(e.inst))) + sizeofVarint(uint64(len(e.addr))) +
		int64(len(e.data)+len(e.inst)+len(e.addr))
	if checksum {
		encLen += 4
	}
	b = appendVarint(b, uint64(encLen))
	b = appendVarint(b, uint64(e.tgtLen))
	b = append(b, 0)
	b = appendVarint(b, uint64(len(e.data)))
	b = appendVarint(b, uint64(len(e.inst)))
	b = appendVarint(b, uint64(len(e.addr)))
	if checksum {
		b = append(b, byte(e.adler>>24), byte(e.adler>>16), byte(e.adler>>8), byte(e.adler))
	}
	b = append(b, e.data...)
	b = append(b, e.inst...)
	return append(b, e.addr...)
}

func writeFileHeader(w io.Writer, appHeader []byte) error {
	b := append([]byte(nil), magic...)
	if appHeader != nil {
		b = append(b, hdrAppHeader)
		b = appendVarint(b, uint64(len(appHeader)))
		b = append(b, appHeader...)
	} else {
		b = append(b, 0)
	}
	_, err := w.Write(b)
	return err
}
