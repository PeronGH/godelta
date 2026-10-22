package lzma

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"os/exec"
	"testing"
)

func testData(n int, seed uint64) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
	words := [][]byte{[]byte("delta "), []byte("window "), []byte("source "), []byte("\x00\x00\x00\x00"), []byte("target\n")}
	var b bytes.Buffer
	for b.Len() < n {
		switch r.IntN(4) {
		case 0:
			for range r.IntN(64) {
				b.WriteByte(byte(r.Uint32()))
			}
		case 1:
			if b.Len() > 0 {
				off := r.IntN(b.Len())
				end := min(b.Len(), off+r.IntN(300))
				b.Write(bytes.Clone(b.Bytes()[off:end]))
			}
		default:
			b.Write(words[r.IntN(len(words))])
		}
	}
	return b.Bytes()[:n]
}

func xzCompress(t *testing.T, data []byte, args ...string) []byte {
	t.Helper()
	path, err := exec.LookPath("xz")
	if err != nil {
		t.Skip("xz not installed")
	}
	cmd := exec.Command(path, append([]string{"--format=xz", "--check=none", "-c"}, args...)...)
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("xz: %v", err)
	}
	return out
}

func TestXZAgainstCLI(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		args []string
	}{
		{"text-6", testData(3<<20, 1), []string{"-6"}},
		{"text-0", testData(1<<20, 2), []string{"-0"}},
		{"text-9e", testData(2<<20, 3), []string{"-9e"}},
		{"lc4", testData(1<<20, 4), []string{"--lzma2=preset=6,lc=4,lp=0,pb=0"}},
		{"lp2", testData(1<<20, 5), []string{"--lzma2=preset=6,lc=1,lp=2,pb=3"}},
		{"random", testData(0, 0), []string{"-6"}},
	}
	r := rand.New(rand.NewPCG(7, 7))
	cases[len(cases)-1].data = make([]byte, 300_000)
	for i := range cases[len(cases)-1].data {
		cases[len(cases)-1].data[i] = byte(r.Uint32())
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comp := xzCompress(t, tc.data, tc.args...)
			for _, limit := range []int{64 << 20, minDictAlloc} {
				x := NewXZDecoder(limit)
				n, err := x.readHeaders(comp)
				if err != nil {
					t.Fatal(err)
				}
				out := make([]byte, len(tc.data))
				m, err := x.lz.Decode(comp[n:], out)
				if limit == minDictAlloc && len(tc.data) > limit {
					if err == nil {
						if !bytes.Equal(out, tc.data) {
							t.Fatal("output mismatch with small history")
						}
					} else if !errors.Is(err, ErrLimit) {
						t.Fatalf("small history: %v", err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(out, tc.data) {
					t.Fatal("output mismatch")
				}
				if comp[n+m] != 0 {
					t.Fatalf("expected end marker, got %#x", comp[n+m])
				}
			}
		})
	}
}

// TestPiecewise splits the decoded output on chunk boundaries and feeds the
// chunks in separate calls, as the persistent secondary streams do.
func TestPiecewise(t *testing.T) {
	data := testData(5<<20, 9)
	comp := xzCompress(t, data, "-6", "--block-size=100MiB")
	x := NewXZDecoder(64 << 20)
	n, err := x.readHeaders(comp)
	if err != nil {
		t.Fatal(err)
	}
	x.headerDone = true
	body := comp[n:]
	var got []byte
	for body[0] != 0 {
		clen, ulen := chunkSizes(body)
		out := make([]byte, ulen)
		if err := x.Decode(body[:clen], out); err != nil {
			t.Fatal(err)
		}
		got = append(got, out...)
		body = body[clen:]
	}
	if !bytes.Equal(got, data) {
		t.Fatal("output mismatch")
	}
}

func chunkSizes(p []byte) (clen, ulen int) {
	c := p[0]
	if c < 0x80 {
		u := int(p[1])<<8 | int(p[2]) + 1
		return 3 + u, u
	}
	u := int(c&0x1F)<<16 | int(p[1])<<8 | int(p[2]) + 1
	cs := int(p[3])<<8 | int(p[4]) + 1
	h := 5
	if c >= 0xC0 {
		h = 6
	}
	return h + cs, u
}

func FuzzXZ(f *testing.F) {
	f.Add([]byte{}, uint16(10))
	f.Fuzz(func(t *testing.T, in []byte, n uint16) {
		x := NewXZDecoder(1 << 20)
		_ = x.Decode(in, make([]byte, int(n)+1))
		_ = x.Decode(in, make([]byte, int(n)+1))
	})
}
