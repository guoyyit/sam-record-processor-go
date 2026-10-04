package samproc

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// journal file (plain text) records the progress of a sort or merge run.
//
// Lines:
//   KIND=<sort|merge>
//   OUT=<final output path>
//   INPUT[0]=<input path>
//   ...
//   CHUNK=<absolute chunk path>      (one per completed spill chunk)
//   PART=<n>@<byte offset>           (committed prefixes of partial output)
//   COMMITTED=1
//
// Only information present in a flushed journal is treated as durable.

const journalName = "journal.txt"

type journal struct {
	dir    string
	file   *os.File
	writer *bufio.Writer
	kind   string
	output string
	inputs []string
	chunks []string
	parts  []partMark
	closed bool
}

type partMark struct {
	n      int
	offset int64
}

// createRunDir makes a fresh run directory under cfg.TempDir.
func createRunDir(cfg Config, prefix string) (string, error) {
	base := cfg.TempDir
	if base == "" {
		base = os.TempDir()
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(base, "samproc-"+prefix+"-*")
	if err != nil {
		return "", err
	}
	return dir, nil
}

func createJournal(dir, kind, output string, inputs []string) (*journal, error) {
	path := filepath.Join(dir, journalName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	j := &journal{dir: dir, file: f, writer: newLineWriter(f), kind: kind, output: output, inputs: inputs}
	for _, line := range []string{"KIND=" + kind, "OUT=" + output} {
		if err := j.writeLine(line); err != nil {
			return nil, err
		}
	}
	for i, in := range inputs {
		if err := j.writeLine("INPUT[" + strconv.Itoa(i) + "]=" + in); err != nil {
			return nil, err
		}
	}
	return j, nil
}

func newLineWriter(f *os.File) *bufio.Writer { return bufio.NewWriterSize(f, 4096) }

func (j *journal) writeLine(line string) error {
	if _, err := j.writer.WriteString(line + "\n"); err != nil {
		return err
	}
	return j.writer.Flush()
}

func (j *journal) addChunk(path string) error {
	j.chunks = append(j.chunks, path)
	return j.writeLine("CHUNK=" + path)
}

func (j *journal) addPart(n int, offset int64) error {
	j.parts = append(j.parts, partMark{n: n, offset: offset})
	if err := j.writeLine(fmt.Sprintf("PART=%d@%d", n, offset)); err != nil {
		return err
	}
	return nil
}

func (j *journal) commit() error {
	if err := j.writeLine("COMMITTED=1"); err != nil {
		return err
	}
	return j.file.Sync()
}

func (j *journal) close(remove bool) error {
	if j.closed {
		return nil
	}
	j.closed = true
	cerr := j.file.Close()
	if remove {
		rerr := os.RemoveAll(j.dir)
		if cerr == nil {
			cerr = rerr
		}
	}
	return cerr
}

type journalState struct {
	kind      string
	output    string
	inputs    []string
	chunks    []string
	parts     []partMark
	committed bool
	dir       string
}

// findReusableJournal scans dir (recursively, one level) for a journal
// whose kind, output and ordered input set match the requested run.
func findReusableJournal(root, kind, output string, inputs []string) (*journalState, error) {
	var found *journalState
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != journalName {
			return nil
		}
		st, perr := parseJournal(path)
		if perr != nil {
			return nil // corrupt/foreign journal: ignore
		}
		if st.kind == kind && st.output == output && sameInputs(st.inputs, inputs) {
			found = st
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

func parseJournal(path string) (*journalState, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st := &journalState{dir: filepath.Dir(path)}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	inputMap := map[int]string{}
	maxInput := -1
	for sc.Scan() {
		line := sc.Text()
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, errors.New("bad journal line")
		}
		switch key {
		case "KIND":
			st.kind = value
		case "OUT":
			st.output = value
		case "CHUNK":
			if _, statErr := os.Stat(value); statErr != nil {
				return nil, fmt.Errorf("missing chunk %s", value)
			}
			st.chunks = append(st.chunks, value)
		case "COMMITTED":
			st.committed = value == "1"
		case "PART":
			nStr, offStr, splitOK := strings.Cut(value, "@")
			n, e1 := strconv.Atoi(nStr)
			off, e2 := strconv.ParseInt(offStr, 10, 64)
			if !splitOK || e1 != nil || e2 != nil {
				return nil, errors.New("bad PART mark")
			}
			st.parts = append(st.parts, partMark{n: n, offset: off})
		default:
			if strings.HasPrefix(key, "INPUT[") && strings.HasSuffix(key, "]") {
				idxStr := key[len("INPUT[") : len(key)-1]
				idx, e := strconv.Atoi(idxStr)
				if e != nil {
					return nil, errors.New("bad INPUT index")
				}
				inputMap[idx] = value
				if idx > maxInput {
					maxInput = idx
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if maxInput >= 0 {
		st.inputs = make([]string, maxInput+1)
		for i := 0; i <= maxInput; i++ {
			v, ok := inputMap[i]
			if !ok {
				return nil, fmt.Errorf("missing INPUT[%d]", i)
			}
			st.inputs[i] = v
		}
	}
	if st.kind == "" || st.output == "" {
		return nil, errors.New("incomplete journal")
	}
	return st, nil
}

func sameInputs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	// Multi-way merge is order-sensitive for chunk identity, so inputs
	// are compared as ordered lists.
	for i := range a {
		absA, _ := filepath.Abs(a[i])
		absB, _ := filepath.Abs(b[i])
		if absA != absB {
			return false
		}
	}
	return true
}

// cleanupStaleRuns removes run directories that have no matching
// journal (e.g. leftover *.tmp spill files) directly under root.
func cleanupStaleRuns(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, "samproc-") {
			continue
		}
		path := filepath.Join(root, name)
		if _, err := os.Stat(filepath.Join(path, journalName)); err != nil {
			_ = os.RemoveAll(path)
		}
	}
}

// sortedChunks sorts chunk paths by their numeric id suffix for stable
// reuse ordering.
func sortedChunks(chunks []string) []string {
	out := append([]string(nil), chunks...)
	sort.Strings(out)
	return out
}
