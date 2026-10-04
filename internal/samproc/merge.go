package samproc

import (
	"bufio"
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/guoyyit/sam-record-processor-go/internal/sam"
)

// perMergeReaderBudget bounds the resident cost attributed to one open
// merge reader (read buffer plus the just-parsed record).
const perMergeReaderBudget int64 = 1 << 16

const maxMergeFanIn = 256

// mergeFanIn bounds how many input streams one merge pass keeps open.
func mergeFanIn(cfg Config, nInputs int) int {
	fan := maxMergeFanIn
	if cfg.memoryLimited() {
		fan = int(cfg.MemoryBytes / perMergeReaderBudget)
		if fan < 2 {
			fan = 2
		}
		if fan > maxMergeFanIn {
			fan = maxMergeFanIn
		}
	}
	if fan > nInputs {
		fan = nInputs
	}
	return fan
}

// Merge merges already coordinate-sorted SAM inputs into one globally
// ordered, legal SAM output. Records are emitted byte-for-byte
// unchanged; the result satisfies the sort invariant and may be merged
// again.
func Merge(ctx context.Context, cfg Config, inputs []string, output string) error {
	if err := cfg.normalize(); err != nil {
		return err
	}
	if len(inputs) == 0 {
		return errors.New("merge: at least one input is required")
	}
	absInputs, err := absPaths(inputs)
	if err != nil {
		return err
	}
	absOutput, err := filepath.Abs(output)
	if err != nil {
		return err
	}

	st, _, err := prepareRun(ctx, cfg, "merge", absInputs, absOutput)
	if err != nil {
		return err
	}
	if !st.committed {
		if _, err := mergeCascade(ctx, cfg, st.dir, absInputs, absOutput, st.journal); err != nil {
			_ = st.close(false)
			return err
		}
		if err := st.commit(); err != nil {
			_ = st.close(false)
			return err
		}
	}
	return st.close(true)
}

// mergeChunksToOutput runs the bounded-fan-in cascade for sort chunks.
func mergeChunksToOutput(ctx context.Context, cfg Config, chunks []string, output string, cleanup bool) error {
	dir, err := createRunDir(cfg, "merge")
	if err != nil {
		return err
	}
	j, err := createJournal(dir, "merge", output, chunks)
	if err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	if _, err := mergeCascade(ctx, cfg, j.dir, chunks, output, j); err != nil {
		_ = j.close(false)
		return err
	}
	if err := j.commit(); err != nil {
		_ = j.close(false)
		return err
	}
	return j.close(cleanup)
}

// mergeCascade runs repeated bounded-fan-in passes until at most one
// fan-in group remains, then performs the journaled, resumable final
// pass into output. Intermediate passes are complete atomic files, so a
// crash only discards the unfinished pass, which is rebuilt
// deterministically on restart.
func mergeCascade(ctx context.Context, cfg Config, runDir string, inputs []string, output string, j *journal) (string, error) {
	current := append([]string(nil), inputs...)
	level := 0
	for len(current) > mergeFanIn(cfg, len(current)) {
		fan := mergeFanIn(cfg, len(current))
		var next []string
		for start := 0; start < len(current); start += fan {
			end := start + fan
			if end > len(current) {
				end = len(current)
			}
			part := tempMergePath(runDir, level, start/fan)
			if _, statErr := os.Stat(part); statErr != nil {
				if err := mergeFilesAtomic(ctx, current[start:end], part); err != nil {
					return "", err
				}
			}
			next = append(next, part)
		}
		current = next
		level++
	}
	if err := mergeInputsToOutput(ctx, cfg, j, current); err != nil {
		return "", err
	}
	return output, nil
}

// mergeFilesAtomic merges sorted inputs into path via tmp + fsync +
// rename. Used for non-final cascade passes.
func mergeFilesAtomic(ctx context.Context, inputs []string, path string) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<16)
	if err := writeMerged(ctx, inputs, bw, 0, nil); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := bw.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// writeMerged streams the total-order merge of inputs into w. skipN is
