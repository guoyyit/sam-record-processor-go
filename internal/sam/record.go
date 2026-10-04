package sam

import (
	"fmt"
	"strconv"
	"strings"
)

// Record is one parsed SAM alignment line.
type Record struct {
	Raw    []byte
	QName  string
	Flag   Flag
	RName  string
	Pos    int // 1-based; 0 when unavailable
	MapQ   int
	Cigar  Cigar
	RNext  string
	PNext  int
	TLEN   int
	Seq    string
	Qual   string
	Tags   string
	LineNo int
}

// ParseError locates a failing alignment record by 1-based line number.
type ParseError struct {
	LineNo int
	Text   string
	Reason string
}

func (e *ParseError) Error() string {
	if e.LineNo > 0 {
		return fmt.Sprintf("record at line %d: %s (record: %q)", e.LineNo, e.Reason, e.Text)
	}
	return fmt.Sprintf("%s (record: %q)", e.Reason, e.Text)
}

func recError(lineNo int, line []byte, format string, args ...any) error {
	return &ParseError{LineNo: lineNo, Text: string(line), Reason: fmt.Sprintf(format, args...)}
}

// ParseRecord parses one SAM alignment line. lineNo is the 1-based line
// number within the input file and is attached to errors.
func ParseRecord(line []byte, lineNo int) (*Record, error) {
	if len(line) == 0 {
		return nil, recError(lineNo, line, "empty alignment line")
	}
	if line[0] == '@' {
		return nil, recError(lineNo, line, "header line found where alignment record expected")
	}
	if strings.IndexByte(string(line), '\t') < 0 {
		return nil, recError(lineNo, line, "not tab-separated; at least 11 mandatory fields required")
	}
	fields := strings.Split(string(line), "\t")
	if len(fields) < 11 {
		return nil, recError(lineNo, line, "too few fields: got %d, require 11", len(fields))
	}
	if strings.IndexByte(fields[0], ' ') >= 0 {
		return nil, recError(lineNo, line, "QNAME must not contain spaces")
	}
	r := &Record{
		Raw:    append([]byte(nil), line...),
		QName:  fields[0],
		Tags:   strings.Join(fields[11:], "\t"),
		LineNo: lineNo,
	}

	flagN, err := strconv.ParseUint(fields[1], 10, 16)
	if err != nil {
		return nil, recError(lineNo, line, "invalid FLAG %q: %v", fields[1], err)
	}
	r.Flag = Flag(flagN)

	r.RName = fields[2]
	pos, err := parsePos(fields[3])
	if err != nil {
		return nil, recError(lineNo, line, "invalid POS %q: %v", fields[3], err)
	}
	r.Pos = pos
	mapq, err := strconv.Atoi(fields[4])
	if err != nil || mapq < 0 || mapq > 255 {
		return nil, recError(lineNo, line, "invalid MAPQ %q: integer 0..255 required", fields[4])
	}
	r.MapQ = mapq

	r.Cigar, err = ParseCigar(fields[5])
	if err != nil {
		return nil, recError(lineNo, line, "%v", err)
	}

	r.RNext = fields[6]
	if r.RNext != "*" && r.RNext != "=" {
		// basic name check; declared-reference check happens in Validate
		if strings.IndexAny(r.RNext, " \t") >= 0 {
			return nil, recError(lineNo, line, "invalid RNEXT %q", fields[6])
		}
	}
	pnext, err := parsePos(fields[7])
	if err != nil {
		return nil, recError(lineNo, line, "invalid PNEXT %q: %v", fields[7], err)
	}
	r.PNext = pnext
	tlen, err := strconv.Atoi(fields[8])
	if err != nil {
		return nil, recError(lineNo, line, "invalid TLEN %q: %v", fields[8], err)
	}
	r.TLEN = tlen

	r.Seq = fields[9]
	r.Qual = fields[10]
	if err := r.checkSeqQual(); err != nil {
		return nil, recError(lineNo, line, "%v", err)
	}
	if err := r.validateTags(); err != nil {
		return nil, recError(lineNo, line, "%v", err)
	}
	if err := r.Flag.Validate(); err != nil {
		return nil, recError(lineNo, line, "%v", err)
	}
	return r, nil
}

func parsePos(s string) (int, error) {
	if s == "0" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("must be 0 or a positive integer")
	}
	return n, nil
}

