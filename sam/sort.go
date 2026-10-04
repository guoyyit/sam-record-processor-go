package sam

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
)

// SortOptions controls external coordinate sorting.
type SortOptions struct {
	// MemoryLimit bounds approximate in-memory record data, in bytes.
	// Inputs larger than the limit are processed via temporary runs.
	MemoryLimit int64
	// Threads bounds concurrent chunk sorting and merging. Output is
	// identical for any value >= 1.
	Threads int
	// WorkDir holds intermediate runs and the resume manifest.
	// Defaults to <outPath>.work.
	WorkDir string
	// KeepWork keeps the work directory after a successful sort.
	KeepWork bool
}

func (o *SortOptions) withDefaults(outPath string) SortOptions {
	cp := *o
	if cp.MemoryLimit <= 0 {
		cp.MemoryLimit = 256 << 20
	}
	if cp.Threads <= 0 {
		cp.Threads = 1
	}
	if cp.WorkDir == "" {
		cp.WorkDir = outPath + ".work"
	}
	return cp
}

// SortFile coordinate-sorts the SAM file at inPath into outPath. The
// output is deterministic: identical inputs and options produce
// byte-identical results. Interrupted runs may be resumed by re-invoking
// with the same arguments; completed intermediate runs are reused.
func SortFile(inPath, outPath string, opts SortOptions) error {
	if inPath == outPath {
		return fmt.Errorf("sort: input and output paths must differ")
	}
	opts = opts.withDefaults(outPath)
	configHash, err := sortConfigHash(inPath, opts)
	if err != nil {
		return err
	}
	store, err := openRunStore(opts.WorkDir, configHash)
	if err != nil {
		return fmt.Errorf("sort: %w", err)
	}

	header, runs, err := produceRuns(inPath, store, opts)
	if err != nil {
		return err
	}
	runs, err = mergePasses(store, runs, header, fanInFor(opts.MemoryLimit), opts.Threads)
	if err != nil {
		return err
	}
	if err := writeFinal(header, runs, outPath); err != nil {
		return err
	}
	if !opts.KeepWork {
		return store.remove()
	}
	return nil
}

// sortConfigHash identifies a sort job: same input content and options
// map to the same work directory state.
func sortConfigHash(inPath string, opts SortOptions) (string, error) {
	f, err := os.Open(inPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	fmt.Fprintf(h, "\nsort-v1\nmem=%d\nthreads=%d\n", opts.MemoryLimit, opts.Threads)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// chunkOverhead approximates per-record memory beyond the raw line.
const chunkOverhead = 160

// produceRuns reads the input, sorts chunks of bounded memory and spills
// them to run files, using up to opts.Threads concurrent workers. Chunk
// boundaries depend only on the input and options, so re-runs reuse
// existing runs from the store.
func produceRuns(inPath string, store *runStore, opts SortOptions) (*Header, []string, error) {
	chunkMem := opts.MemoryLimit / int64(opts.Threads+1)
	if chunkMem < 512 {
		chunkMem = 512
	}

	f, err := os.Open(inPath)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	rd := NewReader(f)
	header, err := rd.ReadHeader()
	if err != nil {
		return nil, nil, err
	}

	type job struct {
		idx  int
		recs []*Record
	}
	type result struct {
		idx  int
		path string
		err  error
	}
	jobs := make(chan job, opts.Threads)
	resCh := make(chan result)
	var wg sync.WaitGroup
	for w := 0; w < opts.Threads; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				sortRecords(j.recs)
				name := fmt.Sprintf("run-%06d", j.idx)
				p, err := store.ensure(name, func(w io.Writer) error {
					if err := WriteHeader(w, header); err != nil {
						return err
					}
					return writeRecords(w, j.recs)
				})
				resCh <- result{idx: j.idx, path: p, err: err}
			}
		}()
	}
	results := make([]result, 0)
	var resMu sync.Mutex
	collectorDone := make(chan struct{})
	go func() {
		for r := range resCh {
			resMu.Lock()
			results = append(results, r)
			resMu.Unlock()
		}
		close(collectorDone)
	}()

	nChunks := 0
	var chunk []*Record
	var estBytes int64
	flush := func() {
		if len(chunk) == 0 {
			return
		}
		jobs <- job{idx: nChunks, recs: chunk}
		nChunks++
		chunk = nil
		estBytes = 0
	}
	for {
		rec, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			close(jobs)
			wg.Wait()
			close(resCh)
			<-collectorDone
			return nil, nil, err
		}
		chunk = append(chunk, rec)
		estBytes += int64(len(rec.raw)) + chunkOverhead
		if estBytes >= chunkMem {
			flush()
		}
	}
	flush()
	close(jobs)
	wg.Wait()
	close(resCh)
	<-collectorDone

	for _, r := range results {
		if r.err != nil {
			return nil, nil, r.err
		}
	}
	if len(results) != nChunks {
		return nil, nil, fmt.Errorf("sort: internal error: %d results for %d chunks", len(results), nChunks)
	}
	runs := make([]string, 0, nChunks)
	for _, r := range results {
		runs = append(runs, r.path)
	}
	sort.Strings(runs) // run-%06d names sort by chunk index
	return header, runs, nil
}

func writeRecords(w io.Writer, recs []*Record) error {
	for _, r := range recs {
		if _, err := io.WriteString(w, r.Line()+"\n"); err != nil {
			return err
		}
	}
	return nil
}

// writeFinal merges runs into outPath (header first) atomically.
func writeFinal(header *Header, runs []string, outPath string) error {
	tmp := outPath + ".tmp"
	os.Remove(tmp)
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	if err := WriteHeader(bw, MergeHeaders(header)); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	err = mergeRunFiles(runs, func(rec *Record) error {
		_, err := io.WriteString(bw, rec.Line()+"\n")
		return err
	})
	if err == nil {
		err = bw.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, outPath)
}
