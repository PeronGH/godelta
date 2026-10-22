package godelta

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fixture struct {
	name     string
	size     int
	noSource bool
	args     []string
	appHdr   string
	err      error
}

var fixtures = []fixture{
	{name: "lzma-single", size: 300_000, args: []string{"-9", "-S", "lzma", "-A=godelta"}, appHdr: "godelta"},
	{name: "lzma-multi", size: 400_000, args: []string{"-9", "-S", "lzma", "-W", "16384", "-A="}},
	{name: "lzma-nosource", size: 200_000, noSource: true, args: []string{"-9", "-S", "lzma", "-W", "16384", "-A="}},
	{name: "lzma-level1", size: 300_000, args: []string{"-1", "-S", "lzma", "-W", "65536", "-A="}},
	{name: "none-multi", size: 200_000, args: []string{"-9", "-S", "none", "-W", "16384", "-A="}},
	{name: "nochecksum", size: 200_000, args: []string{"-9", "-n", "-S", "lzma", "-W", "16384", "-A="}},
	{name: "djw", size: 100_000, args: []string{"-9", "-S", "djw", "-A="}, err: ErrUnsupported},
}

func (f *fixture) data() (old, new []byte) {
	old = genData(uint64(f.size), f.size)
	return old, mutate(old, uint64(f.size)+1)
}

func (f *fixture) path() string { return filepath.Join("testdata", f.name+".vcdiff") }

func (f *fixture) delta(t *testing.T) []byte {
	t.Helper()
	if *update {
		old, new := f.data()
		args := append([]string{"-e"}, f.args...)
		inputs := map[string][]byte{"new": new}
		if !f.noSource {
			inputs["old"] = old
			args = append(args, "-s", "old")
		}
		d := xdelta3(t, inputs, "out", append(args, "new", "out")...)
		if err := os.WriteFile(f.path(), d, 0o644); err != nil {
			t.Fatal(err)
		}
		return d
	}
	d, err := os.ReadFile(f.path())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFixtures(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			delta := f.delta(t)
			old, new := f.data()
			if f.noSource {
				old = nil
			}
			info, err := Inspect(bytes.NewReader(delta))
			if err != nil {
				t.Fatal(err)
			}
			if info.TargetSize() != int64(len(new)) {
				t.Errorf("TargetSize = %d, want %d", info.TargetSize(), len(new))
			}
			if info.SourceSize() > int64(len(old)) {
				t.Errorf("SourceSize = %d, source has %d", info.SourceSize(), len(old))
			}
			if string(info.AppHeader) != f.appHdr {
				t.Errorf("AppHeader = %q, want %q", info.AppHeader, f.appHdr)
			}

			got, err := decodeBytes(t, old, delta, nil)
			if f.err != nil {
				if !errors.Is(err, f.err) {
					t.Fatalf("err = %v, want %v", err, f.err)
				}
				var e *Error
				if !errors.As(err, &e) || e.Feature == "" {
					t.Fatalf("err %v lacks a feature name", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, new) {
				t.Fatalf("decoded %d bytes, want %d; content mismatch", len(got), len(new))
			}
		})
	}
}

// TestLZMAContinuation checks that the multi-window fixtures exercise LZMA
// streams that continue across windows.
func TestLZMAContinuation(t *testing.T) {
	for _, name := range []string{"lzma-multi", "lzma-nosource"} {
		d, err := os.ReadFile(filepath.Join("testdata", name+".vcdiff"))
		if err != nil {
			t.Fatal(err)
		}
		info, err := Inspect(bytes.NewReader(d))
		if err != nil {
			t.Fatal(err)
		}
		var data, inst, addr int
		for _, w := range info.Windows {
			if w.DataCompressed {
				data++
			}
			if w.InstCompressed {
				inst++
			}
			if w.AddrCompressed {
				addr++
			}
		}
		if len(info.Windows) < 2 || max(data, inst, addr) < 2 {
			t.Errorf("%s: %d windows, compressed sections data=%d inst=%d addr=%d", name, len(info.Windows), data, inst, addr)
		}
	}
}
