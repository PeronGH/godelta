package lzma

const (
	numStates          = 12
	posBitsMax         = 4
	lenToPosStates     = 4
	numAlignBits       = 4
	startPosModelIndex = 4
	endPosModelIndex   = 14
	numFullDistances   = 1 << (endPosModelIndex >> 1)
	matchMinLen        = 2
	probInit           = 1 << 10
	topValue           = 1 << 24
)

type prob = uint16

// rangeDecoder reads from a byte slice holding exactly one LZMA2 chunk.
// Reads past the end yield zero bytes and are detected via pos > len(in).
type rangeDecoder struct {
	in   []byte
	pos  int
	rng  uint32
	code uint32
}

func (rc *rangeDecoder) init(in []byte) bool {
	if len(in) < 5 || in[0] != 0 {
		return false
	}
	rc.in = in
	rc.pos = 5
	rc.rng = 0xFFFFFFFF
	rc.code = uint32(in[1])<<24 | uint32(in[2])<<16 | uint32(in[3])<<8 | uint32(in[4])
	return true
}

func (rc *rangeDecoder) overrun() bool { return rc.pos > len(rc.in) }

func (rc *rangeDecoder) normalize() {
	if rc.rng < topValue {
		rc.rng <<= 8
		var b byte
		if rc.pos < len(rc.in) {
			b = rc.in[rc.pos]
		}
		rc.pos++
		rc.code = rc.code<<8 | uint32(b)
	}
}

func (rc *rangeDecoder) bit(p *prob) uint32 {
	rc.normalize()
	bound := (rc.rng >> 11) * uint32(*p)
	if rc.code < bound {
		rc.rng = bound
		*p += (1<<11 - *p) >> 5
		return 0
	}
	rc.rng -= bound
	rc.code -= bound
	*p -= *p >> 5
	return 1
}

func (rc *rangeDecoder) direct(n uint32) uint32 {
	var res uint32
	for ; n > 0; n-- {
		rc.normalize()
		rc.rng >>= 1
		res <<= 1
		if rc.code >= rc.rng {
			rc.code -= rc.rng
			res |= 1
		}
	}
	return res
}

func (rc *rangeDecoder) tree(probs []prob, bits uint32) uint32 {
	m := uint32(1)
	for i := uint32(0); i < bits; i++ {
		m = m<<1 | rc.bit(&probs[m])
	}
	return m - 1<<bits
}

func (rc *rangeDecoder) reverseTree(probs []prob, bits uint32) uint32 {
	m := uint32(1)
	var sym uint32
	for i := uint32(0); i < bits; i++ {
		b := rc.bit(&probs[m])
		m = m<<1 | b
		sym |= b << i
	}
	return sym
}

type lenDecoder struct {
	choice  prob
	choice2 prob
	low     [1 << posBitsMax][1 << 3]prob
	mid     [1 << posBitsMax][1 << 3]prob
	high    [1 << 8]prob
}

func (ld *lenDecoder) reset() {
	ld.choice = probInit
	ld.choice2 = probInit
	resetProbs(ld.high[:])
	for i := range ld.low {
		resetProbs(ld.low[i][:])
		resetProbs(ld.mid[i][:])
	}
}

// decode returns the match length minus matchMinLen.
func (ld *lenDecoder) decode(rc *rangeDecoder, posState uint32) uint32 {
	if rc.bit(&ld.choice) == 0 {
		return rc.tree(ld.low[posState][:], 3)
	}
	if rc.bit(&ld.choice2) == 0 {
		return 8 + rc.tree(ld.mid[posState][:], 3)
	}
	return 16 + rc.tree(ld.high[:], 8)
}

func resetProbs(p []prob) {
	for i := range p {
		p[i] = probInit
	}
}

// props are the LZMA literal and position parameters.
type props struct {
	lc, lp, pb uint32
}

func decodeProps(b byte) (props, bool) {
	if b >= 9*5*5 {
		return props{}, false
	}
	p := props{lc: uint32(b % 9)}
	b /= 9
	p.lp = uint32(b % 5)
	p.pb = uint32(b / 5)
	return p, p.lc+p.lp <= 4
}

// state is the LZMA decoder model that persists across LZMA2 chunks.
type state struct {
	props
	literal    []prob
	isMatch    [numStates << posBitsMax]prob
	isRep      [numStates]prob
	isRepG0    [numStates]prob
	isRepG1    [numStates]prob
	isRepG2    [numStates]prob
	isRep0Long [numStates << posBitsMax]prob
	posSlot    [lenToPosStates][1 << 6]prob
	posSpecial [1 + numFullDistances - endPosModelIndex]prob
	align      [1 << numAlignBits]prob
	lenDec     lenDecoder
	repLenDec  lenDecoder
	st         uint32
	rep        [4]uint32
}

func (s *state) setProps(p props) {
	s.props = p
	n := 0x300 << (p.lc + p.lp)
	if cap(s.literal) >= n {
		s.literal = s.literal[:n]
	} else {
		s.literal = make([]prob, n)
	}
	s.reset()
}

