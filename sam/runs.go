package sam

import (
	"bufio"
	"container/heap"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

// runEntry describes one completed intermediate run file.
type runEntry struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// manifest tracks completed runs so an interrupted sort or merge can be
// resumed without redoing finished work.
type manifest struct {
	ConfigHash string              `json:"configHash"`
	Runs       map[string]runEntry `json:"runs"`
}

const manifestName = "manifest.json"

// runStore manages the work directory of a sort or merge job.
type runStore struct {
	dir      string
	manifest *manifest
	mu       sync.Mutex
	produced int // runs produced in this process (for the test failpoint)
}

// openRunStore prepares dir for a job identified by configHash. State left
// by previous runs of the same job (same configHash) is validated and kept
// for reuse; anything else — stale manifests, orphaned temp files, corrupt
// runs — is removed so results stay deterministic.
func openRunStore(dir, configHash string) (*runStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	m := &manifest{ConfigHash: configHash, Runs: map[string]runEntry{}}
	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	switch {
	case err == nil:
		var old manifest
		if json.Unmarshal(data, &old) == nil && old.ConfigHash == configHash && old.Runs != nil {
			m.Runs = old.Runs
		}
	case !os.IsNotExist(err):
		return nil, err
	}
	s := &runStore{dir: dir, manifest: m}
	if err := s.validateAndSweep(); err != nil {
		return nil, err
	}
	if err := s.persist(); err != nil {
		return nil, err
	}
	return s, nil
}

// validateAndSweep drops manifest entries whose files are missing or
// corrupt, and deletes files not referenced by the manifest.
func (s *runStore) validateAndSweep() error {
	keep := map[string]bool{manifestName: true}
	for name, e := range s.manifest.Runs {
		sum, size, err := hashFile(filepath.Join(s.dir, e.File))
		if err != nil || sum != e.SHA256 || size != e.Size {
			delete(s.manifest.Runs, name)
			continue
		}
		keep[e.File] = true
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if !keep[ent.Name()] {
			if err := os.RemoveAll(filepath.Join(s.dir, ent.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *runStore) persist() error {
	data, err := json.MarshalIndent(s.manifest, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, manifestName+".tmp")
	if err := writeFileAtomic(tmp, filepath.Join(s.dir, manifestName), data); err != nil {
		return err
	}
	return nil
}

// ensure returns the path of the named run, producing it via produce if it
// is not already present and valid in the manifest. produce writes the run
// content; the file appears in the directory only once fully written.
func (s *runStore) ensure(name string, produce func(w io.Writer) error) (string, error) {
	s.mu.Lock()
	if e, ok := s.manifest.Runs[name]; ok {
		s.mu.Unlock()
		return filepath.Join(s.dir, e.File), nil
	}
	s.mu.Unlock()

	final := filepath.Join(s.dir, name)
	tmp := final + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	bw := bufio.NewWriterSize(f, 1<<20)
	w := io.MultiWriter(bw, h)
	produceErr := produce(w)
	flushErr := bw.Flush()
	syncErr := f.Sync()
	closeErr := f.Close()
	switch {
	case produceErr != nil:
		os.Remove(tmp)
		return "", produceErr
	case flushErr != nil:
		os.Remove(tmp)
		return "", flushErr
	case syncErr != nil:
		os.Remove(tmp)
		return "", syncErr
	case closeErr != nil:
		os.Remove(tmp)
		return "", closeErr
	}
	info, err := os.Stat(tmp)
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return "", err
	}

	s.mu.Lock()
	s.manifest.Runs[name] = runEntry{
		File:   name,
		SHA256: hex.EncodeToString(h.Sum(nil)),
		Size:   info.Size(),
	}
	err = s.persist()
	s.produced++
	n := s.produced
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	if limit := failAfterRuns(); limit > 0 && n >= limit {
		return "", fmt.Errorf("injected failure after producing %d runs (SAMPROC_FAIL_AFTER_RUNS)", n)
	}
	return final, nil
}

// failAfterRuns reads the test failpoint: abort after producing N runs.
func failAfterRuns() int {
	n, _ := strconv.Atoi(os.Getenv("SAMPROC_FAIL_AFTER_RUNS"))
	return n
}

// remove deletes the whole work directory.
func (s *runStore) remove() error { return os.RemoveAll(s.dir) }

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// writeFileAtomic writes data to tmp, fsyncs and renames it to final.
func writeFileAtomic(tmp, final string, data []byte) error {
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, final)
}

// mergeItem is one buffered record in a k-way merge.
type mergeItem struct {
	rec *Record
	src int
}

type mergeHeap []mergeItem

func (h mergeHeap) Len() int { return len(h) }
func (h mergeHeap) Less(i, j int) bool {
	if c := Compare(h[i].rec, h[j].rec); c != 0 {
		return c < 0
	}
	return h[i].src < h[j].src
}
func (h mergeHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *mergeHeap) Push(x interface{}) { *h = append(*h, x.(mergeItem)) }
func (h *mergeHeap) Pop() interface{} {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}

// mergeRunFiles merges already-sorted run files, invoking emit for each
// record in global sorted order. Memory use is bounded by the number of
// input files. Each input is verified to be sorted. emit must not retain
// the record.
func mergeRunFiles(paths []string, emit func(rec *Record) error) error {
	files := make([]*os.File, 0, len(paths))
	readers := make([]*Reader, 0, len(paths))
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	h := &mergeHeap{}
	last := make([]*Record, len(paths))
	for i, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		files = append(files, f)
		rd := NewReader(f)
		readers = append(readers, rd)
		rec, err := rd.Next()
		if err == io.EOF {
			continue
		}
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		last[i] = rec
		heap.Push(h, mergeItem{rec: rec, src: i})
	}
	for h.Len() > 0 {
		it := heap.Pop(h).(mergeItem)
		if err := emit(it.rec); err != nil {
			return err
		}
		rec, err := readers[it.src].Next()
		switch {
		case err == io.EOF:
		case err != nil:
			return fmt.Errorf("%s: %w", paths[it.src], err)
		default:
			if Compare(last[it.src], rec) > 0 {
				return fmt.Errorf("%s: input is not sorted: %q after %q",
					paths[it.src], rec.Line(), last[it.src].Line())
			}
			last[it.src] = rec
			heap.Push(h, mergeItem{rec: rec, src: it.src})
		}
	}
	return nil
}

// mergePasses reduces runs to at most fanIn runs by merging batches,
// naming intermediate runs "m<pass>-<index>" in the store. Intermediate
// merges run with up to threads goroutines; the resulting run list is
// deterministic regardless of thread count.
// Each produced run carries the given header so runs stay valid SAM.
func mergePasses(store *runStore, runs []string, header *Header, fanIn, threads int) ([]string, error) {
	if threads < 1 {
		threads = 1
	}
	for pass := 1; len(runs) > fanIn; pass++ {
		var groups [][]string
		for i := 0; i < len(runs); i += fanIn {
			end := i + fanIn
			if end > len(runs) {
				end = len(runs)
			}
			groups = append(groups, runs[i:end])
		}
		next := make([]string, len(groups))
		errs := make([]error, len(groups))
		var wg sync.WaitGroup
		sem := make(chan struct{}, threads)
		for i, grp := range groups {
			wg.Add(1)
			go func(i int, grp []string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				name := fmt.Sprintf("m%d-%06d", pass, i)
				path, err := store.ensure(name, func(w io.Writer) error {
					if err := WriteHeader(w, header); err != nil {
						return err
					}
					return mergeRunFiles(grp, func(rec *Record) error {
						_, err := io.WriteString(w, rec.Line()+"\n")
						return err
					})
				})
				next[i], errs[i] = path, err
			}(i, grp)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return nil, err
			}
		}
		runs = next
	}
	return runs, nil
}

// fanInFor derives the maximum merge fan-in from a memory budget.
func fanInFor(memoryLimit int64) int {
	const perReader = 2 << 20 // Reader buffer plus record overhead
	n := int(memoryLimit / perReader)
	if n < 2 {
		n = 2
	}
	if n > 1024 {
		n = 1024
	}
	return n
}

// sortRecords sorts records by the global coordinate order.
func sortRecords(recs []*Record) {
	sort.Slice(recs, func(i, j int) bool { return Less(recs[i], recs[j]) })
}
