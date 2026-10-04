package samproc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/guoyyit/sam-record-processor-go/internal/sam"
)

// estimateRecordOverhead approximates the non-Raw footprint of one held
// record (parsed struct, CIGAR ops, slice headers).
const estimateRecordOverhead = 256

// Sort coordinate-sorts arbitrary-order SAM inputs into output.
//
// Inputs are streamed record by record; buffered record bytes never
// exceed cfg.MemoryBytes. Oversized inputs spill sorted chunk files to
// cfg.TempDir. Completed chunks are journaled and reused on restart.
// The final output is produced by a k-way merge and atomically renamed.
func Sort(ctx context.Context, cfg Config, inputs []string, output string) error {
	if err := cfg.normalize(); err != nil {
		return err
	}
	if len(inputs) == 0 {
		return errors.New("sort: at least one input is required")
	}
	absInputs, err := absPaths(inputs)
	if err != nil {
		return err
	}
	absOutput, err := filepath.Abs(output)
	if err != nil {
		return err
	}

	st, existing, err := prepareRun(ctx, cfg, "sort", absInputs, absOutput)
	if err != nil {
		return err
	}

	if !st.committed {
		if err := produceChunks(ctx, cfg, st.journal, existing, absInputs); err != nil {
			return err
		}
		if err := st.commit(); err != nil {
			return err
		}
	}
	if err := st.close(false); err != nil {
		return err
	}

	paths := sortedChunks(st.chunks)
	return mergeChunksToOutput(ctx, cfg, paths, absOutput, true)
}

// runView is a journal plus flags derived at preparation time.
type runView struct {
	*journal
	committed bool
}

func prepareRun(ctx context.Context, cfg Config, kind string, inputs []string, output string) (*runView, []string, error) {
	root := tempRoot(cfg)
	if cfg.TempDir != "" {
		if err := os.MkdirAll(cfg.TempDir, 0o755); err != nil {
			return nil, nil, err
		}
		cleanupStaleRuns(root)
	}
	reused, err := findReusableJournal(root, kind, output, inputs)
	if err != nil {
		return nil, nil, err
	}
	// With no resumable journal, a crashed merge's output.tmp is stale
	// and must not shadow the new atomic output.
	if reused == nil {
		_ = os.Remove(output + ".tmp")
	}
	if reused != nil {
		// Resume: completed chunks in the journal are durable.
		dir, jerr := resumeJournal(reused, inputs)
		if jerr == nil {
			return &runView{
				journal:   dir,
				committed: reused.committed,
			}, append([]string(nil), reused.chunks...), nil
		}
		// Fall through to a fresh run if the journal cannot be resumed.
	}
	runDir, err := createRunDir(cfg, kind)
	if err != nil {
		return nil, nil, err
	}
	j, err := createJournal(runDir, kind, output, inputs)
	if err != nil {
		_ = os.RemoveAll(runDir)
		return nil, nil, err
	}
	return &runView{journal: j}, nil, nil
}

// resumeJournal reopens an existing run directory for appending.
func resumeJournal(st *journalState, inputs []string) (*journal, error) {
	path := filepath.Join(st.dir, journalName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &journal{
		dir:    st.dir,
		file:   f,
		writer: newLineWriter(f),
		kind:   st.kind,
		output: st.output,
		inputs: inputs,
		chunks: append([]string(nil), st.chunks...),
		parts:  append([]partMark(nil), st.parts...),
	}, nil
}

func tempRoot(cfg Config) string {
	if cfg.TempDir != "" {
		return cfg.TempDir
	}
	return os.TempDir()
}

func absPaths(paths []string) ([]string, error) {
	out := make([]string, len(paths))
	for i, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		out[i] = abs
	}
	return out, nil
}

