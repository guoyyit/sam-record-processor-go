package samproc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSortBasicAndUnmappedLast(t *testing.T) {
	cfg, dir := testConfig(t, 1<<30, 1)
	in := writeSAM(t, dir, "in.sam",
		frag("b", 0, "chr2", 5, "4M", "ACGT", "IIII"),
		frag("u", 4, "*", 0, "*", "*", "*"),
		frag("a", 0, "chr1", 20, "4M", "ACGT", "IIII"),
		frag("c", 16, "chr1", 10, "4M", "ACGT", "IIII"),
	)
	out := filepath.Join(dir, "out.sam")
	mustSort(t, cfg, out, in)
	got := readRecords(t, out)
	want := []string{"c", "a", "b", "u"}
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i][:len(w)] != w || (len(got[i]) > len(w) && got[i][len(w)] != '\t') {
			t.Fatalf("record %d = %q, want name %s", i, got[i], w)
		}
	}
}

func TestSortDeterministicConcurrentVsSerial(t *testing.T) {
	var lines []string
	for i := 0; i < 300; i++ {
		ref := "chr1"
		if i%3 == 0 {
			ref = "chr2"
		}
		pos := 1 + (i*37)%500
		lines = append(lines, frag(
			"q"+itoaUint(uint64(i)), 0, ref, pos, "4M", "ACGT", "IIII"))
	}
	// Add ties: same ref/pos, different raw bytes.
	lines = append(lines,
		frag("t1", 0, "chr1", 100, "4M", "AAAA", "IIII"),
		frag("t2", 0, "chr1", 100, "4M", "CCCC", "IIII"),
	)

	run := func(workers int, mem int64) []byte {
		cfg, dir := testConfig(t, mem, workers)
		in := writeSAM(t, dir, "in.sam", lines...)
		out := filepath.Join(dir, "out.sam")
		mustSort(t, cfg, out, in)
		return readAll(t, out)
	}
	serial := run(1, 1<<30)
	concurrent := run(4, 1<<30)
	spilled := run(4, 2048) // forces multiple chunks
	spilledSerial := run(1, 2048)
	if string(serial) != string(concurrent) {
		t.Fatalf("serial vs concurrent mismatch\n%q\n%q", serial, concurrent)
	}
	if string(serial) != string(spilled) {
		t.Fatalf("serial vs spilled mismatch")
	}
	if string(spilled) != string(spilledSerial) {
		t.Fatalf("concurrent spill vs serial spill mismatch")
	}
}

func TestSortRejectsBadInput(t *testing.T) {
	cfg, dir := testConfig(t, 1<<30, 1)
	good := testHeaderText +
		frag("ok", 0, "chr1", 10, "4M", "ACGT", "IIII") + "\n"
	bad := good +
		frag("bad", 0, "ghost", 10, "4M", "ACGT", "IIII") + "\n"
	in := filepath.Join(dir, "bad.sam")
	if err := os.WriteFile(in, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Sort(context.Background(), cfg, []string{in}, filepath.Join(dir, "o.sam"))
	if err == nil {
		t.Fatal("expected error for undeclared reference, got nil")
	}
	if !strings.Contains(err.Error(), "line") {
		t.Fatalf("error must locate the record, got: %v", err)
	}
}

func TestSortMultipleInputsMerge(t *testing.T) {
	cfg, dir := testConfig(t, 2048, 2)
	in1 := writeSAM(t, dir, "a.sam",
		frag("a", 0, "chr1", 10, "4M", "AAAA", "IIII"),
		frag("c", 0, "chr1", 40, "4M", "CCCC", "IIII"),
	)
	in2 := writeSAM(t, dir, "b.sam",
		frag("b", 0, "chr1", 20, "4M", "GGGG", "IIII"),
	)
	out := filepath.Join(dir, "o.sam")
	mustSort(t, cfg, out, in1, in2)
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
}

func TestSortVeryLongLine(t *testing.T) {
	cfg, dir := testConfig(t, 1<<30, 1)
	// One record far longer than the 64 KiB scanner buffer.
	seq := strings.Repeat("A", 5_000)
	qual := strings.Repeat("I", 5_000)
	in := writeSAM(t, dir, "big.sam",
		frag("big", 0, "chr1", 1, "5000M", seq, qual),
	)
	out := filepath.Join(dir, "o.sam")
	mustSort(t, cfg, out, in)
	if len(readRecords(t, out)) != 1 {
		t.Fatal("long record was lost or split")
	}
}

func recordNames(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		for j, c := range l {
			if c == '\t' {
				out[i] = l[:j]
				break
			}
		}
	}
	return out
}

func itoaUint(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
