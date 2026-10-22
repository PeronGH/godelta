package godelta

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "regenerate testdata fixtures with xdelta3")

func newRand(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed*0x9E3779B97F4A7C15+1))
}

var words = []string{"alpha", "bravo", "charlie", "delta", "echo", "window", "source", "target", "copy", "add", "run", "\n", "\t", "0000", "ffff"}

// genData returns n bytes of compressible, structured pseudo-random data.
func genData(seed uint64, n int) []byte {
	r := newRand(seed)
	b := make([]byte, 0, n+64)
	for len(b) < n {
		switch r.IntN(10) {
		case 0:
			for range r.IntN(32) {
				b = append(b, byte(r.Uint32()))
			}
		case 1:
			// A short repeating pattern yields self-overlapping copies.
			pat := words[r.IntN(len(words))]
			pat = pat[:1+r.IntN(len(pat))]
			for range 20 + r.IntN(400) {
				b = append(b, pat...)
			}
		case 2:
			if len(b) > 16 {
				off := r.IntN(len(b) - 8)
				end := min(len(b), off+8+r.IntN(200))
				b = append(b, b[off:end]...)
			}
		default:
			b = append(b, words[r.IntN(len(words))]...)
			b = append(b, ' ')
		}
	}
	return b[:n]
}

// mutate derives a new version of old with insertions, deletions, moved
// blocks and byte edits, ending with old's tail so copies reach the source
// end.
func mutate(old []byte, seed uint64) []byte {
	r := newRand(seed)
	var b []byte
	pos := 0
	for pos < len(old) {
		n := min(len(old)-pos, 1+r.IntN(8000))
		chunk := bytes.Clone(old[pos : pos+n])
		for range r.IntN(4) {
			chunk[r.IntN(len(chunk))] ^= byte(1 + r.IntN(255))
		}
		b = append(b, chunk...)
		pos += n
		switch r.IntN(5) {
		case 0, 3:
			b = append(b, genData(r.Uint64(), r.IntN(6000))...)
		case 1:
			pos += r.IntN(2000)
		case 2:
			off := r.IntN(len(old))
			b = append(b, old[off:min(len(old), off+r.IntN(5000))]...)
		}
	}
	return append(b, old[max(0, len(old)-777):]...)
}

func xdelta3Path(t testing.TB) string {
	t.Helper()
	p, err := exec.LookPath("xdelta3")
	if err != nil {
		t.Skip("xdelta3 not installed")
	}
	return p
}

// xdelta3 runs the xdelta3 CLI with files in a temporary directory. Inputs
// maps file names to contents; the named output file is returned.
func xdelta3(t testing.TB, inputs map[string][]byte, output string, args ...string) []byte {
	t.Helper()
	bin := xdelta3Path(t)
	dir := t.TempDir()
	for name, data := range inputs {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bin, append([]string{"-f"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("xdelta3 %v: %v\n%s", args, err, out)
	}
	data, err := os.ReadFile(filepath.Join(dir, output))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeBytes(t testing.TB, src, delta []byte, opts *Options) ([]byte, error) {
	t.Helper()
	var out bytes.Buffer
	var ra *bytes.Reader
	if src != nil {
		ra = bytes.NewReader(src)
	}
	var err error
	var n int64
	if ra != nil {
		n, err = Decode(context.Background(), ra, int64(len(src)), bytes.NewReader(delta), &out, opts)
	} else {
		n, err = Decode(context.Background(), nil, 0, bytes.NewReader(delta), &out, opts)
	}
	if err == nil && n != int64(out.Len()) {
		return nil, fmt.Errorf("Decode returned %d, wrote %d", n, out.Len())
	}
	return out.Bytes(), err
}
