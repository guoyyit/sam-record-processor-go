package samproc

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func measurePeakHeap(fn func()) uint64 {
	runtime.GC()
	var peak atomic.Uint64
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				runtime.GC()
				runtime.ReadMemStats(&ms)
				if ms.HeapInuse > peak.Load() {
					peak.Store(ms.HeapInuse)
				}
				runtime.Gosched()
			}
		}
	}()
	fn()
	close(stop)
	return peak.Load()
}

func TestSortMemoryCap(t *testing.T) {
	// Many ~100-byte records; spill aggressively with a 1 MiB cap.
	var lines []string
	seq := strings.Repeat("A", 40)
	qual := strings.Repeat("I", 40)
	for i := 0; i < 20_000; i++ {
		ref := "chr1"
		if i%5 == 0 {
			ref = "chr2"
		}
		lines = append(lines, frag(
			"q"+itoaUint(uint64(i)), 0, ref, 1+(i%4900), "40M", seq, qual))
	}

	run := func(mem int64) (uint64, int) {
		cfg, dir := testConfig(t, mem, 1)
		in := writeSAM(t, dir, "in.sam", lines...)
		out := filepath.Join(dir, "o.sam")
		var peak uint64
		peak = measurePeakHeap(func() {
			if err := Sort(context.Background(), cfg, []string{in}, out); err != nil {
				t.Fatalf("Sort: %v", err)
			}
		})
		return peak, len(readRecords(t, out))
	}

	cappedPeak, cappedN := run(1 << 20)
	freePeak, freeN := run(-1)
	t.Logf("heap growth capped=%d KiB unlimited=%d KiB", cappedPeak/1024, freePeak/1024)
	if cappedN != len(lines) || freeN != len(lines) {
		t.Fatalf("record loss: capped=%d free=%d want=%d", cappedN, freeN, len(lines))
	}
	// The capped run must hold strictly less resident data than the
	// unlimited run, which keeps every record in RAM simultaneously.
	if cappedPeak >= freePeak {
		t.Fatalf("memory cap ineffective: capped=%d unlimited=%d", cappedPeak, freePeak)
	}
}
