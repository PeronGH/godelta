package godelta

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// benchSetup writes a 32 MiB source, its mutated target and an xdelta3
// delta between them to a temporary directory.
func benchSetup(b *testing.B) (dir string, old, delta []byte, size int64) {
	b.Helper()
	old = genData(1, 32<<20)
	new := mutate(old, 2)
	dir = b.TempDir()
	delta = xdelta3(b, map[string][]byte{"old": old, "new": new}, "out", "-e", "-9", "-S", "lzma", "-s", "old", "new", "out")
	for name, data := range map[string][]byte{"old": old, "delta": delta} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return dir, old, delta, int64(len(new))
}

// BenchmarkDecode compares this package with the xdelta3 CLI on the same
// delta; both read the source from a file.
func BenchmarkDecode(b *testing.B) {
	dir, _, delta, size := benchSetup(b)
	b.Run("godelta", func(b *testing.B) {
		src, err := os.Open(filepath.Join(dir, "old"))
		if err != nil {
			b.Fatal(err)
		}
		defer src.Close()
		fi, _ := src.Stat()
		b.SetBytes(size)
		for range b.N {
			if _, err := Decode(context.Background(), src, fi.Size(), bytes.NewReader(delta), io.Discard, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("xdelta3", func(b *testing.B) {
		bin := xdelta3Path(b)
		b.SetBytes(size)
		for range b.N {
			cmd := exec.Command(bin, "-d", "-c", "-s", "old", "delta")
			cmd.Dir = dir
			cmd.Stdout = io.Discard
			if err := cmd.Run(); err != nil {
				b.Fatal(err)
			}
		}
	})
}
