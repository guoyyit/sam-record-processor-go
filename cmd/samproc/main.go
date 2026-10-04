// Command samproc provides SAM sorting, merging, duplicate marking,
// indexing and coverage queries on local files.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/guoyyit/sam-record-processor-go/sam"
)

const usage = `samproc - SAM record processing toolkit

Usage:
  samproc sort     -i in.sam -o out.sam [--mem 256M] [--threads N] [--workdir DIR] [--keep-work]
  samproc merge    -o out.sam [--mem 256M] [--threads N] [--workdir DIR] in1.sam in2.sam ...
  samproc dedup    -i sorted.sam -o out.sam
  samproc index    -i sorted.sam
  samproc coverage -i sorted.sam --ref NAME --start N --end N [--include-dups]
  samproc validate -i in.sam
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "sort":
		err = cmdSort(os.Args[2:])
	case "merge":
		err = cmdMerge(os.Args[2:])
	case "dedup":
		err = cmdDedup(os.Args[2:])
	case "index":
		err = cmdIndex(os.Args[2:])
	case "coverage":
		err = cmdCoverage(os.Args[2:])
	case "validate":
		err = cmdValidate(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "samproc %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

// parseMem parses a byte size with optional K/M/G suffix.
func parseMem(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	last := s[len(s)-1]
	switch last {
	case 'K', 'k':
		mult = 1 << 10
		s = s[:len(s)-1]
	case 'M', 'm':
		mult = 1 << 20
		s = s[:len(s)-1]
	case 'G', 'g':
		mult = 1 << 30
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid memory size %q", s)
	}
	return n * mult, nil
}

type memFlag struct{ v *int64 }

func (m memFlag) String() string { return fmt.Sprint(*m.v) }
func (m memFlag) Set(s string) error {
	n, err := parseMem(s)
	if err != nil {
		return err
	}
	*m.v = n
	return nil
}

func cmdSort(args []string) error {
	fs := flag.NewFlagSet("sort", flag.ContinueOnError)
	in := fs.String("i", "", "input SAM file")
	out := fs.String("o", "", "output SAM file")
	opts := sam.SortOptions{}
	fs.Var(memFlag{&opts.MemoryLimit}, "mem", "memory limit (bytes, or K/M/G suffix)")
	fs.IntVar(&opts.Threads, "threads", 1, "concurrent sort/merge workers")
	fs.StringVar(&opts.WorkDir, "workdir", "", "directory for intermediate runs")
	fs.BoolVar(&opts.KeepWork, "keep-work", false, "keep intermediate runs after success")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return fmt.Errorf("-i and -o are required")
	}
	return sam.SortFile(*in, *out, opts)
}

func cmdMerge(args []string) error {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	out := fs.String("o", "", "output SAM file")
	opts := sam.MergeOptions{}
	fs.Var(memFlag{&opts.MemoryLimit}, "mem", "memory limit (bytes, or K/M/G suffix)")
	fs.IntVar(&opts.Threads, "threads", 1, "concurrent merge workers")
	fs.StringVar(&opts.WorkDir, "workdir", "", "directory for intermediate runs")
	fs.BoolVar(&opts.KeepWork, "keep-work", false, "keep intermediate runs after success")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || fs.NArg() == 0 {
		return fmt.Errorf("-o and at least one input are required")
	}
	return sam.MergeFiles(fs.Args(), *out, opts)
}

func cmdDedup(args []string) error {
	fs := flag.NewFlagSet("dedup", flag.ContinueOnError)
	in := fs.String("i", "", "coordinate-sorted input SAM file")
	out := fs.String("o", "", "output SAM file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return fmt.Errorf("-i and -o are required")
	}
	return sam.DedupFile(*in, *out)
}

func cmdIndex(args []string) error {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	in := fs.String("i", "", "coordinate-sorted SAM file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return fmt.Errorf("-i is required")
	}
	p, err := sam.BuildIndex(*in)
	if err != nil {
		return err
	}
	fmt.Println(p)
	return nil
}

func cmdCoverage(args []string) error {
	fs := flag.NewFlagSet("coverage", flag.ContinueOnError)
	in := fs.String("i", "", "coordinate-sorted SAM file")
	ref := fs.String("ref", "", "reference name")
	start := fs.Int("start", 0, "1-based interval start (inclusive)")
	end := fs.Int("end", 0, "1-based interval end (inclusive)")
	opts := sam.CoverageOptions{}
	fs.BoolVar(&opts.IncludeDups, "include-dups", false, "count duplicate-marked records")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *ref == "" || *start == 0 || *end == 0 {
		return fmt.Errorf("-i, --ref, --start and --end are required")
	}
	w := bufio.NewWriter(os.Stdout)
	err := sam.CoveragePositions(*in, *ref, *start, *end, opts, func(p *sam.Position) error {
		_, err := io.WriteString(w, sam.FormatPosition(p)+"\n")
		return err
	})
	if err != nil {
		return err
	}
	return w.Flush()
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	in := fs.String("i", "", "input SAM file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return fmt.Errorf("-i is required")
	}
	f, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer f.Close()
	rd := sam.NewReader(f)
	n := 0
	for {
		_, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		n++
	}
	fmt.Printf("OK: %d records, references: %s\n", n, strings.Join(rd.Header().Refs, ", "))
	return nil
}
