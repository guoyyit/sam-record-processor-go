package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/guoyyit/sam-record-processor-go/internal/samproc"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "samproc:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("missing command")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "sort":
		return cmdSort(ctx, rest)
	case "merge":
		return cmdMerge(ctx, rest)
	case "markdup":
		return cmdMarkdup(ctx, rest)
	case "coverage":
		return cmdCoverage(ctx, rest)
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func addCommonFlags(fs *flag.FlagSet) *samproc.Config {
	cfg := &samproc.Config{}
	fs.String("m", "",
		"in-RAM cap in bytes (suffixes K/M/G allowed); default 256M; negative disables the cap")
	fs.IntVar(&cfg.Concurrency, "j", samproc.DefaultConfig().Concurrency,
		"worker concurrency; 1 forces serial execution")
	fs.StringVar(&cfg.TempDir, "t", "", "directory for spill chunks and restart journals")
	return cfg
}

func resolveMemory(args []string, cfg *samproc.Config) error {
	raw := fsValue(args, "m")
	if raw == "" {
		cfg.MemoryBytes = samproc.DefaultConfig().MemoryBytes
		return nil
	}
	mem, err := parseSize(raw)
	if err != nil {
		return err
	}
	cfg.MemoryBytes = mem
	return nil
}

func parseSize(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'K', 'k':
		mult, s = 1<<10, s[:len(s)-1]
	case 'M', 'm':
		mult, s = 1<<20, s[:len(s)-1]
	case 'G', 'g':
		mult, s = 1<<30, s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}

func cmdSort(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sort", flag.ContinueOnError)
	cfg := addCommonFlags(fs)
	output := fs.String("o", "", "output SAM path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := resolveMemory(args, cfg); err != nil {
		return err
	}
	if *output == "" || fs.NArg() == 0 {
		return fmt.Errorf("sort requires -o and at least one input")
	}
	return samproc.Sort(ctx, *cfg, fs.Args(), *output)
}

func cmdMerge(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	cfg := addCommonFlags(fs)
	output := fs.String("o", "", "output SAM path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := resolveMemory(args, cfg); err != nil {
		return err
	}
	if *output == "" || fs.NArg() == 0 {
		return fmt.Errorf("merge requires -o and at least one input")
	}
	return samproc.Merge(ctx, *cfg, fs.Args(), *output)
}

func cmdMarkdup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("markdup", flag.ContinueOnError)
	cfg := addCommonFlags(fs)
	output := fs.String("o", "", "output SAM path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := resolveMemory(args, cfg); err != nil {
		return err
	}
	if *output == "" || fs.NArg() != 1 {
		return fmt.Errorf("markdup requires -o and exactly one input")
	}
	return samproc.MarkDuplicates(ctx, *cfg, fs.Arg(0), *output)
}

func cmdCoverage(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("coverage", flag.ContinueOnError)
	cfg := addCommonFlags(fs)
	output := fs.String("o", "", "output path (default stdout)")
	ref := fs.String("r", "", "reference name (RNAME)")
	start := fs.Int("s", 0, "interval start (1-based, inclusive)")
	end := fs.Int("e", 0, "interval end (inclusive)")
	fs.BoolVar(&cfg.CountDuplicates, "dup", false, "count duplicate-flagged records")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := resolveMemory(args, cfg); err != nil {
		return err
	}
	if *ref == "" || *start < 1 || *end < *start || fs.NArg() != 1 {
		return fmt.Errorf("coverage requires -r, -s, -e and exactly one input")
	}

	out := os.Stdout
	if *output != "" {
		f, err := os.Create(*output)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	enc := json.NewEncoder(out)
	return samproc.Coverage(ctx, *cfg, fs.Arg(0), *ref, *start, *end,
		func(p samproc.Position) error {
			return enc.Encode(p)
		})
}

// fsValue extracts a flag's raw value from args before parsing, for
// size-suffix support.
func fsValue(args []string, name string) string {
	prefix := "-" + name
	for i, a := range args {
		if a == prefix && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, prefix+"=") {
			return strings.TrimPrefix(a, prefix+"=")
		}
	}
	return ""
}

func usage() {
	fmt.Fprint(os.Stderr, `samproc - SAM record processor

usage: samproc <command> [flags] [inputs...]

commands:
  sort      coordinate-sort arbitrary-order SAM inputs
  merge     merge already coordinate-sorted SAM inputs
  markdup   mark duplicate records in a coordinate-sorted SAM
  coverage  per-position coverage view as JSON lines

common flags:
  -m bytes   in-RAM cap (K/M/G suffixes), default 256M
  -j N       concurrency (1 = serial)
  -t dir     temp/work directory for spills and restart
  -o path    output path
`)
}
