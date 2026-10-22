package godelta

import (
	"io"
	"math"
)

// CopySegment identifies where a window's copy segment comes from.
type CopySegment byte

// Copy segment kinds.
const (
	CopyNone   CopySegment = iota // the window copies only from itself
	CopySource                    // VCD_SOURCE: the segment is in the source
	CopyTarget                    // VCD_TARGET: the segment is earlier target data
)

// WindowInfo summarizes one window.
type WindowInfo struct {
	// DeltaOffset is the offset of the window header in the delta.
	DeltaOffset int64
	// TargetOffset and TargetLength locate the window's output.
	TargetOffset int64
	TargetLength int64
	// Copy, CopyOffset and CopyLength describe the copy segment.
	Copy       CopySegment
	CopyOffset int64
	CopyLength int64
	// HasChecksum reports whether the window carries an Adler-32 checksum.
	HasChecksum bool
	Checksum    uint32
	// Section lengths as stored in the delta.
	DataLength int64
	InstLength int64
	AddrLength int64
	// Whether each section uses secondary compression.
	DataCompressed bool
	InstCompressed bool
	AddrCompressed bool
}

// Info describes a delta without decoding it.
type Info struct {
	Header
	Windows []WindowInfo
}

// TargetSize returns the total length of the target.
func (in *Info) TargetSize() int64 {
	if len(in.Windows) == 0 {
		return 0
	}
	w := in.Windows[len(in.Windows)-1]
	return w.TargetOffset + w.TargetLength
}

// SourceSize returns the minimum source length the delta's source segments
// require.
func (in *Info) SourceSize() int64 {
	var n int64
	for _, w := range in.Windows {
		if w.Copy == CopySource {
			n = max(n, w.CopyOffset+w.CopyLength)
		}
	}
	return n
}

// Inspect parses the header and window headers of delta, skipping section
// contents. It needs no source and performs no decompression.
func Inspect(delta io.Reader) (*Info, error) {
	dr := newDeltaReader(delta)
	h, err := readHeader(dr, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	info := &Info{Header: h}
	var total int64
	for {
		w, err := readWindowHeader(dr, math.MaxInt64)
		if err == io.EOF {
			return info, nil
		}
		if err != nil {
			return nil, err
		}
		if w.tgtLen > math.MaxInt64-total {
			return nil, dr.err(ErrCorrupt, "total target length overflows")
		}
		wi := WindowInfo{
			DeltaOffset:    w.offset,
			TargetOffset:   total,
			TargetLength:   w.tgtLen,
			CopyOffset:     w.cpyOff,
			CopyLength:     w.cpyLen,
			HasChecksum:    w.ind&winAdler32 != 0,
			Checksum:       w.adler,
			DataLength:     w.dataLen,
			InstLength:     w.instLen,
			AddrLength:     w.addrLen,
			DataCompressed: w.delInd&delData != 0,
			InstCompressed: w.delInd&delInst != 0,
			AddrCompressed: w.delInd&delAddr != 0,
		}
		switch {
		case w.ind&winSource != 0:
			wi.Copy = CopySource
		case w.ind&winTarget != 0:
			wi.Copy = CopyTarget
		}
		info.Windows = append(info.Windows, wi)
		total += w.tgtLen
		if err := dr.skip(w.dataLen + w.instLen + w.addrLen); err != nil {
			return nil, err
		}
	}
}
