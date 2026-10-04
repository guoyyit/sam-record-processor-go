package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guoyyit/sam-record-processor-go/sam"
)

const e2eHeader = "@HD\tVN:1.6\tSO:unsorted\n@SQ\tSN:chr1\tLN:1000\n@SQ\tSN:chr2\tLN:1000\n"

func writeSAM(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(e2eHeader+body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEndToEndPipeline(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.sam")
	b := filepath.Join(dir, "b.sam")
	writeSAM(t, a,
		"r9\t0\tchr2\t50\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII\n"+
			"r1\t0\tchr1\t100\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII\n"+
			"r2\t0\tchr1\t100\t60\t10M\t*\t0\t0\tAAAAAAAAAA\t5555555555\n"+
			"r3\t4\t*\t0\t0\t*\t*\t0\t0\tNNN\t###\n")
	writeSAM(t, b,
		"r5\t0\tchr1\t90\t60\t10M\t*\t0\t0\tCCCCCCCCCC\tIIIIIIIIII\n"+
			"r6\t0\tchr1\t100\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII\n")

	aSorted := filepath.Join(dir, "a.sorted.sam")
	bSorted := filepath.Join(dir, "b.sorted.sam")
	if err := cmdSort([]string{"-i", a, "-o", aSorted, "--mem", "1K", "--threads", "2"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdSort([]string{"-i", b, "-o", bSorted}); err != nil {
		t.Fatal(err)
	}
	merged := filepath.Join(dir, "merged.sam")
	if err := cmdMerge([]string{"-o", merged, aSorted, bSorted}); err != nil {
		t.Fatal(err)
	}
	dedup := filepath.Join(dir, "dedup.sam")
	if err := cmdDedup([]string{"-i", merged, "-o", dedup}); err != nil {
		t.Fatal(err)
	}
	if err := cmdIndex([]string{"-i", dedup}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sam.IndexPath(dedup)); err != nil {
		t.Fatalf("index not written: %v", err)
	}

	// Coverage of chr1:100 must exclude the two duplicate-marked records.
	var got []string
	err := sam.CoveragePositions(dedup, "chr1", 100, 100, sam.CoverageOptions{},
		func(p *sam.Position) error {
			got = append(got, sam.FormatPosition(p))
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "chr1\t100\t1\t1\t0\tA:40:+" {
		t.Fatalf("coverage: %v", got)
	}

	// Validate accepts the final output and rejects a corrupt file.
	if err := cmdValidate([]string{"-i", dedup}); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.sam")
	writeSAM(t, bad, "rx\t0\tchr9\t10\t60\t4M\t*\t0\t0\tACGT\tIIII\n")
	err = cmdValidate([]string{"-i", bad})
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("expected undeclared-reference error, got %v", err)
	}
}

func TestParseMem(t *testing.T) {
	cases := map[string]int64{
		"1024": 1024, "1K": 1024, "2k": 2048,
		"256M": 256 << 20, "1G": 1 << 30,
	}
	for in, want := range cases {
		got, err := parseMem(in)
		if err != nil || got != want {
			t.Fatalf("parseMem(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parseMem("bogus"); err == nil {
		t.Fatal("expected error")
	}
}
