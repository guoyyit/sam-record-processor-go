// Package sam provides parsing, validation, sorting, merging, duplicate
// marking and coverage analysis for SAM (Sequence Alignment/Map) files.
package sam

// Flag is a SAM bitwise flag (column 2).
type Flag uint16

const (
	FlagPaired        Flag = 0x1
	FlagProperPair    Flag = 0x2
	FlagUnmapped      Flag = 0x4
	FlagMateUnmapped  Flag = 0x8
	FlagReverse       Flag = 0x10
	FlagMateReverse   Flag = 0x20
	FlagRead1         Flag = 0x40
	FlagRead2         Flag = 0x80
	FlagSecondary     Flag = 0x100
	FlagQCFail        Flag = 0x200
	FlagDuplicate     Flag = 0x400
	FlagSupplementary Flag = 0x800
)

// CigarOp is a single CIGAR operation.
type CigarOp struct {
	Op  byte // one of MIDNSHP=X
	Len int
}

// ConsumesQuery reports whether the op consumes query (read) bases.
func (c CigarOp) ConsumesQuery() bool {
	switch c.Op {
	case 'M', 'I', 'S', '=', 'X':
		return true
	}
	return false
}

// ConsumesRef reports whether the op consumes reference bases.
func (c CigarOp) ConsumesRef() bool {
	switch c.Op {
	case 'M', 'D', 'N', '=', 'X':
		return true
	}
	return false
}

// Record is a parsed SAM alignment record.
type Record struct {
	Name  string
	Flag  Flag
	RName string // reference name, "*" if none
	Pos   int    // 1-based leftmost coordinate, 0 if unplaced
	MapQ  int
	Cigar []CigarOp // nil if CIGAR is "*"
	RNext string    // mate reference: "=", "*", or a reference name
	PNext int
	TLen  int
	Seq   string // "" if "*"
	Qual  string // "" if "*"
	Aux   []string

	raw   string // original line, without trailing newline
	dirty bool   // set when parsed fields were mutated after parsing
}

// Placed reports whether the record carries a usable coordinate.
func (r *Record) Placed() bool { return r.RName != "*" && r.Pos > 0 }

// QueryLen returns the number of query bases consumed by the CIGAR.
func (r *Record) QueryLen() int {
	n := 0
	for _, op := range r.Cigar {
		if op.ConsumesQuery() {
			n += op.Len
		}
	}
	return n
}

// RefLen returns the number of reference bases consumed by the CIGAR.
func (r *Record) RefLen() int {
	n := 0
	for _, op := range r.Cigar {
		if op.ConsumesRef() {
			n += op.Len
		}
	}
	return n
}

// End returns the 1-based inclusive reference end coordinate, or 0 for
// unplaced records.
func (r *Record) End() int {
	if !r.Placed() {
		return 0
	}
	return r.Pos + r.RefLen() - 1
}

// QualSum returns the sum of base qualities (Phred scores). A missing
// quality string ("*") sums to 0.
func (r *Record) QualSum() int {
	sum := 0
	for i := 0; i < len(r.Qual); i++ {
		sum += int(r.Qual[i]) - 33
	}
	return sum
}

// SetFlag sets the given flag bits and marks the record modified.
func (r *Record) SetFlag(f Flag) { r.Flag |= f; r.dirty = true }

// Header holds the SAM header: raw lines plus the reference dictionary.
type Header struct {
	Lines  []string // raw header lines, each starting with '@'
	Refs   []string // reference names in @SQ declaration order
	RefSet map[string]bool
}

// HasRef reports whether name is declared by an @SQ line.
func (h *Header) HasRef(name string) bool { return h.RefSet[name] }