// the number of popped records to suppress (prefix resume).
func writeMerged(ctx context.Context, inputs []string, w io.Writer, skipN int,
	onEmit func(popped int, rec *sam.Record) error) error {

	readers := make([]*chunkReader, 0, len(inputs))
	defer func() {
		for _, r := range readers {
			_ = r.Close()
		}
	}()

	var header *sam.Header
	h := &mergeHeap{}
	lastBySource := make([]*sam.Record, len(inputs))
	for i, input := range inputs {
		if err := ctx.Err(); err != nil {
			return err
		}
		cr, err := openChunk(input)
		if err != nil {
			return fmt.Errorf("merge: open %s: %w", input, err)
		}
		readers = append(readers, cr)
		if i == 0 {
			header = cr.Header()
		}
	}
	if header == nil {
		return fmt.Errorf("merge: no readable header from first input")
	}

	push := func(i int) error {
		r, err := readers[i].Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("merge: %s: %w", inputs[i], err)
		}
		if last := lastBySource[i]; last != nil && Compare(last, r) > 0 {
			return fmt.Errorf("merge: input %s is not coordinate sorted around line %d",
				inputs[i], r.LineNo)
		}
		lastBySource[i] = r
		heap.Push(h, &heapItem{rec: r, source: i})
		return nil
	}
	for i := range inputs {
		if err := push(i); err != nil {
			return err
		}
	}

	if skipN == 0 {
		if _, err := w.Write(header.Raw()); err != nil {
			return err
		}
	}

	var lineBuf []byte
	popped := 0
	for h.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := heap.Pop(h).(*heapItem)
		rec := item.rec
		popped++
		if err := push(item.source); err != nil {
			return err
		}
		if popped <= skipN {
			continue
		}
		lineBuf = rec.Append(lineBuf[:0])
		lineBuf = append(lineBuf, '\n')
		if _, err := w.Write(lineBuf); err != nil {
			return err
		}
		if onEmit != nil {
			if err := onEmit(popped, rec); err != nil {
				return err
			}
		}
	}
	return nil
}

// mergeInputsToOutput runs the final journaled pass with prefix resume
// into j.output.
func mergeInputsToOutput(ctx context.Context, cfg Config, j *journal, inputs []string) error {
	tmp := j.output + ".tmp"
	resumeOffset := int64(-1)
	resumeSkip := 0
	if len(j.parts) > 0 {
		mark := j.parts[len(j.parts)-1]
		resumeOffset = mark.offset
		resumeSkip = mark.n
	}

	var f *os.File
	if resumeOffset >= 0 {
		var oerr error
		f, oerr = os.OpenFile(tmp, os.O_RDWR, 0o644)
		if oerr != nil {
			return oerr
		}
	} else {
		var cerr error
		f, cerr = os.Create(tmp)
		if cerr != nil {
			return cerr
		}
	}
	closeAndRemove := func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}

	var offset, written int64
	emitted := 0
	if resumeOffset >= 0 {
		offset = resumeOffset
		if err := f.Truncate(offset); err != nil {
			closeAndRemove()
			return err
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			closeAndRemove()
			return err
		}
	} else {
		// Header is written by writeMerged; initialize the byte cursor
		// past it so checkpoint offsets are absolute.
		offset = 0
	}
	bw := bufio.NewWriterSize(f, 1<<16)
	headerLen := int64(0)
	// Determine header size up front so offsets account for it.
	if cr0, oerr := openChunk(inputs[0]); oerr == nil {
		headerLen = int64(len(cr0.Header().Raw()))
		_ = cr0.Close()
	}
	if resumeOffset < 0 {
		offset = headerLen
	}

	checkpointEvery := int64(1 << 20)
	if injectCheckpointBytes > 0 {
		checkpointEvery = injectCheckpointBytes
	}
	checkpoint := func(popped int) error {
		if err := bw.Flush(); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		offset += written
		return j.addPart(popped, offset)
	}

	onEmit := func(popped int, rec *sam.Record) error {
		written += int64(len(rec.Raw)) + 1
		emitted++
		if injectMergeFailLines > 0 && emitted >= injectMergeFailLines {
			if err := checkpoint(popped); err != nil {
				return err
			}
			written = 0
			_ = f.Close()
			return &errInjected{what: "merge line budget"}
		}
		if written >= checkpointEvery {
			if err := checkpoint(popped); err != nil {
				return err
			}
			written = 0
		}
		return nil
	}

	if err := writeMerged(ctx, inputs, bw, resumeSkip, onEmit); err != nil {
		var injected *errInjected
		if errors.As(err, &injected) {
			// Simulated crash: leave tmp output and journal in place.
			return err
		}
		closeAndRemove()
		return err
	}
	if err := bw.Flush(); err != nil {
		closeAndRemove()
		return err
	}
	if err := f.Sync(); err != nil {
		closeAndRemove()
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, j.output); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// heapItem is the current record of one input stream.
type heapItem struct {
	rec    *sam.Record
	source int
}

type mergeHeap []*heapItem

func (h mergeHeap) Len() int { return len(h) }

func (h mergeHeap) Less(i, j int) bool {
	if c := Compare(h[i].rec, h[j].rec); c != 0 {
		return c < 0
	}
	return h[i].source < h[j].source
}

func (h mergeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *mergeHeap) Push(x any) { *h = append(*h, x.(*heapItem)) }

func (h *mergeHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return it
}
