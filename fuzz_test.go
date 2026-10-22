package godelta

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
)

func addFixtureSeeds(f *testing.F, withSource bool) {
	for _, fx := range fixtures {
		d, err := os.ReadFile(fx.path())
		if err != nil {
			f.Fatal(err)
		}
		if withSource {
			old, _ := fx.data()
			f.Add(d, old[:min(len(old), 4096)])
		} else {
			f.Add(d)
		}
	}
}

func FuzzDecode(f *testing.F) {
	addFixtureSeeds(f, true)
	sentinels := []error{ErrCorrupt, ErrChecksum, ErrSourceRange, ErrUnsupported, ErrLimit}
	f.Fuzz(func(t *testing.T, delta, src []byte) {
		opts := &Options{MaxWindowSize: 1 << 20, SourceBlockSize: 1024, TargetHistory: 1 << 16}
		_, err := Decode(context.Background(), bytes.NewReader(src), int64(len(src)), bytes.NewReader(delta), io.Discard, opts)
		if err == nil {
			return
		}
		for _, s := range sentinels {
			if errors.Is(err, s) {
				return
			}
		}
		t.Fatalf("untyped error: %v", err)
	})
}

func FuzzInspect(f *testing.F) {
	addFixtureSeeds(f, false)
	f.Fuzz(func(t *testing.T, delta []byte) {
		info, err := Inspect(bytes.NewReader(delta))
		if err != nil {
			if !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrUnsupported) {
				t.Fatalf("untyped error: %v", err)
			}
			return
		}
		_ = info.TargetSize()
		_ = info.SourceSize()
	})
}

func FuzzMerge(f *testing.F) {
	addFixtureSeeds(f, false)
	f.Fuzz(func(t *testing.T, delta []byte) {
		opts := &Options{MaxWindowSize: 1 << 20}
		var out bytes.Buffer
		if err := Merge(context.Background(), []io.Reader{bytes.NewReader(delta), bytes.NewReader(delta)}, &out, opts); err != nil {
			return
		}
		if _, err := Inspect(&out); err != nil {
			t.Fatalf("Merge produced an unreadable delta: %v", err)
		}
	})
}
