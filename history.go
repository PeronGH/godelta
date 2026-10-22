package godelta

import "fmt"

// targetHistory retains recently written target windows so that VCD_TARGET
// windows can copy from them. The most recent window is always kept; older
// ones are kept while the total stays within limit.
type targetHistory struct {
	limit int64
	wins  []histWindow // oldest first
	total int64
}

type histWindow struct {
	start int64
	data  []byte
}

// add retains data written at start and returns a buffer that is no longer
// referenced, if any, for reuse.
func (h *targetHistory) add(start int64, data []byte) (spare []byte) {
	h.wins = append(h.wins, histWindow{start, data})
	h.total += int64(len(data))
	for len(h.wins) > 1 && h.total > h.limit {
		old := h.wins[0]
		h.wins[0] = histWindow{}
		h.wins = h.wins[1:]
		h.total -= int64(len(old.data))
		if cap(old.data) > cap(spare) {
			spare = old.data
		}
	}
	return spare
}

// copyAt fills p with target bytes starting at absolute target offset off.
func (h *targetHistory) copyAt(p []byte, off int64) error {
	for len(p) > 0 {
		found := false
		for _, w := range h.wins {
			if off >= w.start && off < w.start+int64(len(w.data)) {
				n := copy(p, w.data[off-w.start:])
				p = p[n:]
				off += int64(n)
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("copy from target offset %d is outside the retained target history (see Options.TargetHistory)", off)
		}
	}
	return nil
}
