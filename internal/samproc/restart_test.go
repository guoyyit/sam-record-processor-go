package samproc

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func genLines(n int) []string {
	lines := make([]string, n)
	for i := 0; i < n; i++ {
		ref := "chr1"
		if i%2 == 0 {
			ref = "chr2"
		}
		lines[i] = frag("q"+itoaUint(uint64(i)), 0, ref, 1+(i*13)%400,
			"4M", "ACGT", "IIII")
	}
	return lines
}

func TestSortRestartMatchesOneShot(t *testing.T) {
	lines := genLines(200)

	// Reference: one-shot run.
	refCfg, refDir := testConfig(t, 3000, 2)
	refIn := writeSAM(t, refDir, "in.sam", lines...)
	refOut := filepath.Join(refDir, "ref.sam")
	mustSort(t, refCfg, refOut, refIn)
	want := readAll(t, refOut)

	// Failing run: small chunk budget, reuse the same work dir.
	cfg, dir := testConfig(t, 3000, 2)
	in := writeSAM(t, dir, "in.sam", lines...)
	out := filepath.Join(dir, "out.sam")

	injectSortFailChunks = 2
	err1 := Sort(context.Background(), cfg, []string{in}, out)
	injectSortFailChunks = 0
	if err1 == nil {
		t.Fatal("expected injected failure on first run")
	}

	// Completed chunks must remain as valid, self-contained files.
	entries := listChunkFiles(t, cfg.TempDir)
	if len(entries) < 2 {
		t.Fatalf("expected durable chunks, got %d", len(entries))
	}

	// Restart and compare byte-for-byte.
	if err := Sort(context.Background(), cfg, []string{in}, out); err != nil {
		t.Fatalf("restart Sort: %v", err)
	}
	got := readAll(t, out)
	if string(got) != string(want) {
		t.Fatal("restarted sort differs from one-shot sort")
	}
	// A second restart must be a no-op and byte-identical.
	if err := Sort(context.Background(), cfg, []string{in}, out); err != nil {
		t.Fatalf("second Sort: %v", err)
	}
	if string(readAll(t, out)) != string(want) {
		t.Fatal("second sort differs")
	}
}

func TestMergeRestartMatchesOneShot(t *testing.T) {
	lines := genLines(200)
	cfgRef, d0 := testConfig(t, 4000, 1)
	in0 := writeSAM(t, d0, "in.sam", lines...)
	sorted0 := filepath.Join(d0, "sorted.sam")
	mustSort(t, cfgRef, sorted0, in0)

	// Split the sorted file into two sorted halves by coordinate
	// (they are already ordered, so a byte split keeps each half
	// sorted).
	cfg, dir := testConfig(t, 4000, 1)
	a := writeSorted(t, dir, "a.sam", readRecords(t, sorted0)[:100]...)
	b := writeSorted(t, dir, "b.sam", readRecords(t, sorted0)[100:]...)
	out := filepath.Join(dir, "m.sam")

	injectCheckpointBytes = 400 // force fsync boundaries mid-run
	injectMergeFailLines = 30
	err1 := Merge(context.Background(), cfg, []string{a, b}, out)
	injectMergeFailLines = 0
	injectCheckpointBytes = 0
	if err1 == nil {
		t.Fatal("expected injected merge failure")
	}

	if err := Merge(context.Background(), cfg, []string{a, b}, out); err != nil {
		t.Fatalf("restart Merge: %v", err)
	}
	wantL := readRecords(t, sorted0)
	gotL := readRecords(t, out)
	if len(wantL) != len(gotL) {
		t.Fatalf("len want %d got %d", len(wantL), len(gotL))
	}
	for i := range wantL {
		if wantL[i] != gotL[i] {
			t.Fatalf("diff at %d\n want %s\n got  %s", i, wantL[i], gotL[i])
		}
	}
	if err := Merge(context.Background(), cfg, []string{a, b}, out); err != nil {
		t.Fatalf("second Merge: %v", err)
	}
	if string(readAll(t, out)) != string(readAll(t, sorted0)) {
		t.Fatal("second merge differs")
	}
}

func listChunkFiles(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := walkRunFiles(root, func(p string) {
		paths = append(paths, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func walkRunFiles(root string, fn func(string)) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), chunkSuffix) {
			fn(path)
		}
		return nil
	})
}
