package samproc

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/guoyyit/sam-record-processor-go/internal/sam"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testHeaderText = "@HD\tVN:1.6\tSO:coordinate\n@SQ\tSN:chr1\tLN:10000\n@SQ\tSN:chr2\tLN:5000\n"

// writeSAM writes lines to a new file and returns its path.
func writeSAM(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	var buf bytes.Buffer
	buf.WriteString(testHeaderText)
	for _, l := range lines {
		buf.WriteString(l)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// readRecords returns the alignment lines of a SAM file (header dropped).
func readRecords(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "@") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// readAll returns the full file bytes.
func readAll(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func testConfig(t *testing.T, mem int64, workers int) (Config, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.MemoryBytes = mem
	cfg.Concurrency = workers
	cfg.TempDir = dir
	return cfg, dir
}

// frag builds a single-end alignment line.
func frag(name string, flag int, ref string, pos int, cigar, seq, qual string) string {
	return fmt.Sprintf("%s\t%d\t%s\t%d\t60\t%s\t*\t0\t0\t%s\t%s",
		name, flag, ref, pos, cigar, seq, qual)
}

// paired builds one mate of a paired fragment.
func paired(name string, flag int, ref string, pos, pnext int, cigar, seq, qual, rnext string) string {
	return fmt.Sprintf("%s\t%d\t%s\t%d\t60\t%s\t%s\t%d\t0\t%s\t%s",
		name, flag, ref, pos, cigar, rnext, pnext, seq, qual)
}

func mustSort(t *testing.T, cfg Config, out string, inputs ...string) {
	t.Helper()
	if err := Sort(context.Background(), cfg, inputs, out); err != nil {
		t.Fatalf("Sort: %v", err)
	}
}
func mustParse(line string) *sam.Record {
	r, err := sam.ParseRecord([]byte(line), 1)
	if err != nil {
		panic(err)
	}
	return r
}

func mustAbs(t *testing.T, paths ...string) []string {
	t.Helper()
	out, err := absPaths(paths)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func listTree(t *testing.T, root string) {
	t.Helper()
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			t.Log("walkerr", err)
			return nil
		}
		t.Logf("NODE %s size=%d", path, info.Size())
		return nil
	})
}

func countRecords(t *testing.T, p string) int {
	t.Helper()
	return len(readRecords(t, p))
}
func globMerge(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && hasSubstr(path, ".samproc-merge-L") {
			out = append(out, path)
		}
		return nil
	})
	return out
}
func hasSubstr(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
