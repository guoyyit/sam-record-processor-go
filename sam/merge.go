package sam

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// MergeOptions controls k-way merging of sorted SAM files.
type MergeOptions struct {
	// MemoryLimit bounds merge fan-in and read buffers, in bytes.
	MemoryLimit int64
	// Threads bounds concurrent intermediate merge passes.
	Threads int
	// WorkDir holds intermediate runs when the fan-in of a single pass
	// would exceed MemoryLimit. Defaults to <outPath>.work.
	WorkDir string
	// KeepWork keeps the work directory after a successful merge.
	KeepWork bool
}

func (o *MergeOptions) withDefaults(outPath string) MergeOptions {
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

// MergeFiles merges coordinate-sorted SAM files into one globally sorted
// output. Records are carried through unmodified; headers are combined.
// The result can itself participate in further merges.
func MergeFiles(inPaths []string, outPath string, opts MergeOptions) error {
	if len(inPaths) == 0 {
		return fmt.Errorf("merge: no input files")
	}
	for _, p := range inPaths {
		if p == outPath {
			return fmt.Errorf("merge: output path must differ from inputs")
		}
	}
	opts = opts.withDefaults(outPath)
	configHash, err := mergeConfigHash(inPaths, opts)
	if err != nil {
		return err
	}
	store, err := openRunStore(opts.WorkDir, configHash)
	if err != nil {
		return fmt.Errorf("merge: %w", err)
	}

	headers := make([]*Header, 0, len(inPaths))
	for _, p := range inPaths {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		h, err := NewReader(f).ReadHeader()
		f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		headers = append(headers, h)
	}
	merged := MergeHeaders(headers...)

	runs, err := mergePasses(store, inPaths, merged, fanInFor(opts.MemoryLimit), opts.Threads)
	if err != nil {
		return err
	}
	if err := writeFinal(merged, runs, outPath); err != nil {
		return err
	}
	if !opts.KeepWork {
		return store.remove()
	}
	return nil
}

// mergeConfigHash identifies a merge job by its ordered inputs and options.
func mergeConfigHash(inPaths []string, opts MergeOptions) (string, error) {
	h := sha256.New()
	for _, p := range inPaths {
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
		fmt.Fprintf(h, "\n<%s", p)
	}
	fmt.Fprintf(h, "\nmerge-v1\nmem=%d\nthreads=%d\n", opts.MemoryLimit, opts.Threads)
	return hex.EncodeToString(h.Sum(nil)), nil
}
