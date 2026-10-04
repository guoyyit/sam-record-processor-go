package samproc

import (
	"context"
	"path/filepath"
	"testing"
)

func writeSorted(t *testing.T, dir, name string, lines ...string) string {
	return writeSAM(t, dir, name, lines...)
}

func TestMergeOrderedKeepsEveryRecord(t *testing.T) {
	cfg, dir := testConfig(t, 1<<20, 1)
	a := writeSorted(t, dir, "a.sam",
		frag("a", 0, "chr1", 10, "4M", "AAAA", "IIII"),
		frag("c", 0, "chr1", 30, "4M", "CCCC", "IIII"),
	)
	b := writeSorted(t, dir, "b.sam",
		frag("b", 0, "chr1", 20, "4M", "GGGG", "IIII"),
	)
	out := filepath.Join(dir, "m.sam")
	if err := Merge(context.Background(), cfg, []string{a, b}, out); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	names := recordNames(readRecords(t, out))
	want := []string{"a", "b", "c"}
	if len(names) != len(want) {
		t.Fatalf("got %v want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v want %v", names, want)
		}
	}

	// Output must itself be a valid merge input (re-merge is a no-op).
	out2 := filepath.Join(dir, "m2.sam")
	if err := Merge(context.Background(), cfg, []string{out}, out2); err != nil {
		t.Fatalf("re-merge: %v", err)
	}
	if string(readAll(t, out)) != string(readAll(t, out2)) {
		t.Fatal("re-merge changed the output")
	}
}

func TestMergeByteIdenticalFromDistinctShards(t *testing.T) {
	// Distinct shards may legitimately contain the same bytes; merge
	// must not drop either occurrence.
	cfg, dir := testConfig(t, 1<<20, 1)
	line := frag("a", 0, "chr1", 10, "4M", "AAAA", "IIII")
	a := writeSorted(t, dir, "a.sam", line)
	b := writeSorted(t, dir, "b.sam", line)
	out := filepath.Join(dir, "m.sam")
	if err := Merge(context.Background(), cfg, []string{a, b}, out); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if got := len(readRecords(t, out)); got != 2 {
		t.Fatalf("got %d records, want 2 (no loss across shards)", got)
	}
}

func TestMergeRejectsUnsorted(t *testing.T) {
	cfg, dir := testConfig(t, 1<<20, 1)
	a := writeSorted(t, dir, "a.sam",
		frag("late", 0, "chr1", 90, "4M", "AAAA", "IIII"),
		frag("early", 0, "chr1", 10, "4M", "AAAA", "IIII"),
	)
	err := Merge(context.Background(), cfg, []string{a}, filepath.Join(dir, "o.sam"))
	if err == nil {
		t.Fatal("expected error for unsorted merge input")
	}
}
