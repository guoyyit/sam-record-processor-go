package samproc

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func markedDup(line string) bool {
	fields := strings.Split(line, "\t")
	v := 0
	for _, c := range fields[1] {
		v = v*10 + int(c-'0')
	}
	return v&0x400 != 0
}

func TestMarkDupSingleEnd(t *testing.T) {
	cfg, dir := testConfig(t, 1<<20, 1)
	in := writeSAM(t, dir, "in.sam",
		frag("lo", 0, "chr1", 10, "4M", "AAAA", "!!!!"), // qual 0
		frag("hi", 0, "chr1", 10, "4M", "CCCC", "IIII"), // qual 40 -> winner
		frag("other", 0, "chr1", 20, "4M", "GGGG", "IIII"),
	)
	out := filepath.Join(dir, "o.sam")
	if err := MarkDuplicates(context.Background(), cfg, in, out); err != nil {
		t.Fatalf("MarkDuplicates: %v", err)
	}
	byName := map[string]string{}
	for _, l := range readRecords(t, out) {
		name := l[:strings.IndexByte(l, '\t')]
		byName[name] = l
	}
	if !markedDup(byName["lo"]) {
		t.Fatalf("low-quality record must be marked duplicate:\n%s", byName["lo"])
	}
	if markedDup(byName["hi"]) {
		t.Fatalf("high-quality representative must not be marked:\n%s", byName["hi"])
	}
	if markedDup(byName["other"]) {
		t.Fatal("non-duplicate record was marked")
	}
}

func TestMarkDupOrientationMatters(t *testing.T) {
	cfg, dir := testConfig(t, 1<<20, 1)
	in := writeSAM(t, dir, "in.sam",
		frag("fwd", 0, "chr1", 10, "4M", "AAAA", "IIII"),
		frag("rev", 16, "chr1", 10, "4M", "CCCC", "IIII"),
	)
	out := filepath.Join(dir, "o.sam")
	if err := MarkDuplicates(context.Background(), cfg, in, out); err != nil {
		t.Fatal(err)
	}
	for _, l := range readRecords(t, out) {
		if markedDup(l) {
			t.Fatalf("different orientations are not duplicates: %s", l)
		}
	}
}

func TestMarkDupTieDeterministic(t *testing.T) {
	cfg, dir := testConfig(t, 1<<20, 1)
	in := writeSAM(t, dir, "in.sam",
		frag("bbb", 0, "chr1", 10, "4M", "CCCC", "IIII"),
		frag("aaa", 0, "chr1", 10, "4M", "AAAA", "IIII"),
	)
	out := filepath.Join(dir, "o.sam")
	if err := MarkDuplicates(context.Background(), cfg, in, out); err != nil {
		t.Fatal(err)
	}
	byName := map[string]bool{}
	for _, l := range readRecords(t, out) {
		name := l[:strings.IndexByte(l, '\t')]
		byName[name] = markedDup(l)
	}
	if byName["aaa"] {
		t.Fatal("equal-quality tie must keep lexicographically smaller raw line")
	}
	if !byName["bbb"] {
		t.Fatal("equal-quality tie must mark the other record")
	}
}

func TestMarkDupPaired(t *testing.T) {
	cfg, dir := testConfig(t, 1<<20, 1)
	// Two fragments with identical ends (chr1:10 fwd, chr1:50 rev).
	mkFrag := func(prefix string, q1, q2 byte) []string {
		return []string{
			paired(prefix, 99, "chr1", 10, 50, "4M", "AAAA", string([]byte{q1, q1, q1, q1}), "chr1"),
			paired(prefix, 147, "chr1", 50, 10, "4M", "TTTT", string([]byte{q2, q2, q2, q2}), "chr1"),
		}
	}
	var lines []string
	lines = append(lines, mkFrag("hi", 'I', 'I')...)
	lines = append(lines, mkFrag("lo", '!', '!')...)
	in := writeSAM(t, dir, "in.sam", lines...)
	out := filepath.Join(dir, "o.sam")
	if err := MarkDuplicates(context.Background(), cfg, in, out); err != nil {
		t.Fatal(err)
	}
	type state struct{ total, dup int }
	byName := map[string]*state{}
	for _, l := range readRecords(t, out) {
		name := l[:strings.IndexByte(l, '\t')]
		st := byName[name]
		if st == nil {
			st = &state{}
			byName[name] = st
		}
		st.total++
		if markedDup(l) {
			st.dup++
		}
	}
	for _, l := range readRecords(t, out) {
		t.Log("LINE", l)
	}
	t.Logf("hi=%+v lo=%+v", byName["hi"], byName["lo"])
	if byName["hi"].dup != 0 {
		t.Fatal("best pair must stay unmarked")
	}
	if byName["lo"].dup != byName["lo"].total {
		t.Fatal("both mates of the losing pair must be marked")
	}
}

func TestMarkDupExempt(t *testing.T) {
	cfg, dir := testConfig(t, 1<<20, 1)
	in := writeSAM(t, dir, "in.sam",
		frag("sec1", 256, "chr1", 10, "4M", "AAAA", "IIII"), // secondary
		frag("sec2", 256, "chr1", 10, "4M", "CCCC", "IIII"),
		frag("sup1", 2048, "chr1", 10, "4M", "GGGG", "IIII"), // supplementary
		frag("sup2", 2048, "chr1", 10, "4M", "TTTT", "IIII"),
		frag("un1", 4, "*", 0, "*", "*", "*"),
		frag("un2", 4, "*", 0, "*", "*", "*"),
	)
	out := filepath.Join(dir, "o.sam")
	if err := MarkDuplicates(context.Background(), cfg, in, out); err != nil {
		t.Fatal(err)
	}
	for _, l := range readRecords(t, out) {
		if markedDup(l) {
			t.Fatalf("secondary/supplementary/unmapped must never be marked: %s", l)
		}
	}
}