func (s *state) reset() {
	resetProbs(s.literal)
	resetProbs(s.isMatch[:])
	resetProbs(s.isRep[:])
	resetProbs(s.isRepG0[:])
	resetProbs(s.isRepG1[:])
	resetProbs(s.isRepG2[:])
	resetProbs(s.isRep0Long[:])
	for i := range s.posSlot {
		resetProbs(s.posSlot[i][:])
	}
	resetProbs(s.posSpecial[:])
	resetProbs(s.align[:])
	s.lenDec.reset()
	s.repLenDec.reset()
	s.st = 0
	s.rep = [4]uint32{}
}

// decodeChunk decodes one LZMA chunk from in, filling out exactly. It
// returns false if the chunk is malformed. A match distance beyond the
// retained history (but within the stream's declared dictionary) is
// reported through limited.
func (s *state) decodeChunk(d *dict, in []byte, out []byte) (ok, limited bool) {
	var rc rangeDecoder
	if !rc.init(in) {
		return false, false
	}
	pbMask := uint32(1)<<s.pb - 1
	lpMask := uint32(1)<<s.lp - 1
	lc := s.lc
	st := s.st
	rep0, rep1, rep2, rep3 := s.rep[0], s.rep[1], s.rep[2], s.rep[3]
	defer func() {
		s.st = st
		s.rep = [4]uint32{rep0, rep1, rep2, rep3}
	}()

	op := 0
	for op < len(out) {
		if rc.overrun() {
			return false, false
		}
		posState := uint32(d.total) & pbMask
		if rc.bit(&s.isMatch[st<<posBitsMax+posState]) == 0 {
			prev := uint32(d.prev())
			base := 0x300 * ((uint32(d.total)&lpMask)<<lc + prev>>(8-lc))
			probs := s.literal[base : base+0x300]
			sym := uint32(1)
			if st >= 7 {
				if int(rep0) >= d.full {
					return false, d.trimmed(rep0)
				}
				match := uint32(d.back(int(rep0) + 1))
				for sym < 0x100 {
					mb := (match >> 7) & 1
					match <<= 1
					b := rc.bit(&probs[(1+mb)<<8+sym])
					sym = sym<<1 | b
					if mb != b {
						break
					}
				}
			}
			for sym < 0x100 {
				sym = sym<<1 | rc.bit(&probs[sym])
			}
			b := byte(sym)
			d.put(b)
			out[op] = b
			op++
			switch {
			case st < 4:
				st = 0
			case st < 10:
				st -= 3
			default:
				st -= 6
			}
			continue
		}

		var n uint32
		if rc.bit(&s.isRep[st]) == 0 {
			rep3, rep2, rep1 = rep2, rep1, rep0
			n = s.lenDec.decode(&rc, posState)
			if st < 7 {
				st = 7
			} else {
				st = 10
			}
			rep0 = s.decodeDistance(&rc, n)
			if rep0 == 0xFFFFFFFF {
				// End-of-payload marker; not allowed in LZMA2.
				return false, false
			}
		} else {
			if rc.bit(&s.isRepG0[st]) == 0 {
				if rc.bit(&s.isRep0Long[st<<posBitsMax+posState]) == 0 {
					if st < 7 {
						st = 9
					} else {
						st = 11
					}
					if int(rep0) >= d.full {
						return false, d.trimmed(rep0)
					}
					b := d.back(int(rep0) + 1)
					d.put(b)
					out[op] = b
					op++
					continue
				}
			} else {
				var dist uint32
				if rc.bit(&s.isRepG1[st]) == 0 {
					dist = rep1
				} else {
					if rc.bit(&s.isRepG2[st]) == 0 {
						dist = rep2
					} else {
						dist = rep3
						rep3 = rep2
					}
					rep2 = rep1
				}
				rep1 = rep0
				rep0 = dist
			}
			n = s.repLenDec.decode(&rc, posState)
			if st < 7 {
				st = 8
			} else {
				st = 11
			}
		}
		length := int(n) + matchMinLen
		if uint64(rep0) >= uint64(d.full) {
			return false, d.trimmed(rep0)
		}
		if length > len(out)-op {
			return false, false
		}
		d.repeat(int(rep0)+1, length, out[op:])
		op += length
	}
	// Account for a final normalization the encoder flushed.
	rc.normalize()
	return rc.pos == len(in) && rc.code == 0, false
}

func (s *state) decodeDistance(rc *rangeDecoder, n uint32) uint32 {
	lenState := min(n, lenToPosStates-1)
	slot := rc.tree(s.posSlot[lenState][:], 6)
	if slot < startPosModelIndex {
		return slot
	}
	direct := slot>>1 - 1
	dist := (2 | slot&1) << direct
	if slot < endPosModelIndex {
		return dist + rc.reverseTree(s.posSpecial[dist-slot:], direct)
	}
	dist += rc.direct(direct-numAlignBits) << numAlignBits
	return dist + rc.reverseTree(s.align[:], numAlignBits)
}
