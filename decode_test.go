package godelta

import (
	"bytes"
	"context"
	"errors"
	"hash/adler32"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type synthWindow struct {
	enc      windowEncoder
	checksum bool
	target   []byte
}

func buildDelta(wins ...*synthWindow) []byte {
	var b bytes.Buffer
	if err := writeFileHeader(&b, nil); err != nil {
		panic(err)
	}
	for _, w := range wins {
		if w.checksum {
			w.enc.adler = adler32.Checksum(w.target)
		}
		b.Write(appendWindow(nil, &w.enc, w.checksum))
	}
	return b.Bytes()
}

func TestTargetWindows(t *testing.T) {
	w1 := &synthWindow{checksum: true, target: []byte("hello, vcdiff world")}
	w1.enc.reset(0, 0, 0)
	w1.enc.add(w1.target)

	// The second window copies "vcdiff" and "hello" from the first window.
	w2 := &synthWindow{checksum: true, target: []byte("vcdiff says hello")}
	w2.enc.reset(winTarget, 0, int64(len(w1.target)))
	w2.enc.copy(7, 6)
	w2.enc.add([]byte(" says "))
	w2.enc.copy(0, 5)

	// The third window copies from the first one, two windows back.
	w3 := &synthWindow{checksum: true, target: []byte("world")}
	w3.enc.reset(winTarget, 14, 5)
	w3.enc.copy(0, 5)

	delta := buildDelta(w1, w2, w3)
	want := "hello, vcdiff worldvcdiff says helloworld"

	got, err := decodeBytes(t, nil, delta, &Options{TargetHistory: 1 << 20})
	if err != nil || string(got) != want {
		t.Fatalf("got %q, %v", got, err)
	}
	// By default only the previous window is retained.
	if _, err := decodeBytes(t, nil, delta, nil); !errors.Is(err, ErrLimit) {
		t.Fatalf("err = %v, want ErrLimit", err)
	}
}

func TestOverlappingAndSpanningCopies(t *testing.T) {
	src := []byte("0123456789")
	w := &synthWindow{checksum: true}
	w.enc.reset(winSource, 6, 4) // segment "6789"
	w.enc.copy(2, 4)             // "89" then the first two target bytes "89"
	w.enc.add([]byte("ab"))
	w.enc.copy(4+4, 10) // overlapping run of "ab"
	w.target = []byte("8989abababababab")
	got, err := decodeBytes(t, src, buildDelta(w), nil)
	if err != nil || !bytes.Equal(got, w.target) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestErrors(t *testing.T) {
	delta, err := os.ReadFile(filepath.Join("testdata", "lzma-single.vcdiff"))
	if err != nil {
		t.Fatal(err)
	}
	f := fixtures[0]
	old, new := f.data()

	if _, err := decodeBytes(t, old[:len(old)/2], delta, nil); !errors.Is(err, ErrSourceRange) {
		t.Errorf("short source: err = %v", err)
	}
	if _, err := decodeBytes(t, nil, delta, nil); !errors.Is(err, ErrSourceRange) {
		t.Errorf("no source: err = %v", err)
	}
	// A source ReaderAt that is shorter than the declared size.
	var out bytes.Buffer
	_, err = Decode(context.Background(), bytes.NewReader(old[:100]), int64(len(old)), bytes.NewReader(delta), &out, nil)
	if !errors.Is(err, ErrSourceRange) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("lying source size: err = %v", err)
	}
	if _, err := decodeBytes(t, old, delta[:len(delta)-1], nil); !errors.Is(err, ErrCorrupt) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated delta: err = %v", err)
	}
	if _, err := decodeBytes(t, old, delta, &Options{MaxWindowSize: 1000}); !errors.Is(err, ErrLimit) {
		t.Errorf("window limit: err = %v", err)
	}

	// A wrong checksum is detected unless checks are skipped.
	info, err := Inspect(bytes.NewReader(delta))
	if err != nil {
		t.Fatal(err)
	}
	w := info.Windows[0]
	end := int64(len(delta))
	if len(info.Windows) > 1 {
		end = info.Windows[1].DeltaOffset
	}
	// The checksum immediately precedes the sections.
	sum := end - w.DataLength - w.InstLength - w.AddrLength - 4
	bad := bytes.Clone(delta)
	bad[sum] ^= 1
	_, err = decodeBytes(t, old, bad, nil)
	var e *Error
	if !errors.Is(err, ErrChecksum) || !errors.As(err, &e) || e.Window != 0 {
		t.Errorf("bad checksum: err = %v", err)
	}
	if got, err := decodeBytes(t, old, bad, &Options{SkipChecksum: true}); err != nil || !bytes.Equal(got, new) {
		t.Errorf("SkipChecksum: %v", err)
	}
}

