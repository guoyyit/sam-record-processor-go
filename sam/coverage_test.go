package sam

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func collectCov(t *testing.T, path, ref string, start, end int, opts CoverageOptions) []*Position {
	t.Helper()
	var out []*Position
	err := CoveragePositions(path, ref, start, end, opts, func(p *Position) error {
		cp := *p
		cp.Obs = append([]Observation(nil), p.Obs...)
		out = append(out, &cp)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != end-start+1 {
		t.Fatalf("got %d positions, want %d", len(out), end-start+1)
	}
	for i, p := range out {
		if p.Pos != start+i {
			t.Fatalf("position %d: got %d", i, p.Pos)
		}
	}
	return out
}

const covHeader = "@HD\tVN:1.6\tSO:coordinate\n@SQ\tSN:chr1\tLN:100000\n@SQ\tSN:chr2\tLN:100000\n"

func writeCovInput(t *testing.T, records []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cov.sam")
	if err := os.WriteFile(p, []byte(covHeader+strings.Join(records, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCoverageCigarSemantics(t *testing.T) {
	in := writeCovInput(t, []string{
		// 5M2I3M: bases at 10-14, insertion of 2 after 14, bases at 15-17.
		"r1\t0\tchr1\t10\t60\t5M2I3M\t*\t0\t0\tAACGTTTAAA\tIIIIIIIIII",
		// 2M3D2M: bases at 12-13, deletion over 14-16, bases at 17-18.
		"r2\t16\tchr1\t12\t60\t2M3D2M\t*\t0\t0\tCCCC\t5555",
		// duplicate-flagged read over 16-19.
		"r3\t1024\tchr1\t16\t60\t4M\t*\t0\t0\tGGGG\tIIII",
	})
	pos := collectCov(t, in, "chr1", 10, 20, CoverageOptions{})
	at := func(p int) *Position { return pos[p-10] }

	if d := at(10); d.Depth != 1 || d.Covered != 1 || d.Obs[0].Base != 'A' || d.Obs[0].Qual != 40 || d.Obs[0].Reverse {
		t.Fatalf("pos 10: %+v", d)
	}
	if d := at(14); d.Depth != 1 || d.Covered != 2 || d.Ins != 2 {
		t.Fatalf("pos 14: depth %d covered %d ins %d", d.Depth, d.Covered, d.Ins)
	}
	// Deletion-only positions for r2: depth 0 from r2 but covered.
	if d := at(15); d.Depth != 1 || d.Covered != 2 {
		t.Fatalf("pos 15: depth %d covered %d", d.Depth, d.Covered)
	}
	// r3 is duplicate-flagged: excluded by default.
	if d := at(19); d.Depth != 0 || d.Covered != 0 {
		t.Fatalf("pos 19: depth %d covered %d (duplicate must be excluded)", d.Depth, d.Covered)
	}
	// Position beyond all records: depth 0 and covered 0.
	if d := at(20); d.Depth != 0 || d.Covered != 0 {
		t.Fatalf("pos 20: %+v", d)
	}
	// Reverse strand observation from r2 alongside forward r1.
	if d := at(12); len(d.Obs) != 2 || d.Obs[0].Reverse || !d.Obs[1].Reverse ||
		d.Obs[1].Base != 'C' || d.Obs[1].Qual != 20 {
		t.Fatalf("pos 12: %+v", d)
	}
	// With duplicates included, r3 contributes.
	pos = collectCov(t, in, "chr1", 16, 19, CoverageOptions{IncludeDups: true})
	if pos[0].Depth != 2 || pos[3].Depth != 1 {
		t.Fatalf("with dups: pos16 depth %d, pos19 depth %d", pos[0].Depth, pos[3].Depth)
	}
}

func TestCoverageZeroDepthVsNoRecord(t *testing.T) {
	in := writeCovInput(t, []string{
		// Deletion over 11-13: spanned but no bases.
		"r1\t0\tchr1\t10\t60\t1M3D1M\t*\t0\t0\tAC\tII",
	})
	pos := collectCov(t, in, "chr1", 9, 16, CoverageOptions{})
	cases := []struct {
		p       int
		depth   int
		covered int
	}{
		{9, 0, 0},  // no record at all
		{10, 1, 1}, // base
		{11, 0, 1}, // deletion: zero depth, but covered
		{12, 0, 1},
		{13, 0, 1},
		{14, 1, 1},
		{15, 0, 0},
		{16, 0, 0},
	}
	for _, tc := range cases {
		got := pos[tc.p-9]
		if got.Depth != tc.depth || got.Covered != tc.covered {
			t.Fatalf("pos %d: depth %d covered %d, want %d/%d",
				tc.p, got.Depth, got.Covered, tc.depth, tc.covered)
		}
	}
}

func TestCoverageExcludesNonPrimary(t *testing.T) {
	in := writeCovInput(t, []string{
		"pri\t0\tchr1\t10\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII",
		"sec\t256\tchr1\t10\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII",
		"sup\t2048\tchr1\t10\t60\t10M\t*\t0\t0\tAAAAAAAAAA\tIIIIIIIIII",
	})
	pos := collectCov(t, in, "chr1", 10, 19, CoverageOptions{IncludeDups: true})
	for _, p := range pos {
		if p.Depth != 1 {
			t.Fatalf("pos %d: depth %d, want 1 (only primary)", p.Pos, p.Depth)
		}
	}
}

func TestCoverageEmptyRegion(t *testing.T) {
	in := writeCovInput(t, []string{
		"r1\t0\tchr1\t10\t60\t5M\t*\t0\t0\tAAAAA\tIIIII",
	})
	pos := collectCov(t, in, "chr1", 100, 110, CoverageOptions{})
	for _, p := range pos {
		if p.Depth != 0 || p.Covered != 0 || len(p.Obs) != 0 {
			t.Fatalf("pos %d: %+v", p.Pos, p)
		}
	}
	// Reference with no records at all.
	pos = collectCov(t, in, "chr2", 1, 5, CoverageOptions{})
	for _, p := range pos {
		if p.Depth != 0 || p.Covered != 0 {
			t.Fatalf("chr2 pos %d: %+v", p.Pos, p)
		}
	}
}

func TestCoverageIndexConsistency(t *testing.T) {
	// Sorted file spanning multiple index windows.
	in := writeTempSAM(t, genSAM(rand.New(rand.NewSource(23)), 1500))
	dir := t.TempDir()
	sorted := filepath.Join(dir, "sorted.sam")
	if err := SortFile(in, sorted, SortOptions{}); err != nil {
		t.Fatal(err)
	}
	regions := []struct {
		ref         string
		start, end  int
		includeDups bool
	}{
		{"chr1", 1, 500, false},
		{"chr1", 20000, 20100, false},
		{"chr10", 50000, 50300, true},
		{"chr2", 89999, 90001, false},
		{"chr2", 95000, 96000, false}, // beyond all records
	}
	collect := func(path string) []string {
		var lines []string
		for _, rg := range regions {
			err := CoveragePositions(path, rg.ref, rg.start, rg.end,
				CoverageOptions{IncludeDups: rg.includeDups},
				func(p *Position) error {
					lines = append(lines, FormatPosition(p))
					return nil
				})
			if err != nil {
				t.Fatal(err)
			}
		}
		return lines
	}
	without := collect(sorted)
	if _, err := BuildIndex(sorted); err != nil {
		t.Fatal(err)
	}
	with := collect(sorted)
	if strings.Join(with, "\n") != strings.Join(without, "\n") {
		for i := range with {
			if with[i] != without[i] {
				t.Fatalf("line %d differs:\nwith:    %s\nwithout: %s", i, with[i], without[i])
			}
		}
		t.Fatal("indexed and full-scan coverage differ")
	}
	// Spot-check a known position against a brute-force count.
	_, recs := readRecords(t, sorted)
	ref, p0 := "chr1", 20000
	wantDepth := 0
	for _, r := range recs {
		if r.RName != ref || r.Flag&(FlagUnmapped|FlagSecondary|FlagSupplementary|FlagDuplicate) != 0 {
			continue
		}
		ar := expandRecord(r)
		if p0 >= ar.pos && p0 <= ar.end && ar.base[p0-ar.pos] != 0 {
			wantDepth++
		}
	}
	var got *Position
	if err := CoveragePositions(sorted, ref, p0, p0, CoverageOptions{}, func(p *Position) error {
		got = p
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got.Depth != wantDepth {
		t.Fatalf("depth at %s:%d = %d, want %d", ref, p0, got.Depth, wantDepth)
	}
}

func TestCoverageInvalidInterval(t *testing.T) {
	in := writeCovInput(t, nil)
	if err := CoveragePositions(in, "chr1", 0, 10, CoverageOptions{}, func(*Position) error { return nil }); err == nil {
		t.Fatal("expected error for start < 1")
	}
	if err := CoveragePositions(in, "chr1", 10, 5, CoverageOptions{}, func(*Position) error { return nil }); err == nil {
		t.Fatal("expected error for end < start")
	}
	if err := CoveragePositions(in, "*", 1, 5, CoverageOptions{}, func(*Position) error { return nil }); err == nil {
		t.Fatal("expected error for wildcard reference")
	}
}

func TestFormatPosition(t *testing.T) {
	p := &Position{
		Ref: "chr1", Pos: 42, Depth: 2, Covered: 3, Ins: 1,
		Obs: []Observation{
			{Base: 'A', Qual: 40},
			{Base: 'T', Qual: 20, Reverse: true},
		},
	}
	got := FormatPosition(p)
	want := "chr1\t42\t2\t3\t1\tA:40:+,T:20:-"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if !strings.HasPrefix(fmt.Sprintf("%v", got), "chr1") {
		t.Fatal("format broken")
	}
}
