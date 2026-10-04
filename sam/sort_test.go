package sam

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// genSAM builds a deterministic pseudo-random SAM file.
func genSAM(r *rand.Rand, n int) string {
	var b strings.Builder
	b.WriteString("@HD\tVN:1.6\tSO:unsorted\n")
	b.WriteString("@SQ\tSN:chr1\tLN:100000\n")
	b.WriteString("@SQ\tSN:chr2\tLN:100000\n")
	b.WriteString("@SQ\tSN:chr10\tLN:100000\n")
	refs := []string{"chr1", "chr2", "chr10"}
	for i := 0; i < n; i++ {
		seq := strings.Repeat("A", 40)
		qual := strings.Repeat("I", 40)
		switch r.Intn(10) {
		case 0: // unmapped, no coordinate
			fmt.Fprintf(&b, "r%d\t4\t*\t0\t0\t*\t*\t0\t0\t%s\t%s\n", i, seq, qual)
		case 1: // secondary
			fmt.Fprintf(&b, "r%d\t%d\t%s\t%d\t30\t40M\t*\t0\t0\t%s\t%s\n",
				i, 256, refs[r.Intn(3)], 1+r.Intn(90000), seq, qual)
		case 2: // supplementary
			fmt.Fprintf(&b, "r%d\t%d\t%s\t%d\t30\t40M\t*\t0\t0\t%s\t%s\n",
				i, 2048, refs[r.Intn(3)], 1+r.Intn(90000), seq, qual)
		default: // mapped single-end, forward or reverse
			flag := 0
			if r.Intn(2) == 0 {
				flag = 16
			}
			fmt.Fprintf(&b, "r%d\t%d\t%s\t%d\t60\t40M\t*\t0\t0\t%s\t%s\n",
				i, flag, refs[r.Intn(3)], 1+r.Intn(90000), seq, qual)
		}
	}
	return b.String()
}

func writeTempSAM(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.sam")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readRecords(t *testing.T, path string) (*Header, []*Record) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rd := NewReader(f)
	var recs []*Record
	for {
		rec, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		recs = append(recs, rec)
	}
	return rd.Header(), recs
}

func assertSorted(t *testing.T, path string) {
	t.Helper()
	_, recs := readRecords(t, path)
	for i := 1; i < len(recs); i++ {
		if Compare(recs[i-1], recs[i]) > 0 {
			t.Fatalf("record %d out of order: %q > %q", i, recs[i-1].Line(), recs[i].Line())
		}
	}
}

