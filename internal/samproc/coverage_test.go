package samproc

import (
	"context"
	"testing"
)

func collectCoverage(t *testing.T, cfg Config, in, ref string, start, end int) []Position {
	t.Helper()
	var out []Position
	err := Coverage(context.Background(), cfg, in, ref, start, end, func(p Position) error {
		out = append(out, p)
		return nil
	})
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}
	return out
}

func TestCoverageDeletionAndZero(t *testing.T) {
	cfg, dir := testConfig(t, 1<<20, 1)
	// 3M 1D 2M at pos 10: bases at 10,11,12; deletion at 13; bases 14,15.
	in := writeSAM(t, dir, "in.sam",
		frag("r", 0, "chr1", 10, "3M1D2M", "ACGTA", "IIIII"),
	)
	positions := collectCoverage(t, cfg, in, "chr1", 9, 16)
	if len(positions) != 8 {
		t.Fatalf("got %d positions, want 8", len(positions))
	}
	if positions[0].Covered || positions[0].Depth != 0 {
		t.Fatal("pos 9 must be uncovered with depth 0")
	}
	// deletion site 13: covered but contributes no base.
	p13 := positions[4]
	if !p13.Covered || p13.Depth != 0 || len(p13.Observations) != 0 {
		t.Fatalf("deletion site must be covered with zero observations: %+v", p13)
	}
	if baseAt(positions, 10) != "A" || baseAt(positions, 15) != "A" {
		t.Fatal("aligned bases wrong around deletion")
	}
	if positions[len(positions)-1].Covered {
		t.Fatal("pos 16 must be uncovered")
	}
}

func TestCoverageInsertionDoesNotShift(t *testing.T) {
	cfg, dir := testConfig(t, 1<<20, 1)
	// 2M 2I 2M at pos 20: ref bases at 20,21,22,23; insertion after 21.
	in := writeSAM(t, dir, "in.sam",
		frag("r", 0, "chr1", 20, "2M2I2M", "ACGTAC", "IIIIII"),
	)
	positions := collectCoverage(t, cfg, in, "chr1", 20, 23)
	p21 := positions[1]
	if len(p21.InsertedBases) != 2 {
		t.Fatalf("expected 2 inserted bases after pos 21, got %d", len(p21.InsertedBases))
	}
	if string(p21.InsertedBases[0].Base) != "G" || string(p21.InsertedBases[1].Base) != "T" {
		t.Fatal("inserted bases wrong")
	}
	if baseAt(positions, 22) != "A" || baseAt(positions, 23) != "C" {
		t.Fatal("insertion must not shift downstream positions")
	}
}

func TestCoverageNoRecordInInterval(t *testing.T) {
	cfg, dir := testConfig(t, 1<<20, 1)
	in := writeSAM(t, dir, "in.sam",
		frag("r", 0, "chr1", 100, "4M", "ACGT", "IIII"),
	)
	positions := collectCoverage(t, cfg, in, "chr1", 1, 3)
	for _, p := range positions {
		if p.Covered {
			t.Fatal("every site must be uncovered when no record intersects")
		}
	}
}

func TestCoverageDirectionAndDup(t *testing.T) {
	cfg, dir := testConfig(t, 1<<20, 1)
	in := writeSAM(t, dir, "in.sam",
		frag("fwd", 1024, "chr1", 50, "1M", "A", "I"), // duplicate
		frag("rev", 16, "chr1", 50, "1M", "C", "I"),
	)
	without := collectCoverage(t, cfg, in, "chr1", 50, 50)[0]
	if without.Depth != 1 || without.Observations[0].Reverse != true {
		t.Fatalf("duplicate must be excluded by default: %+v", without)
	}
	cfg.CountDuplicates = true
	with := collectCoverage(t, cfg, in, "chr1", 50, 50)[0]
	if with.Depth != 2 {
		t.Fatalf("duplicate must count when enabled, depth=%d", with.Depth)
	}
}

func TestCoverageBoundedMemory(t *testing.T) {
	// A large number of overlapping records plus a long interval must
	// complete while the active set stays tiny (records are 1M long,
	// but coverage never buffers per-position state).
	cfg, dir := testConfig(t, 1<<20, 1)
	var lines []string
	for i := 0; i < 500; i++ {
		lines = append(lines, frag("q"+itoaUint(uint64(i)), 0, "chr1", 10+i%5, "10M",
			"ACGTACGTAC", "IIIIIIIIII"))
	}
	in := writeSAM(t, dir, "in.sam", lines...)
	positions := collectCoverage(t, cfg, in, "chr1", 1, 200)
	if len(positions) != 200 {
		t.Fatalf("got %d positions", len(positions))
	}
}

func TestCoverageSpliceAndSoftClip(t *testing.T) {
	cfg, dir := testConfig(t, 1<<20, 1)
	// 1S 2M 3N 2M 1S at POS 10.
	// ref bases: pos10,11 aligned; 12-14 intron; 15,16 aligned.
	in := writeSAM(t, dir, "in.sam",
		frag("r", 0, "chr1", 10, "1S2M3N2M1S", "SACGTA", "IIIIII"),
	)
	positions := collectCoverage(t, cfg, in, "chr1", 10, 16)
	// Intron positions 12..14 are skipped (not covered, no base).
	for _, p := range positions {
		switch p.Pos {
		case 10, 11, 15, 16:
			if !p.Covered || p.Depth != 1 {
				t.Fatalf("pos %d must be covered with 1 base: %+v", p.Pos, p)
			}
		case 12, 13, 14:
			if p.Covered {
				t.Fatalf("intron pos %d must not be covered: %+v", p.Pos, p)
			}
		}
	}
	if baseAt(positions, 10) != "A" || baseAt(positions, 11) != "C" {
		t.Fatal("soft-clipped leading base must not occupy ref pos 10")
	}
	if baseAt(positions, 15) != "G" || baseAt(positions, 16) != "T" {
		t.Fatal("bases after intron must align to 15/16")
	}
}

func TestCoverageUnknownQual(t *testing.T) {
	cfg, dir := testConfig(t, 1<<20, 1)
	in := writeSAM(t, dir, "in.sam",
		frag("r", 0, "chr1", 10, "2M", "AC", "*"),
	)
	positions := collectCoverage(t, cfg, in, "chr1", 10, 10)
	if positions[0].Observations[0].Qual != 255 {
		t.Fatalf("QUAL '*' must yield 255, got %d", positions[0].Observations[0].Qual)
	}
}

func baseAt(positions []Position, p int) string {
	for _, x := range positions {
		if x.Pos == p {
			if len(x.Observations) == 0 {
				return ""
			}
			return string(x.Observations[0].Base)
		}
	}
	return ""
}
