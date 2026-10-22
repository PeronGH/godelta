package godelta

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// sourceCache reads the source in fixed-size blocks through an LRU cache, so
// that a window only reads the parts of its source segment it copies from,
// and revisited blocks are not read twice.
type sourceCache struct {
	ctx       context.Context
	src       io.ReaderAt
	size      int64
	blockSize int64
	maxBlocks int
	blocks    map[int64]*block
	head      block // sentinel of the LRU list; head.next is most recent
	last      *block
}

type block struct {
	num        int64
	data       []byte
	prev, next *block
}

func newSourceCache(ctx context.Context, src io.ReaderAt, size int64, opts *Options) *sourceCache {
	c := &sourceCache{
		ctx:       ctx,
		src:       src,
		size:      size,
		blockSize: int64(opts.SourceBlockSize),
		maxBlocks: int(max(1, opts.SourceCacheSize/int64(opts.SourceBlockSize))),
		blocks:    make(map[int64]*block),
	}
	c.head.next = &c.head
	c.head.prev = &c.head
	return c
}

// copyAt fills p with source bytes starting at off.
func (c *sourceCache) copyAt(p []byte, off int64) error {
	if c.src == nil {
		return rangeError("window copies from the source but none was provided")
	}
	if off < 0 || off > c.size || int64(len(p)) > c.size-off {
		return rangeError(fmt.Sprintf("copy of %d bytes at source offset %d exceeds source size %d", len(p), off, c.size))
	}
	for len(p) > 0 {
		num := off / c.blockSize
		b, err := c.get(num)
		if err != nil {
			return err
		}
		n := copy(p, b.data[off-num*c.blockSize:])
		p = p[n:]
		off += int64(n)
	}
	return nil
}

func (c *sourceCache) get(num int64) (*block, error) {
	if c.last != nil && c.last.num == num {
		return c.last, nil
	}
	b, ok := c.blocks[num]
	if ok {
		c.unlink(b)
	} else {
		if err := c.ctx.Err(); err != nil {
			return nil, err
		}
		if len(c.blocks) >= c.maxBlocks {
			b = c.head.prev
			c.unlink(b)
			delete(c.blocks, b.num)
		} else {
			b = &block{data: make([]byte, c.blockSize)}
		}
		start := num * c.blockSize
		n := min(c.blockSize, c.size-start)
		b.num = num
		b.data = b.data[:cap(b.data)][:n]
		if k, err := c.src.ReadAt(b.data, start); k < len(b.data) {
			if err == nil || errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, &sourceReadError{err}
		}
		c.blocks[num] = b
	}
	// Insert at the front.
	b.prev = &c.head
	b.next = c.head.next
	c.head.next.prev = b
	c.head.next = b
	c.last = b
	return b, nil
}

func (c *sourceCache) unlink(b *block) {
	b.prev.next = b.next
	b.next.prev = b.prev
	if c.last == b {
		c.last = nil
	}
}

// sourceReadError marks a failed read from the source ReaderAt.
type sourceReadError struct{ err error }

func (e *sourceReadError) Error() string { return "reading source: " + e.err.Error() }
func (e *sourceReadError) Unwrap() error { return e.err }

// rangeError describes a copy outside the available source or target data.
type rangeError string

func (e rangeError) Error() string { return string(e) }
