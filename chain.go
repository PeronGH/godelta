package godelta

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
)

// ApplyChain applies deltas in order: deltas[0] applies to src, and each
// following delta applies to the previous one's output. The final target is
// streamed to dst. Intermediate results up to Options.ChainMemoryLimit bytes
// stay in memory; larger ones spill to temporary files in tempDir (the
// default temporary directory if empty), which are removed before returning.
func ApplyChain(ctx context.Context, src io.ReaderAt, srcSize int64, deltas []io.Reader, dst io.Writer, tempDir string, opts *Options) (written int64, err error) {
	if len(deltas) == 0 {
		return 0, errors.New("godelta: ApplyChain needs at least one delta")
	}
	o := opts.withDefaults()
	var prev *spill
	defer func() {
		if prev != nil {
			err = errors.Join(err, prev.close())
		}
	}()
	for _, delta := range deltas[:len(deltas)-1] {
		next := &spill{limit: o.ChainMemoryLimit, dir: tempDir}
		_, err := Decode(ctx, src, srcSize, delta, next, &o)
		if prev != nil {
			err = errors.Join(err, prev.close())
		}
		prev = next
		if err != nil {
			return 0, err
		}
		src, srcSize = next.readerAt()
	}
	return Decode(ctx, src, srcSize, deltas[len(deltas)-1], dst, &o)
}

// spill buffers written data in memory up to limit, then moves it to a
// temporary file.
type spill struct {
	limit int64
	dir   string
	mem   []byte
	f     *os.File
	size  int64
}

func (s *spill) Write(p []byte) (int, error) {
	if s.f == nil {
		if int64(len(s.mem))+int64(len(p)) <= s.limit {
			s.mem = append(s.mem, p...)
			s.size += int64(len(p))
			return len(p), nil
		}
		f, err := os.CreateTemp(s.dir, "godelta-*")
		if err != nil {
			return 0, err
		}
		s.f = f
		if _, err := f.Write(s.mem); err != nil {
			return 0, err
		}
		s.mem = nil
	}
	n, err := s.f.Write(p)
	s.size += int64(n)
	return n, err
}

func (s *spill) readerAt() (io.ReaderAt, int64) {
	if s.f != nil {
		return s.f, s.size
	}
	return bytes.NewReader(s.mem), s.size
}

func (s *spill) close() error {
	s.mem = nil
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	return errors.Join(err, os.Remove(s.f.Name()))
}
