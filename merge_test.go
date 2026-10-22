package godelta

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
)

func TestMerge(t *testing.T) {
	versions := [][]byte{genData(100, 600_000)}
	for i := range 3 {
		versions = append(versions, mutate(versions[i], uint64(200+i)))
	}
	for _, args := range [][]string{
		{"-9", "-S", "lzma", "-W", "65536"},
		{"-1", "-S", "none", "-W", "16384", "-n"},
	} {
		var deltas [][]byte
		var mergeArgs []string
		inputs := map[string][]byte{}
		for i := 1; i < len(versions); i++ {
			d := xdelta3(t, map[string][]byte{"old": versions[i-1], "new": versions[i]}, "out",
				append(append([]string{"-e"}, args...), "-s", "old", "new", "out")...)
			deltas = append(deltas, d)
			name := "d" + string(rune('0'+i))
			inputs[name] = d
			if i < len(versions)-1 {
				mergeArgs = append(mergeArgs, "-m", name)
			}
		}
		want := versions[len(versions)-1]

		var readers []io.Reader
		for _, d := range deltas {
			readers = append(readers, bytes.NewReader(d))
		}
		var merged bytes.Buffer
		if err := Merge(context.Background(), readers, &merged, nil); err != nil {
			t.Fatal(err)
		}
		got, err := decodeBytes(t, versions[0], merged.Bytes(), nil)
		if err != nil {
			t.Fatalf("%v: decoding merged delta: %v", args, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%v: merged delta decodes to wrong output", args)
		}

		for _, limit := range []int64{1000, 0} {
			dir := t.TempDir()
			var readers []io.Reader
			for _, d := range deltas {
				readers = append(readers, bytes.NewReader(d))
			}
			var out bytes.Buffer
			n, err := ApplyChain(context.Background(), bytes.NewReader(versions[0]), int64(len(versions[0])), readers, &out, dir, &Options{ChainMemoryLimit: limit})
			if err != nil || n != int64(len(want)) || !bytes.Equal(out.Bytes(), want) {
				t.Fatalf("ApplyChain(limit %d) = %d, %v", limit, n, err)
			}
			if left, _ := os.ReadDir(dir); len(left) != 0 {
				t.Fatalf("ApplyChain left %d temporary files", len(left))
			}
		}

		// xdelta3 must decode our merged delta.
		inputs["src"] = versions[0]
		inputs["merged"] = merged.Bytes()
		if out := xdelta3(t, inputs, "out", "-d", "-s", "src", "merged", "out"); !bytes.Equal(out, want) {
			t.Fatalf("%v: xdelta3 decodes our merged delta to wrong output", args)
		}

		// We must decode xdelta3's merged delta.
		x := xdelta3(t, inputs, "xmerged", append(append([]string{"merge"}, mergeArgs...), "d3", "xmerged")...)
		if got, err := decodeBytes(t, versions[0], x, nil); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%v: decoding xdelta3 merge output: %v", args, err)
		}
		t.Logf("%v: deltas %d+%d+%d bytes, merged %d bytes, xdelta3 merged %d bytes",
			args, len(deltas[0]), len(deltas[1]), len(deltas[2]), merged.Len(), len(x))
	}
}
