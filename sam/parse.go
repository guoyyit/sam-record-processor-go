package sam

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ParseError describes a malformed or semantically invalid SAM record.
type ParseError struct {
	Line  int    // 1-based line number in the input
	Field string // offending field or aspect
	Msg   string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("line %d: %s: %s", e.Line, e.Field, e.Msg)
}

// Reader parses a SAM text stream: header lines first, then records.
type Reader struct {
	br         *bufio.Reader
	header     *Header
	pending    *string // first record line, buffered during ReadHeader
	headerDone bool
	line       int  // 1-based number of the most recently read line
	trusted    bool // skip reference-declaration checks (validated input)
}

// NewReader returns a Reader over r.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, 1<<20)}
}

// newTrustedReader returns a Reader over r that expects alignment records
// immediately (no header) and skips reference-declaration checks. It is
// used for streams already validated, such as indexed seeks into files
// parsed earlier.
func newTrustedReader(r io.Reader) *Reader {
	return &Reader{
		br:         bufio.NewReaderSize(r, 1<<20),
		header:     &Header{RefSet: map[string]bool{}},
		headerDone: true,
		trusted:    true,
	}
}

// Header returns the parsed header, valid once ReadHeader or Next has
// been called.
func (rd *Reader) Header() *Header { return rd.header }

// ReadHeader consumes and returns the SAM header. It is optional: Next
// consumes any remaining header lines lazily.
func (rd *Reader) ReadHeader() (*Header, error) {
	if rd.headerDone {
		return rd.header, nil
	}
	rd.header = &Header{RefSet: map[string]bool{}}
	for {
		ln, err := rd.readLine()
		if err == io.EOF {
			rd.headerDone = true
			return rd.header, nil
		}
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(ln, "@") {
			rd.pending = &ln
			rd.headerDone = true
			return rd.header, nil
		}
		if err := parseHeaderLineInto(rd.header, rd.line, ln); err != nil {
			return nil, err
		}
	}
}

// Next returns the next alignment record, or io.EOF when the stream is
// exhausted. Malformed records yield a *ParseError.
func (rd *Reader) Next() (*Record, error) {
	if !rd.headerDone {
		if _, err := rd.ReadHeader(); err != nil {
			return nil, err
		}
	}
	for {
		var ln string
		if rd.pending != nil {
			ln = *rd.pending
			rd.pending = nil
		} else {
			var err error
			ln, err = rd.readLine()
			if err != nil {
				return nil, err
			}
		}
		if strings.HasPrefix(ln, "@") {
			return nil, &ParseError{Line: rd.line, Field: "header",
				Msg: "header line found after alignment records"}
		}
		return parseRecordLine(rd.header, rd.line, ln, rd.trusted)
	}
}

// readLine returns the next line without the trailing newline.
func (rd *Reader) readLine() (string, error) {
	ln, err := rd.br.ReadString('\n')
	if err != nil {
		if err == io.EOF && len(ln) > 0 {
			rd.line++
			return strings.TrimSuffix(strings.TrimSuffix(ln, "\n"), "\r"), nil
		}
		return "", err
	}
	rd.line++
	ln = strings.TrimSuffix(ln, "\n")
	ln = strings.TrimSuffix(ln, "\r")
	if ln == "" {
		return "", &ParseError{Line: rd.line, Field: "line", Msg: "empty line"}
	}
	return ln, nil
}

