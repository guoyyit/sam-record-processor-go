package sam

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Observation is a single read-base observation at a reference position.
type Observation struct {
	Base    byte // observed base (A/C/G/T/N as stored in SEQ)
	Qual    byte // Phred quality score
	Reverse bool // read aligned to the reverse strand
}

// Position describes the coverage of one reference position.
type Position struct {
	Ref     string
	Pos     int // 1-based
	Depth   int // number of base observations
	Covered int // records spanning the position (bases or deletions)
	Ins     int // insertions observed immediately after this position
	Obs     []Observation
}

// CoverageOptions controls coverage computation.
type CoverageOptions struct {
	// IncludeDups counts records flagged as duplicates. By default they
	// are excluded.
	IncludeDups bool
}

// indexWindow is the granularity of the linear index, in reference bases.
const indexWindow = 16384

// indexEntry maps a reference window to the byte offset of the first
// record starting inside it.
type indexEntry struct {
	Ref    string `json:"ref"`
	Pos    int    `json:"pos"`
	Offset int64  `json:"offset"`
}

type indexRef struct {
	MaxRefLen int `json:"maxRefLen"`
}

// samIndex is a linear index over a coordinate-sorted SAM file.
type samIndex struct {
	Version int                 `json:"version"`
	Window  int                 `json:"window"`
	Size    int64               `json:"size"`
	ModTime int64               `json:"modTime"`
	Refs    map[string]indexRef `json:"refs"`
	Entries []indexEntry        `json:"entries"`
}

// IndexPath returns the index path for a SAM file.
func IndexPath(inPath string) string { return inPath + ".sami" }

