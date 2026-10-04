package sam

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dedupRun(t *testing.T, records []string) []*Record {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "in.sam")
	var b strings.Builder
	b.WriteString("@HD\tVN:1.6\tSO:coordinate\n@SQ\tSN:chr1\tLN:100000\n@SQ\tSN:chr2\tLN:100000\n")
	for _, ln := range records {
		b.WriteString(ln + "\n")
	}
	if err := os.WriteFile(in, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.sam")
	if err := DedupFile(in, out); err != nil {
		t.Fatal(err)
	}
	_, recs := readRecords(t, out)
	return recs
}

func isDup(r *Record) bool { return r.Flag&FlagDuplicate != 0 }

func TestDedupPaired(t *testing.T) {
	// Two read pairs with identical both-end coordinates and orientations.
	// The pair with the higher quality sum must be kept.
	// Input is coordinate-sorted, as required by DedupFile.
	recs := dedupRun(t, []string{
		"keep/1\t99\tchr1\t100\t60\t50M\t=\t300\t200\t" + strings.Repeat("A", 50) + "\t" + strings.Repeat("I", 50),
		"dup/1\t99\tchr1\t100\t60\t50M\t=\t300\t200\t" + strings.Repeat("A", 50) + "\t" + strings.Repeat("5", 50),
		"keep/2\t147\tchr1\t300\t60\t50M\t=\t100\t-200\t" + strings.Repeat("T", 50) + "\t" + strings.Repeat("I", 50),
		"dup/2\t147\tchr1\t300\t60\t50M\t=\t100\t-200\t" + strings.Repeat("T", 50) + "\t" + strings.Repeat("5", 50),
	})
	if len(recs) != 4 {
		t.Fatalf("got %d records", len(recs))
	}
	for _, r := range recs {
		wantDup := strings.HasPrefix(r.Name, "dup/")
		if isDup(r) != wantDup {
			t.Fatalf("%s: dup=%v, want %v", r.Name, isDup(r), wantDup)
		}
	}
}

func TestDedupSingleEndAndOrientation(t *testing.T) {
	recs := dedupRun(t, []string{
		"a\t0\tchr1\t100\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII",
		"b\t0\tchr1\t100\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII",
		"c\t16\tchr1\t100\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII", // reverse: distinct
		"d\t0\tchr1\t105\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII",  // other pos: distinct
	})
	if isDup(recs[0]) && isDup(recs[1]) {
		t.Fatal("both forward duplicates marked")
	}
	if !isDup(recs[0]) && !isDup(recs[1]) {
		t.Fatal("no duplicate marked among identical forward reads")
	}
	if isDup(recs[2]) {
		t.Fatal("reverse-strand read must not be marked")
	}
	if isDup(recs[3]) {
		t.Fatal("read at different position must not be marked")
	}
}

func TestDedupTieDeterministic(t *testing.T) {
	// Equal quality sums: the lexicographically smaller name wins.
	recs := dedupRun(t, []string{
		"zzz\t0\tchr1\t100\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII",
		"aaa\t0\tchr1\t100\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII",
	})
	if isDup(recs[0]) == isDup(recs[1]) {
		t.Fatalf("exactly one record must be marked: %v %v", isDup(recs[0]), isDup(recs[1]))
	}
	if isDup(recs[1]) {
		t.Fatal("tie-break must keep the smaller name (aaa)")
	}
}

func TestDedupExclusions(t *testing.T) {
	recs := dedupRun(t, []string{
		"u1\t4\t*\t0\t0\t*\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII",
		"u2\t4\t*\t0\t0\t*\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII",
		"s1\t256\tchr1\t100\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII",
		"s2\t256\tchr1\t100\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII",
		"x1\t2048\tchr1\t100\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII",
		"x2\t2048\tchr1\t100\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII",
	})
	for _, r := range recs {
		if isDup(r) {
			t.Fatalf("%s (flag %d) must not be marked", r.Name, int(r.Flag))
		}
	}
}

func TestDedupMateFieldsMatter(t *testing.T) {
	// Same own start, different mate start: not duplicates.
	recs := dedupRun(t, []string{
		"p1\t99\tchr1\t100\t60\t50M\t=\t300\t200\t" + strings.Repeat("A", 50) + "\t" + strings.Repeat("I", 50),
		"p2\t99\tchr1\t100\t60\t50M\t=\t500\t400\t" + strings.Repeat("A", 50) + "\t" + strings.Repeat("I", 50),
		// Same own start, different mate orientation: not duplicates.
		"q1\t83\tchr1\t700\t60\t50M\t=\t600\t-100\t" + strings.Repeat("A", 50) + "\t" + strings.Repeat("I", 50),
		"q2\t99\tchr1\t700\t60\t50M\t=\t900\t200\t" + strings.Repeat("A", 50) + "\t" + strings.Repeat("I", 50),
	})
	for _, r := range recs {
		if isDup(r) {
			t.Fatalf("%s must not be marked", r.Name)
		}
	}
}

func TestDedupOutputRemainsValid(t *testing.T) {
	in := writeTempSAM(t, genSAM(rand.New(rand.NewSource(17)), 400))
	dir := t.TempDir()
	sorted := filepath.Join(dir, "sorted.sam")
	if err := SortFile(in, sorted, SortOptions{}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "dedup.sam")
	if err := DedupFile(sorted, out); err != nil {
		t.Fatal(err)
	}
	assertSorted(t, out)
	_, before := readRecords(t, sorted)
	_, after := readRecords(t, out)
	if len(before) != len(after) {
		t.Fatalf("record count changed: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i].Flag&^FlagDuplicate != after[i].Flag&^FlagDuplicate {
			t.Fatalf("record %d: non-duplicate flag bits changed", i)
		}
	}
}
