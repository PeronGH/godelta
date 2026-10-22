// Package godelta decodes VCDIFF (RFC 3284) deltas, including those
// produced by xdelta3 with its extensions: LZMA secondary compression,
// per-window Adler-32 checksums and application headers. It can also
// inspect deltas without decoding them, apply chains of deltas, and merge a
// chain into a single delta.
//
// The delta is read forward-only in a single pass and the target is written
// window by window, so neither needs to be seekable. The source is an
// io.ReaderAt; only the blocks that windows copy from are read.
//
// # Memory
//
// A single decode holds roughly:
//
//   - the current target window plus retained target history: by default
//     the previous window, so about 2 × the window size;
//   - cached source blocks, at most Options.SourceCacheSize (32 MiB by
//     default);
//   - the window's raw and decompressed sections, usually smaller than the
//     target window and never more than about 3 × its size each;
//   - one LZMA dictionary per compressed section type (at most three). Each
//     grows only as data is decompressed and is capped at the smaller of the
//     declared dictionary size and Options.MaxSecondaryHistory.
//
// For xdelta3's default 8 MiB windows this stays well under 128 MiB.
// Lengths read from the delta are capped by Options.MaxWindowSize, and
// buffers grow with the data actually read, so a corrupt delta cannot force
// a huge allocation.
//
// Merge is the exception: like xdelta3 merge, it keeps the instructions and
// added data of every input delta in memory.
//
// # Unsupported features
//
// Custom code tables (VCD_CODETABLE) and the DJW and FGK secondary
// compressors are reported as ErrUnsupported.
package godelta