// BuildIndex writes a linear index for a coordinate-sorted SAM file to
// inPath+".sami" and returns the index path.
func BuildIndex(inPath string) (string, error) {
	f, err := os.Open(inPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	idx := &samIndex{
		Version: 1,
		Window:  indexWindow,
		Size:    info.Size(),
		ModTime: info.ModTime().UnixNano(),
		Refs:    map[string]indexRef{},
	}
	header := &Header{RefSet: map[string]bool{}}
	br := bufio.NewReaderSize(f, 1<<20)
	var off int64
	lineNo := 0
	lastRef := ""
	lastWindow := -1
	for {
		raw, err := br.ReadString('\n')
		if len(raw) > 0 {
			lineOff := off
			off += int64(len(raw))
			lineNo++
			ln := strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r")
			if ln != "" {
				if strings.HasPrefix(ln, "@") {
					if err := parseHeaderLineInto(header, lineNo, ln); err != nil {
						return "", err
					}
				} else {
					rec, err := parseRecordLine(header, lineNo, ln, false)
					if err != nil {
						return "", err
					}
					if rec.Placed() {
						ir := idx.Refs[rec.RName]
						if rl := rec.RefLen(); rl > ir.MaxRefLen {
							ir.MaxRefLen = rl
						}
						idx.Refs[rec.RName] = ir
						w := (rec.Pos - 1) / indexWindow
						if rec.RName != lastRef || w != lastWindow {
							idx.Entries = append(idx.Entries, indexEntry{
								Ref:    rec.RName,
								Pos:    w*indexWindow + 1,
								Offset: lineOff,
							})
							lastRef = rec.RName
							lastWindow = w
						}
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return "", err
	}
	out := IndexPath(inPath)
	if err := writeFileAtomic(out+".tmp", out, data); err != nil {
		return "", err
	}
	return out, nil
}

// loadIndex reads an index and verifies it matches the current file.
func loadIndex(path string, info os.FileInfo) *samIndex {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var idx samIndex
	if json.Unmarshal(data, &idx) != nil || idx.Version != 1 || idx.Window <= 0 {
		return nil
	}
	if idx.Size != info.Size() || idx.ModTime != info.ModTime().UnixNano() {
		return nil
	}
	return &idx
}

// seekOffset returns the byte offset from which a scan for ref:[start,...]
// must begin to see every record that can overlap the interval.
func (idx *samIndex) seekOffset(ref string, start int) (int64, bool) {
	ir, ok := idx.Refs[ref]
	if !ok {
		return 0, false
	}
	target := start - ir.MaxRefLen
	if target < 1 {
		target = 1
	}
	best := int64(-1)
	first := int64(-1)
	for _, e := range idx.Entries {
		if e.Ref != ref {
			continue
		}
		if first < 0 {
			first = e.Offset
		}
		if e.Pos > target {
			break
		}
		best = e.Offset
	}
	if best < 0 {
		best = first
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}

// CoveragePositions streams per-position coverage for ref:[start,end]
// (1-based, inclusive) from a coordinate-sorted SAM file, invoking emit
// for every position in the interval. Memory use is bounded by the
// number of records overlapping the interval, not by interval length.
// If an index built by BuildIndex is present it is used to seek.
func CoveragePositions(inPath, ref string, start, end int, opts CoverageOptions, emit func(*Position) error) error {
	if ref == "" || ref == "*" {
		return fmt.Errorf("coverage: reference name required")
	}
	if start < 1 || end < start {
		return fmt.Errorf("coverage: invalid interval [%d,%d]", start, end)
	}
	f, err := os.Open(inPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}

	var rd *Reader
	if idx := loadIndex(IndexPath(inPath), info); idx != nil {
		off, ok := idx.seekOffset(ref, start)
		if !ok {
			rd = nil // reference has no records: all positions are empty
		} else {
			if _, err := f.Seek(off, io.SeekStart); err != nil {
				return err
			}
			rd = newTrustedReader(f)
		}
	} else {
		rd = NewReader(f)
		if _, err := rd.ReadHeader(); err != nil {
			return err
		}
	}
	s := &covSweep{ref: ref, opts: opts}
	if rd != nil {
		s.rd = rd
	} else {
		s.eof = true
	}
	return s.run(start, end, emit)
}

// activeRec is a record expanded into per-position observations over its
// reference span.
type activeRec struct {
	pos  int
	end  int
	rev  bool
	base []byte // 0 where the record contributes no base
	qual []byte
	del  []bool // deletion-spanning positions
	ins  []int  // insertions immediately after the position
}

// covSweep sweeps one reference interval in ascending position order.
type covSweep struct {
	ref    string
	opts   CoverageOptions
	rd     *Reader
	next   *Record // one-record lookahead
	have   bool
	eof    bool
	active []*activeRec
}

func (s *covSweep) run(start, end int, emit func(*Position) error) error {
	for p := start; p <= end; p++ {
		if err := s.ingest(p); err != nil {
			return err
		}
		pos := s.collect(p)
		if err := emit(pos); err != nil {
			return err
		}
	}
	return nil
}

// ingest reads records up to and including position p.
func (s *covSweep) ingest(p int) error {
	for !s.eof {
		if !s.have {
			rec, err := s.rd.Next()
			if err == io.EOF {
				s.eof = true
				break
			}
			if err != nil {
				return err
			}
			s.next, s.have = rec, true
		}
		rec := s.next
		if rec.RName != s.ref {
			if rec.RName > s.ref || !rec.Placed() {
				s.eof = true // sorted input: no more records for ref
				break
			}
			s.have = false // other reference, before ours: skip
			continue
		}
		if rec.Pos > p {
			break
		}
		s.have = false
		if !s.keep(rec) {
			continue
		}
		ar := expandRecord(rec)
		if ar.end >= p {
			s.active = append(s.active, ar)
		}
	}
	return nil
}

// keep applies record-level filters.
func (s *covSweep) keep(rec *Record) bool {
	const excluded = FlagUnmapped | FlagSecondary | FlagSupplementary
	if rec.Flag&excluded != 0 {
		return false
	}
	if !s.opts.IncludeDups && rec.Flag&FlagDuplicate != 0 {
		return false
	}
	return rec.Cigar != nil
}

// collect expires finished records and summarizes position p.
func (s *covSweep) collect(p int) *Position {
	kept := s.active[:0]
	for _, ar := range s.active {
		if ar.end >= p {
			kept = append(kept, ar)
		}
	}
	s.active = kept

	pos := &Position{Ref: s.ref, Pos: p}
	for _, ar := range s.active {
		rel := p - ar.pos
		if rel < 0 || rel >= len(ar.base) {
			continue
		}
		if ar.base[rel] != 0 {
			pos.Depth++
			pos.Covered++
			pos.Obs = append(pos.Obs, Observation{
				Base:    ar.base[rel],
				Qual:    ar.qual[rel],
				Reverse: ar.rev,
			})
		} else if ar.del[rel] {
			pos.Covered++
		}
		pos.Ins += ar.ins[rel]
	}
	return pos
}

// expandRecord unrolls a record's CIGAR into per-position observations.
// Insertions and deletions never shift the reference correspondence of
// subsequent positions.
func expandRecord(rec *Record) *activeRec {
	refLen := rec.RefLen()
	ar := &activeRec{
		pos:  rec.Pos,
		end:  rec.Pos + refLen - 1,
		rev:  rec.Flag&FlagReverse != 0,
		base: make([]byte, refLen),
		qual: make([]byte, refLen),
		del:  make([]bool, refLen),
		ins:  make([]int, refLen),
	}
	p := rec.Pos
	q := 0
	for _, op := range rec.Cigar {
		switch op.Op {
		case 'M', '=', 'X':
			for i := 0; i < op.Len; i++ {
				ar.base[p-ar.pos] = baseAt(rec, q)
				ar.qual[p-ar.pos] = qualAt(rec, q)
				p++
				q++
			}
		case 'I':
			idx := p - 1 - ar.pos
			if idx < 0 {
				idx = 0
			}
			ar.ins[idx] += op.Len
			q += op.Len
		case 'D':
			for i := 0; i < op.Len; i++ {
				ar.del[p-ar.pos] = true
				p++
			}
		case 'N':
			p += op.Len
		case 'S':
			q += op.Len
		case 'H', 'P':
		}
	}
	return ar
}

func baseAt(rec *Record, q int) byte {
	if q < len(rec.Seq) {
		return rec.Seq[q]
	}
	return 'N'
}

func qualAt(rec *Record, q int) byte {
	if q < len(rec.Qual) {
		return rec.Qual[q] - 33
	}
	return 255
}

// FormatPosition renders one coverage position as a TSV line:
// ref, pos, depth, covered, insertions, observations.
// Observations are comma-separated base:qual:strand triples.
func FormatPosition(p *Position) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\t%d\t%d\t%d\t%d\t", p.Ref, p.Pos, p.Depth, p.Covered, p.Ins)
	for i, o := range p.Obs {
		if i > 0 {
			b.WriteByte(',')
		}
		strand := "+"
		if o.Reverse {
			strand = "-"
		}
		fmt.Fprintf(&b, "%c:%d:%s", o.Base, o.Qual, strand)
	}
	return b.String()
}
