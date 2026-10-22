# godelta

Pure-Go decoder for VCDIFF (RFC 3284) deltas, including xdelta3's extensions: LZMA secondary compression, Adler-32 window checksums and application headers. It also inspects deltas, applies delta chains, and merges chains into a single delta.

- Streams the delta and the target; reads only the source blocks it needs through `io.ReaderAt`.
- Bounded memory, 64-bit sizes, `context` cancellation, typed errors.
- No cgo, no dependencies.

## Usage

```sh
go get github.com/PeronGH/godelta
```

```go
src, _ := os.Open("old.bin")
defer src.Close()
fi, _ := src.Stat()
delta, _ := os.Open("patch.vcdiff")
defer delta.Close()
out, _ := os.Create("new.bin")
defer out.Close()

n, err := godelta.Decode(ctx, src, fi.Size(), delta, out, nil)
if errors.Is(err, godelta.ErrChecksum) {
	// corrupt patch or wrong source
}
```

See the package documentation for `Inspect`, `ApplyChain`, `Merge` and `Options`.

## License

Apache-2.0. Parts are derived from [xdelta3](https://github.com/jmacd/xdelta); see NOTICE.
