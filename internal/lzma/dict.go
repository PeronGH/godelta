package lzma

const minDictAlloc = 1 << 16

// dict is the LZMA sliding window. It starts small and grows on demand up to
// its capacity, so memory follows the amount of data actually decoded rather
// than the dictionary size declared by the stream.
type dict struct {
	buf      []byte
	cap      int    // retained capacity; buf grows up to this
	declared uint64 // dictionary size declared by the stream
	pos      int    // next write index in buf
	full     int    // number of valid history bytes
	total    uint64 // bytes written since the last reset
}

func newDict(declared uint64, limit int) *dict {
	return &dict{cap: int(min(declared, uint64(limit))), declared: declared}
}

// trimmed reports whether distance dist-1 (a zero-based LZMA distance) was
// valid for the stream but is no longer retained because cap < declared.
func (d *dict) trimmed(dist uint32) bool {
	return uint64(dist) < d.total && uint64(dist) < d.declared
}

func (d *dict) reset() {
	d.pos = 0
	d.full = 0
	d.total = 0
}

// makeRoom ensures that buf[pos] is writable, growing or wrapping as needed.
func (d *dict) makeRoom() {
	if d.pos < len(d.buf) {
		return
	}
	if len(d.buf) < d.cap {
		n := max(2*len(d.buf), minDictAlloc)
		n = min(n, d.cap)
		nb := make([]byte, n)
		copy(nb, d.buf[:d.pos])
		d.buf = nb
		return
	}
	d.pos = 0
}

func (d *dict) put(b byte) {
	d.makeRoom()
	d.buf[d.pos] = b
	d.pos++
	d.total++
	if d.full < len(d.buf) {
		d.full++
	}
}

// back returns the byte dist positions back; dist must be in [1, full].
func (d *dict) back(dist int) byte {
	i := d.pos - dist
	if i < 0 {
		i += len(d.buf)
	}
	return d.buf[i]
}

// prev returns the most recent byte, or 0 right after a reset.
func (d *dict) prev() byte {
	if d.full == 0 {
		return 0
	}
	return d.back(1)
}

// repeat copies n bytes from dist back to the current position and to out.
// The caller validates dist against full.
func (d *dict) repeat(dist, n int, out []byte) {
	for i := 0; i < n; {
		d.makeRoom()
		src := d.pos - dist
		if src < 0 {
			src += len(d.buf)
		}
		// Copy the largest run that neither wraps nor overlaps itself.
		k := min(n-i, len(d.buf)-d.pos, len(d.buf)-src)
		if src < d.pos {
			k = min(k, d.pos-src)
		}
		copy(d.buf[d.pos:d.pos+k], d.buf[src:src+k])
		copy(out[i:i+k], d.buf[d.pos:d.pos+k])
		d.pos += k
		d.total += uint64(k)
		d.full = min(d.full+k, len(d.buf))
		i += k
	}
}

// write appends p to the dictionary.
func (d *dict) write(p []byte) {
	for len(p) > 0 {
		d.makeRoom()
		k := copy(d.buf[d.pos:], p)
		d.pos += k
		d.total += uint64(k)
		d.full = min(d.full+k, len(d.buf))
		p = p[k:]
	}
}