func (r *Record) checkSeqQual() error {
	if r.Seq != "*" {
		if len(r.Seq) == 0 {
			return fmt.Errorf("SEQ must be '*' or a non-empty sequence")
		}
		for _, b := range []byte(r.Seq) {
			switch b {
			case 'A', 'C', 'G', 'T', 'N', '.',
				'a', 'c', 'g', 't', 'n',
				'M', 'R', 'W', 'S', 'Y', 'K', 'V', 'H', 'D', 'B',
				'm', 'r', 'w', 's', 'y', 'k', 'v', 'h', 'd', 'b':
			default:
				return fmt.Errorf("SEQ contains invalid character %q", string(b))
			}
		}
	}
	if r.Qual != "*" {
		if r.Seq == "*" {
			return fmt.Errorf("QUAL present but SEQ is '*'")
		}
		if len(r.Qual) != len(r.Seq) {
			return fmt.Errorf("SEQ/QUAL length mismatch: %d vs %d", len(r.Seq), len(r.Qual))
		}
		for _, b := range []byte(r.Qual) {
			if b < 33 || b > 126 {
				return fmt.Errorf("QUAL contains byte outside printable Phred33 range")
			}
		}
	} else if r.Seq != "*" {
		// '*' is explicitly allowed.
	}
	return nil
}

func (r *Record) validateTags() error {
	if r.Tags == "" {
		return nil
	}
	for _, f := range strings.Split(r.Tags, "\t") {
		if len(f) < 4 || f[2] != ':' {
			return fmt.Errorf("optional field %q is not of form TAG:TYPE:VALUE", f)
		}
		if !isAlphaNum(f[0]) || !isAlphaNum(f[1]) {
			return fmt.Errorf("optional field %q has an invalid TAG", f)
		}
		switch f[3] {
		case 'A', 'i', 'f', 'Z', 'H', 'B':
		default:
			return fmt.Errorf("optional field %q has unsupported TYPE %q", f, string(f[3]))
		}
		if len(f) < 5 {
			return fmt.Errorf("optional field %q has empty VALUE", f)
		}
	}
	return nil
}

func isAlphaNum(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

// Validate checks the record against the reference dictionary and the
// SAMv1 semantic constraints between FLAG, RNAME/POS, RNEXT/PNEXT,
// CIGAR and SEQ.
func (r *Record) Validate(h *Header) error {
	unmapped := r.Flag.Unmapped()
	paired := r.Flag.Paired()

	if r.RName == "*" {
		if r.Pos != 0 {
			return recError(r.LineNo, r.Raw, "RNAME is '*' but POS=%d (must be 0)", r.Pos)
		}
	} else {
		ref, ok := h.Lookup(r.RName)
		if !ok {
			return recError(r.LineNo, r.Raw, "RNAME %q is not declared in @SQ header", r.RName)
		}
		if r.Pos == 0 {
			return recError(r.LineNo, r.Raw, "mapped record must have POS > 0")
		}
		if end := r.Pos + r.Cigar.ReferenceLength() - 1; end > ref.Length {
			return recError(r.LineNo, r.Raw,
				"alignment extends beyond reference %q: end %d > LN %d", r.RName, end, ref.Length)
		}
	}

	if unmapped {
		// SAMv1 allows an unmapped read to retain RNAME/POS for
		// placement; such a reference must still be declared.
		if r.RName != "*" {
			if _, ok := h.Lookup(r.RName); !ok {
				return recError(r.LineNo, r.Raw, "UNMAP record RNAME %q is not declared in @SQ header", r.RName)
			}
			if r.Pos < 1 {
				return recError(r.LineNo, r.Raw, "UNMAP record with RNAME must have POS > 0")
			}
		}
		if r.Cigar != nil && r.Cigar.String() != "0" {
			return recError(r.LineNo, r.Raw, "UNMAP record must have CIGAR '*' (or '0'), got %q", r.Cigar)
		}
	}

	if !unmapped {
		if r.Cigar == nil {
			return recError(r.LineNo, r.Raw, "mapped record must have a CIGAR (not '*')")
		}
		if err := r.Cigar.Validate(); err != nil {
			return recError(r.LineNo, r.Raw, "%v", err)
		}
		if r.Seq != "*" && r.Cigar.ReadLength() != len(r.Seq) {
			return recError(r.LineNo, r.Raw,
				"CIGAR read length %d does not match SEQ length %d", r.Cigar.ReadLength(), len(r.Seq))
		}
	}

	if paired {
		if r.RNext == "*" {
			if r.PNext != 0 {
				return recError(r.LineNo, r.Raw, "RNEXT is '*' but PNEXT=%d (must be 0)", r.PNext)
			}
		} else {
			name := r.RNext
			if name == "=" {
				name = r.RName
			}
			if _, ok := h.Lookup(name); !ok {
				return recError(r.LineNo, r.Raw, "RNEXT %q is not declared in @SQ header", r.RNext)
			}
			if r.PNext < 1 {
				return recError(r.LineNo, r.Raw, "paired record with known mate position must have PNEXT > 0")
			}
		}
	} else if r.RNext != "*" || r.PNext != 0 || r.TLEN != 0 {
		return recError(r.LineNo, r.Raw, "unpaired record must have RNEXT='*', PNEXT=0, TLEN=0")
	}
	return nil
}