func TestSortBasic(t *testing.T) {
	in := writeTempSAM(t, genSAM(rand.New(rand.NewSource(42)), 300))
	out := filepath.Join(t.TempDir(), "out.sam")
	if err := SortFile(in, out, SortOptions{MemoryLimit: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	assertSorted(t, out)
	h, recs := readRecords(t, out)
	if len(recs) != 300 {
		t.Fatalf("got %d records, want 300", len(recs))
	}
	if len(h.Lines) == 0 || !strings.Contains(h.Lines[0], "SO:coordinate") {
		t.Fatalf("missing SO:coordinate header: %v", h.Lines)
	}
	// Unmapped-without-coordinate records must be at the end.
	lastPlaced := -1
	for i, r := range recs {
		if r.Placed() {
			lastPlaced = i
		} else if lastPlaced >= 0 && i < lastPlaced {
			t.Fatalf("unplaced record %d before placed record %d", i, lastPlaced)
		}
	}
}

func TestSortSpillAndConcurrency(t *testing.T) {
	in := writeTempSAM(t, genSAM(rand.New(rand.NewSource(7)), 600))
	var outputs [][]byte
	for _, cfg := range []SortOptions{
		{MemoryLimit: 1 << 20, Threads: 1},
		{MemoryLimit: 4 << 10, Threads: 1},  // forced spilling, many runs
		{MemoryLimit: 4 << 10, Threads: 4},  // concurrent
		{MemoryLimit: 64 << 10, Threads: 8}, // concurrent, fewer runs
	} {
		out := filepath.Join(t.TempDir(), "out.sam")
		if err := SortFile(in, out, cfg); err != nil {
			t.Fatalf("cfg %+v: %v", cfg, err)
		}
		assertSorted(t, out)
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, data)
	}
	for i := 1; i < len(outputs); i++ {
		if string(outputs[i]) != string(outputs[0]) {
			t.Fatalf("output %d differs from single-threaded large-memory output", i)
		}
	}
}

func TestSortResume(t *testing.T) {
	in := writeTempSAM(t, genSAM(rand.New(rand.NewSource(11)), 600))
	cfg := SortOptions{MemoryLimit: 4 << 10, Threads: 2}

	// Reference output: uninterrupted run.
	refOut := filepath.Join(t.TempDir(), "ref.sam")
	if err := SortFile(in, refOut, cfg); err != nil {
		t.Fatal(err)
	}
	refData, err := os.ReadFile(refOut)
	if err != nil {
		t.Fatal(err)
	}

	// Crashed run: injected failure after the third produced run.
	dir := t.TempDir()
	out := filepath.Join(dir, "out.sam")
	crashCfg := cfg
	crashCfg.WorkDir = filepath.Join(dir, "work")
	crashCfg.KeepWork = true
	t.Setenv("SAMPROC_FAIL_AFTER_RUNS", "3")
	if err := SortFile(in, out, crashCfg); err == nil {
		t.Fatal("expected injected failure")
	}
	t.Setenv("SAMPROC_FAIL_AFTER_RUNS", "")
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("output should not exist after failed run: %v", err)
	}

	// Record state of completed runs, then resume.
	entries, err := os.ReadDir(crashCfg.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 { // manifest + at least one run
		t.Fatalf("expected preserved run files, got %v", entries)
	}
	mtimes := map[string]int64{}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		mtimes[e.Name()] = info.ModTime().UnixNano()
	}
	if err := SortFile(in, out, crashCfg); err != nil {
		t.Fatalf("resume: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(refData) {
		t.Fatal("resumed output differs from uninterrupted output")
	}
	// Completed runs must have been reused, not rewritten.
	for name, mt := range mtimes {
		if name == manifestName {
			continue
		}
		info, err := os.Stat(filepath.Join(crashCfg.WorkDir, name))
		if err != nil {
			t.Fatalf("run file %s vanished: %v", name, err)
		}
		if info.ModTime().UnixNano() != mt {
			t.Fatalf("run file %s was rewritten instead of reused", name)
		}
	}
}

func TestSortCorruptWorkDir(t *testing.T) {
	in := writeTempSAM(t, genSAM(rand.New(rand.NewSource(13)), 400))
	cfg := SortOptions{MemoryLimit: 4 << 10, Threads: 2}
	dir := t.TempDir()
	out := filepath.Join(dir, "out.sam")
	cfg.WorkDir = filepath.Join(dir, "work")

	t.Setenv("SAMPROC_FAIL_AFTER_RUNS", "2")
	if err := SortFile(in, out, cfg); err == nil {
		t.Fatal("expected injected failure")
	}
	t.Setenv("SAMPROC_FAIL_AFTER_RUNS", "")

	// Corrupt one run and drop a stray temp file into the work dir.
	entries, err := os.ReadDir(cfg.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	var runFile string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "run-") {
			runFile = filepath.Join(cfg.WorkDir, e.Name())
			break
		}
	}
	if runFile == "" {
		t.Fatal("no run file found")
	}
	if err := os.WriteFile(runFile, []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(cfg.WorkDir, "run-999999.tmp")
	if err := os.WriteFile(stray, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SortFile(in, out, cfg); err != nil {
		t.Fatalf("resume after corruption: %v", err)
	}
	assertSorted(t, out)
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatal("stray temp file was not cleaned up")
	}
}

func TestSortEmpty(t *testing.T) {
	in := writeTempSAM(t, "@HD\tVN:1.6\n@SQ\tSN:chr1\tLN:100\n")
	out := filepath.Join(t.TempDir(), "out.sam")
	if err := SortFile(in, out, SortOptions{}); err != nil {
		t.Fatal(err)
	}
	h, recs := readRecords(t, out)
	if len(recs) != 0 {
		t.Fatalf("got %d records", len(recs))
	}
	if !h.HasRef("chr1") {
		t.Fatal("header lost reference")
	}
}