func parseHeaderLineInto(h *Header, line int, ln string) error {
	if len(ln) < 3 {
		return &ParseError{Line: line, Field: "header", Msg: "header line too short"}
	}
	typ := ln[1:3]
	switch typ {
	case "HD", "SQ", "RG", "PG", "CO":
	default:
		return &ParseError{Line: line, Field: "header",
			Msg: fmt.Sprintf("unknown header record type %q", typ)}
	}
	if typ == "CO" {
		h.Lines = append(h.Lines, ln)
		return nil
	}
	fields := strings.Split(ln, "\t")
	var sn string
	for _, f := range fields[1:] {
		if len(f) < 5 || f[2] != ':' {
			return &ParseError{Line: line, Field: "header",
				Msg: fmt.Sprintf("malformed header field %q", f)}
		}
		if typ == "SQ" && strings.HasPrefix(f, "SN:") {
			sn = f[3:]
		}
	}
	if typ == "SQ" {
		if sn == "" {
			return &ParseError{Line: line, Field: "header",
				Msg: "@SQ line missing SN field"}
		}
		if h.RefSet[sn] {
			return &ParseError{Line: line, Field: "header",
				Msg: fmt.Sprintf("duplicate @SQ sequence name %q", sn)}
		}
		h.RefSet[sn] = true
		h.Refs = append(h.Refs, sn)
	}
	h.Lines = append(h.Lines, ln)
	return nil
}

func parseRecordLine(h *Header, line int, ln string, trusted bool) (*Record, error) {
	fields := strings.Split(ln, "\t")
	if len(fields) < 11 {
		return nil, &ParseError{Line: line, Field: "record",
			Msg: fmt.Sprintf("expected at least 11 tab-separated fields, got %d", len(fields))}
	}
	rec := &Record{raw: ln}

	rec.Name = fields[0]
	if rec.Name == "" {
		return nil, &ParseError{Line: line, Field: "QNAME", Msg: "empty query name"}
	}

	flag, err := strconv.ParseUint(fields[1], 10, 16)
	if err != nil {
		return nil, &ParseError{Line: line, Field: "FLAG",
			Msg: fmt.Sprintf("invalid flag %q: must be an integer in [0,65535]", fields[1])}
	}
	rec.Flag = Flag(flag)

	rec.RName = fields[2]
	if rec.RName != "*" && !trusted && !h.HasRef(rec.RName) {
		return nil, &ParseError{Line: line, Field: "RNAME",
			Msg: fmt.Sprintf("reference %q not declared in header @SQ lines", rec.RName)}
	}

	pos, err := strconv.Atoi(fields[3])
	if err != nil || pos < 0 {
		return nil, &ParseError{Line: line, Field: "POS",
			Msg: fmt.Sprintf("invalid position %q: must be a non-negative integer", fields[3])}
	}
	rec.Pos = pos

	mapq, err := strconv.Atoi(fields[4])
	if err != nil || mapq < 0 || mapq > 255 {
		return nil, &ParseError{Line: line, Field: "MAPQ",
			Msg: fmt.Sprintf("invalid mapping quality %q: must be an integer in [0,255]", fields[4])}
	}
	rec.MapQ = mapq

	cigar, err := parseCigar(fields[5])
	if err != nil {
		return nil, &ParseError{Line: line, Field: "CIGAR", Msg: err.Error()}
	}
	rec.Cigar = cigar

	rec.RNext = fields[6]
	if rec.RNext != "*" && rec.RNext != "=" && !trusted && !h.HasRef(rec.RNext) {
		return nil, &ParseError{Line: line, Field: "RNEXT",
			Msg: fmt.Sprintf("mate reference %q not declared in header @SQ lines", rec.RNext)}
	}

	pnext, err := strconv.Atoi(fields[7])
	if err != nil || pnext < 0 {
		return nil, &ParseError{Line: line, Field: "PNEXT",
			Msg: fmt.Sprintf("invalid mate position %q: must be a non-negative integer", fields[7])}
	}
	rec.PNext = pnext

	tlen, err := strconv.Atoi(fields[8])
	if err != nil {
		return nil, &ParseError{Line: line, Field: "TLEN",
			Msg: fmt.Sprintf("invalid template length %q", fields[8])}
	}
	rec.TLen = tlen

	rec.Seq = fields[9]
	if rec.Seq == "*" {
		rec.Seq = ""
	} else if !validSeq(rec.Seq) {
		return nil, &ParseError{Line: line, Field: "SEQ",
			Msg: "sequence contains characters outside [A-Za-z=.]"}
	}

	rec.Qual = fields[10]
	if rec.Qual == "*" {
		rec.Qual = ""
	} else {
		for i := 0; i < len(rec.Qual); i++ {
			if rec.Qual[i] < 33 || rec.Qual[i] > 126 {
				return nil, &ParseError{Line: line, Field: "QUAL",
					Msg: "quality contains characters outside printable ASCII [!-~]"}
			}
		}
	}

	rec.Aux = fields[11:]
	for _, a := range rec.Aux {
		if err := validAux(a); err != nil {
			return nil, &ParseError{Line: line, Field: "aux", Msg: err.Error()}
		}
	}

	if err := rec.checkSemantics(line); err != nil {
		return nil, err
	}
	return rec, nil
}