// produceChunks streams every input into memory-bounded, sorted chunks.
// Chunk sequence numbers are assigned in input/stream order on the
// reader side; workers only sort and write their assigned file. Thus
// the chunk set is identical for serial and concurrent execution.
func produceChunks(ctx context.Context, cfg Config, j *journal, existing []string, inputs []string) error {
	doneChunks := make(map[string]bool, len(existing))
	for _, p := range existing {
		doneChunks[p] = true
	}

	workers := cfg.Concurrency
	if workers < 1 {
		workers = 1
	}
	var (
		wg       sync.WaitGroup
		tasks    = make(chan chunkTask, workers)
		mu       sync.Mutex
		errOnce  sync.Once
		firstErr error
		inflight int64
		cond     = sync.NewCond(&mu)
	)
	setErr := func(e error) {
		if e != nil {
			errOnce.Do(func() { firstErr = e })
		}
	}

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range tasks {
				if err := ctx.Err(); err != nil {
					setErr(err)
				} else if err := sortAndWrite(t.header, t.path, t.records); err != nil {
					setErr(err)
				} else {
					mu.Lock()
					err := j.addChunk(t.path)
					mu.Unlock()
					setErr(err)
					mu.Lock()
					if injectSortFailChunks > 0 && len(j.chunks) >= injectSortFailChunks {
						setErr(&errInjected{what: "sort chunk budget"})
					}
					mu.Unlock()
				}
				mu.Lock()
				inflight -= t.bytes
				cond.Broadcast()
				mu.Unlock()
			}
		}()
	}

	emit := func(seq uint64, header *sam.Header, recs []*sam.Record, bytes int64) {
		path := tempChunkPath(j.dir, seq)
		if doneChunks[path] {
			return
		}
		mu.Lock()
		if cfg.memoryLimited() {
			// A single batch may itself exceed the cap (one huge
			// record); it still has to be emitted.
			for inflight > 0 && inflight+bytes > cfg.MemoryBytes && firstErr == nil {
				cond.Wait()
			}
		}
		inflight += bytes
		mu.Unlock()
		select {
		case tasks <- chunkTask{header: header, path: path, records: recs, bytes: bytes}:
		case <-ctx.Done():
			mu.Lock()
			inflight -= bytes
			cond.Broadcast()
			mu.Unlock()
			setErr(ctx.Err())
		}
	}

	var (
		header *sam.Header
		seq    uint64
		buf    []*sam.Record
		used   int64
	)

	// reserve waits until emitting a batch of size bytes would keep the
	// in-flight total at or under the cap (one oversized batch is still
	// admitted, so a single huge record never deadlocks).
	reserve := func(bytes int64) {
		if !cfg.memoryLimited() {
			return
		}
		mu.Lock()
		for inflight > 0 && inflight+bytes > cfg.MemoryBytes {
			if firstErr != nil {
				break
			}
			cond.Wait()
		}
		mu.Unlock()
	}

	flush := func(force bool) error {
		if len(buf) == 0 {
			return nil
		}
		if !force && (!cfg.memoryLimited() || used < cfg.MemoryBytes) {
			return nil
		}
		if header == nil {
			return errors.New("sort: input produced records without a parsed header")
		}
		emit(seq, header, buf, used)
		seq++
		buf = nil
		used = 0
		return nil
	}

	readErr := func() error {
		for _, input := range inputs {
			if err := ctx.Err(); err != nil {
				return err
			}
			cr, err := openChunk(input)
			if err != nil {
				return fmt.Errorf("sort: open %s: %w", input, err)
			}
			if header == nil {
				header = cr.Header()
			}
			for {
				r, nerr := cr.Next()
				if errors.Is(nerr, io.EOF) {
					break
				}
				if nerr != nil {
					_ = cr.Close()
					return fmt.Errorf("sort: %s: %w", input, nerr)
				}
				buf = append(buf, r)
				used += int64(len(r.Raw)) + estimateRecordOverhead
				if cfg.memoryLimited() && used >= cfg.MemoryBytes {
					if ferr := flush(true); ferr != nil {
						return ferr
					}
				} else if cfg.memoryLimited() {
					reserve(used + estimateRecordOverhead)
				}
			}
			if err := cr.Close(); err != nil {
				return err
			}
			if firstErr != nil {
				return firstErr
			}
		}
		if ferr := flush(true); ferr != nil {
			return ferr
		}
		return nil
	}()
	close(tasks)
	wg.Wait()
	if readErr != nil {
		return readErr
	}
	return firstErr
}

type chunkTask struct {
	header  *sam.Header
	path    string
	records []*sam.Record
	bytes   int64
}

func sortAndWrite(h *sam.Header, path string, records []*sam.Record) error {
	sort.SliceStable(records, func(a, b int) bool {
		return Compare(records[a], records[b]) < 0
	})
	return writeChunk(path, h, records)
}