func TestCancel(t *testing.T) {
	f := fixtures[1]
	old, _ := f.data()
	delta, err := os.ReadFile(f.path())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	windows := 0
	opts := &Options{Progress: func(target, delta int64) {
		windows++
		if windows == 2 {
			cancel()
		}
	}}
	n, err := Decode(ctx, bytes.NewReader(old), int64(len(old)), bytes.NewReader(delta), io.Discard, opts)
	if !errors.Is(err, context.Canceled) || windows != 2 || n == 0 {
		t.Fatalf("n = %d, windows = %d, err = %v", n, windows, err)
	}
}

func TestProgress(t *testing.T) {
	f := fixtures[1]
	old, new := f.data()
	delta, err := os.ReadFile(f.path())
	if err != nil {
		t.Fatal(err)
	}
	var lastT, lastD int64
	opts := &Options{Progress: func(target, d int64) {
		if target <= lastT || d <= lastD {
			t.Errorf("progress not increasing: %d,%d after %d,%d", target, d, lastT, lastD)
		}
		lastT, lastD = target, d
	}}
	if _, err := decodeBytes(t, old, delta, opts); err != nil {
		t.Fatal(err)
	}
	if lastT != int64(len(new)) || lastD != int64(len(delta)) {
		t.Fatalf("final progress %d,%d, want %d,%d", lastT, lastD, len(new), len(delta))
	}
}

// TestCorruption flips bytes throughout the fixtures. Decoding must either
// succeed or fail with one of the package's errors, and never panic.
func TestCorruption(t *testing.T) {
	sentinels := []error{ErrCorrupt, ErrChecksum, ErrSourceRange, ErrUnsupported, ErrLimit}
	for _, f := range fixtures {
		delta, err := os.ReadFile(f.path())
		if err != nil {
			t.Fatal(err)
		}
		old, _ := f.data()
		step := max(1, len(delta)/300)
		for i := 0; i < len(delta); i += step {
			for _, flip := range []byte{0x01, 0x80, 0xFF} {
				bad := bytes.Clone(delta)
				bad[i] ^= flip
				_, err := decodeBytes(t, old, bad, &Options{MaxWindowSize: 1 << 20})
				if err == nil {
					continue
				}
				matched := false
				for _, s := range sentinels {
					matched = matched || errors.Is(err, s)
				}
				if !matched {
					t.Fatalf("%s: flip %#x at %d: untyped error %v", f.name, flip, i, err)
				}
				if _, err := Inspect(bytes.NewReader(bad)); err != nil && !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrUnsupported) {
					t.Fatalf("%s: Inspect: flip %#x at %d: %v", f.name, flip, i, err)
				}
			}
		}
	}
}

func TestConcurrentDecode(t *testing.T) {
	var wg sync.WaitGroup
	for _, f := range fixtures {
		if f.err != nil {
			continue
		}
		delta, err := os.ReadFile(f.path())
		if err != nil {
			t.Fatal(err)
		}
		old, new := f.data()
		if f.noSource {
			old = nil
		}
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, err := decodeBytes(t, old, delta, &Options{SourceBlockSize: 4096, SourceCacheSize: 16384})
				if err != nil || !bytes.Equal(got, new) {
					t.Errorf("%s: %v", f.name, err)
				}
			}()
		}
	}
	wg.Wait()
}

// TestLargeTarget decodes a synthetic delta whose target exceeds 4 GiB.
func TestLargeTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	const winSize = 64 << 20
	const windows = 4<<30/winSize + 2
	run := bytes.Repeat([]byte{0x5A}, winSize)
	sum := adler32.Checksum(run)
	var delta bytes.Buffer
	if err := writeFileHeader(&delta, nil); err != nil {
		t.Fatal(err)
	}
	var e windowEncoder
	for range windows {
		e.reset(0, 0, 0)
		e.run(0x5A, winSize)
		e.adler = sum
		delta.Write(appendWindow(nil, &e, true))
	}
	var last int64
	n, err := Decode(context.Background(), nil, 0, &delta, io.Discard, &Options{Progress: func(target, _ int64) { last = target }})
	if err != nil || n != windows*winSize || last != n {
		t.Fatalf("n = %d, progress = %d, err = %v", n, last, err)
	}
}