// checkSemantics validates cross-field consistency of a parsed record.
func (r *Record) checkSemantics(line int) error {
	fail := func(field, msg string) error {
		return &ParseError{Line: line, Field: field, Msg: msg}
	}
	f := r.Flag
	if f&FlagProperPair != 0 && f&FlagPaired == 0 {
		return fail("FLAG", "proper-pair bit (0x2) set without paired bit (0x1)")
	}
	if f&FlagMateUnmapped != 0 && f&FlagPaired == 0 {
		return fail("FLAG", "mate-unmapped bit (0x8) set without paired bit (0x1)")
	}
	if f&FlagMateReverse != 0 && f&FlagPaired == 0 {
		return fail("FLAG", "mate-reverse bit (0x20) set without paired bit (0x1)")
	}
	if f&(FlagRead1|FlagRead2) != 0 && f&FlagPaired == 0 {
		return fail("FLAG", "read1/read2 bit set without paired bit (0x1)")
	}
	if f&FlagRead1 != 0 && f&FlagRead2 != 0 {
		return fail("FLAG", "read1 (0x40) and read2 (0x80) bits both set")
	}
	mapped := f&FlagUnmapped == 0
	if mapped {
		if r.RName == "*" {
			return fail("RNAME", "record not flagged unmapped (0x4) but has no reference")
		}
		if r.Pos == 0 {
			return fail("POS", "mapped record has position 0")
		}
		if r.Cigar == nil {
			return fail("CIGAR", "mapped record has no CIGAR")
		}
	}
	if r.RName == "*" && r.Pos != 0 {
		return fail("POS", "record without reference has non-zero position")
	}
	if r.Seq != "" && r.Qual != "" && len(r.Seq) != len(r.Qual) {
		return fail("QUAL", fmt.Sprintf("quality length %d does not match sequence length %d",
			len(r.Qual), len(r.Seq)))
	}
	if r.Cigar != nil && r.Seq != "" {
		if ql := r.QueryLen(); ql != len(r.Seq) {
			return fail("CIGAR", fmt.Sprintf("CIGAR consumes %d query bases but sequence has %d",
				ql, len(r.Seq)))
		}
	}
	return nil
}

func parseCigar(s string) ([]CigarOp, error) {
	if s == "*" {
		return nil, nil
	}
	if s == "" {
		return nil, fmt.Errorf("empty CIGAR string")
	}
	var ops []CigarOp
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			n = n*10 + int(c-'0')
		case isCigarOp(c):
			if n <= 0 {
				return nil, fmt.Errorf("CIGAR operation %c has non-positive length", c)
			}
			ops = append(ops, CigarOp{Op: c, Len: n})
			n = 0
		default:
			return nil, fmt.Errorf("invalid CIGAR character %q", c)
		}
	}
	if n != 0 || len(ops) == 0 {
		return nil, fmt.Errorf("malformed CIGAR %q", s)
	}
	return ops, nil
}

func isCigarOp(c byte) bool {
	switch c {
	case 'M', 'I', 'D', 'N', 'S', 'H', 'P', '=', 'X':
		return true
	}
	return false
}

func validSeq(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '=' || c == '.') {
			return false
		}
	}
	return true
}

func validAux(s string) error {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 || len(parts[0]) != 2 {
		return fmt.Errorf("malformed auxiliary field %q: want TAG:TYPE:VALUE", s)
	}
	switch parts[1] {
	case "A", "i", "f", "Z", "H", "B":
	default:
		return fmt.Errorf("auxiliary field %q has invalid type %q", s, parts[1])
	}
	return nil
}
