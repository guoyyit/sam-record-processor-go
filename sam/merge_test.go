package sam

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// sortedShard writes records (already shuffled) to a sorted SAM file.
func sortedShard(t *testing.T, dir, name string, lines []string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	var b strings.Builder
	b.WriteString("@HD\tVN:1.6\tSO:coordinate\n@SQ\tSN:chr1\tLN:100000\n@SQ\tSN:chr2\tLN:100000\n")
	for _, ln := range lines {
		b.WriteString(ln + "\n")
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	// Sort the records in place via SortFile to guarantee valid order.
	out := p + ".sorted"
	if err := SortFile(p, out, SortOptions{}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMergeBasic(t *testing.T) {
	dir := t.TempDir()
	r := rand.New(rand.NewSource(3))
	var all []string
	var shards []string
	for s := 0; s < 4; s++ {
		var lines []string
		for i := 0; i < 50; i++ {
			ln := fmt.Sprintf("s%dr%d\t%d\t%s\t%d\t60\t40M\t*\t0\t0\t%s\t%s",
				s, i, []int{0, 16}[r.Intn(2)], []string{"chr1", "chr2"}[r.Intn(2)],
				1+r.Intn(90000), strings.Repeat("C", 40), strings.Repeat("I", 40))
			lines = append(lines, ln)
			all = append(all, ln)
		}
		shards = append(shards, sortedShard(t, dir, fmt.Sprintf("shard%d.sam", s), lines))
	}
	out := filepath.Join(dir, "merged.sam")
	if err := MergeFiles(shards, out, MergeOptions{MemoryLimit: 4 << 10}); err != nil {
		t.Fatal(err)
	}
	assertSorted(t, out)
	_, recs := readRecords(t, out)
	if len(recs) != len(all) {
		t.Fatalf("got %d records, want %d", len(recs), len(all))
	}
	// No record may be lost, duplicated or rewritten.
	got := make([]string, 0, len(recs))
	for _, rec := range recs {
		got = append(got, rec.Line())
	}
	sort.Strings(got)
	sort.Strings(all)
	for i := range all {
		if got[i] != all[i] {
			t.Fatalf("record mismatch at %d:\n got %q\nwant %q", i, got[i], all[i])
		}
	}
	// Header carries the union of references and coordinate sort order.
	h, _ := readRecords(t, out)
	if !h.HasRef("chr1") || !h.HasRef("chr2") {
		t.Fatalf("merged header refs: %v", h.Refs)
	}
	if !strings.Contains(h.Lines[0], "SO:coordinate") {
		t.Fatalf("merged header @HD: %q", h.Lines[0])
	}

	// The merged output must be usable as input to a further merge.
	out2 := filepath.Join(dir, "merged2.sam")
	if err := MergeFiles([]string{out, shards[0]}, out2, MergeOptions{}); err != nil {
		t.Fatal(err)
	}
	assertSorted(t, out2)
	_, recs2 := readRecords(t, out2)
	if len(recs2) != len(all)+50 {
		t.Fatalf("re-merge got %d records", len(recs2))
	}
}

func TestMergeRejectsUnsorted(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.sam")
	content := "@HD\tVN:1.6\n@SQ\tSN:chr1\tLN:1000\n" +
		"r1\t0\tchr1\t100\t60\t40M\t*\t0\t0\t" + strings.Repeat("A", 40) + "\t" + strings.Repeat("I", 40) + "\n" +
		"r2\t0\tchr1\t50\t60\t40M\t*\t0\t0\t" + strings.Repeat("A", 40) + "\t" + strings.Repeat("I", 40) + "\n"
	if err := os.WriteFile(bad, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.sam")
	err := MergeFiles([]string{bad}, out, MergeOptions{})
	if err == nil || !strings.Contains(err.Error(), "not sorted") {
		t.Fatalf("expected not-sorted error, got %v", err)
	}
}

func TestMergeResume(t *testing.T) {
	dir := t.TempDir()
	r := rand.New(rand.NewSource(5))
	var shards []string
	for s := 0; s < 6; s++ {
		var lines []string
		for i := 0; i < 60; i++ {
			lines = append(lines, fmt.Sprintf("s%dr%d\t0\tchr1\t%d\t60\t40M\t*\t0\t0\t%s\t%s",
				s, i, 1+r.Intn(90000), strings.Repeat("G", 40), strings.Repeat("I", 40)))
		}
		shards = append(shards, sortedShard(t, dir, fmt.Sprintf("shard%d.sam", s), lines))
	}
	// Force multiple merge passes with a tiny memory budget (fan-in 2).
	cfg := MergeOptions{MemoryLimit: 4 << 10, Threads: 2}
	ref := filepath.Join(dir, "ref.sam")
	if err := MergeFiles(shards, ref, cfg); err != nil {
		t.Fatal(err)
	}
	refData, err := os.ReadFile(ref)
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out.sam")
	crashCfg := cfg
	crashCfg.WorkDir = filepath.Join(dir, "work")
	t.Setenv("SAMPROC_FAIL_AFTER_RUNS", "1")
	if err := MergeFiles(shards, out, crashCfg); err == nil {
		t.Fatal("expected injected failure")
	}
	t.Setenv("SAMPROC_FAIL_AFTER_RUNS", "")
	if err := MergeFiles(shards, out, crashCfg); err != nil {
		t.Fatalf("resume: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(refData) {
		t.Fatal("resumed merge differs from uninterrupted merge")
	}
}
