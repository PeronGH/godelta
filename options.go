package godelta

// Default limits used when the corresponding Options field is zero.
const (
	DefaultMaxWindowSize       = 64 << 20
	DefaultSourceBlockSize     = 64 << 10
	DefaultSourceCacheSize     = 32 << 20
	DefaultMaxSecondaryHistory = 64 << 20
	DefaultChainMemoryLimit    = 64 << 20
)

// Options configures decoding. The zero value, or a nil *Options, selects
// the defaults.
type Options struct {
	// SkipChecksum disables Adler-32 verification of target windows.
	SkipChecksum bool

	// MaxWindowSize caps the length of a target window. Allocations derived
	// from header fields are bounded by it.
	MaxWindowSize int64

	// SourceBlockSize is the granularity of source reads.
	SourceBlockSize int

	// SourceCacheSize is the memory budget for cached source blocks. It is
	// rounded up to at least one block.
	SourceCacheSize int64

	// TargetHistory is the number of already-written target bytes retained
	// for windows that copy from earlier target data (VCD_TARGET). The
	// previous target window is always retained.
	TargetHistory int64

	// MaxSecondaryHistory caps the dictionary memory retained by each LZMA
	// secondary stream. A stream that refers further back fails with
	// ErrLimit.
	MaxSecondaryHistory int

	// ChainMemoryLimit is the largest intermediate result ApplyChain keeps
	// in memory before spilling to a temporary file.
	ChainMemoryLimit int64

	// Progress, if set, is called after each target window is written with
	// the total target bytes written and delta bytes consumed so far.
	Progress func(target, delta int64)
}

func (o *Options) withDefaults() Options {
	var r Options
	if o != nil {
		r = *o
	}
	if r.MaxWindowSize <= 0 {
		r.MaxWindowSize = DefaultMaxWindowSize
	}
	if r.SourceBlockSize <= 0 {
		r.SourceBlockSize = DefaultSourceBlockSize
	}
	if r.SourceCacheSize <= 0 {
		r.SourceCacheSize = DefaultSourceCacheSize
	}
	if r.MaxSecondaryHistory <= 0 {
		r.MaxSecondaryHistory = DefaultMaxSecondaryHistory
	}
	if r.ChainMemoryLimit <= 0 {
		r.ChainMemoryLimit = DefaultChainMemoryLimit
	}
	return r
}
