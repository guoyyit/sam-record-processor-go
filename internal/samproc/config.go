package samproc

import (
	"fmt"
	"runtime"
)

// DefaultMemoryBytes is used when no explicit memory cap is configured:
// 256 MiB.
const DefaultMemoryBytes int64 = 256 << 20

// Config holds resource limits and processing options.
type Config struct {
	// MemoryBytes bounds the record bytes held in RAM simultaneously
	// during sorting and coverage queries. 0 selects the default; a
	// negative value disables the cap (all in memory).
	MemoryBytes int64

	// Concurrency bounds the number of worker goroutines. 0 selects the
	// number of CPUs; 1 forces serial execution.
	Concurrency int

	// TempDir holds spill chunks and restart journals. Defaults to the
	// OS temp directory.
	TempDir string

	// CountDuplicates controls whether duplicate-flagged observations
	// contribute to the coverage view.
	CountDuplicates bool
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		MemoryBytes: DefaultMemoryBytes,
		Concurrency: runtime.NumCPU(),
	}
}

// normalize fills defaults and validates the configuration.
func (c *Config) normalize() error {
	if c.MemoryBytes == 0 {
		c.MemoryBytes = DefaultMemoryBytes
	}
	if c.Concurrency == 0 {
		c.Concurrency = runtime.NumCPU()
	}
	if c.Concurrency < 1 {
		return fmt.Errorf("concurrency must be >= 1, got %d", c.Concurrency)
	}
	return nil
}

// memoryLimited reports whether an in-RAM cap is enforced.
func (c *Config) memoryLimited() bool { return c.MemoryBytes > 0 }
