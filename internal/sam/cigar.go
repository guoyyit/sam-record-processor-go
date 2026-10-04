package sam

import (
	"fmt"
	"strconv"
)

// Op is one CIGAR operation: Len repetitions of Type.
type Op struct {
	Len  int
	Type byte
}

// Cigar is a parsed CIGAR operation sequence. A nil Cigar means the
// field was '*' (unavailable).
type Cigar []Op

const cigarOps = "MIDNSHP=X"

func isCigarOp(c byte) bool {
	for i := 0; i < len(cigarOps); i++ {
		if cigarOps[i] == c {
			return true
		}
	}
	return false
}

// ParseCigar parses a CIGAR string. '*' yields a nil Cigar with no error.
func ParseCigar(s string) (Cigar, error) {
	if s == "*" {
		return nil, nil
	}
	if len(s) == 0 {
		return nil, fmt.Errorf("empty CIGAR field; use '*' when unavailable")
	}
	var out Cigar
	i := 0
	for i < len(s) {
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == start {
			return nil, fmt.Errorf("malformed CIGAR %q: expected length digits at offset %d", s, i)
		}
		n, err := strconv.Atoi(s[start:i])
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("malformed CIGAR %q: operation length must be a positive integer", s)
		}
		if i >= len(s) {
			return nil, fmt.Errorf("malformed CIGAR %q: trailing length without operation", s)
		}
		if !isCigarOp(s[i]) {
			return nil, fmt.Errorf("malformed CIGAR %q: unknown operation %q", s, string(s[i]))
		}
		out = append(out, Op{Len: n, Type: s[i]})
		i++
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("malformed CIGAR %q: no operations", s)
	}
	return out, nil
}

// String renders the canonical CIGAR text; a nil Cigar renders as '*'.
func (c Cigar) String() string {
	if c == nil {
		return "*"
	}
	b := make([]byte, 0, len(c)*3)
	for _, op := range c {
		b = strconv.AppendInt(b, int64(op.Len), 10)
		b = append(b, op.Type)
	}
	return string(b)
}

// ReferenceLength is the number of reference positions consumed
// (operations M, D, N, =, X).
func (c Cigar) ReferenceLength() int {
	n := 0
	for _, op := range c {
		switch op.Type {
		case 'M', 'D', 'N', '=', 'X':
			n += op.Len
		}
	}
	return n
}

// ReadLength is the number of query bases stored in SEQ (operations
// M, I, S, =, X; H and P consume no stored bases).
func (c Cigar) ReadLength() int {
	n := 0
	for _, op := range c {
		switch op.Type {
		case 'M', 'I', 'S', '=', 'X':
			n += op.Len
		}
	}
	return n
}

// Validate checks structural CIGAR constraints independent of the record.
func (c Cigar) Validate() error {
	if c == nil {
		return nil
	}
	for _, op := range c {
		if !isCigarOp(op.Type) {
			return fmt.Errorf("invalid CIGAR operation %q", string(op.Type))
		}
		if op.Len <= 0 {
			return fmt.Errorf("CIGAR operation length must be positive")
		}
	}
	first, last := c[0].Type, c[len(c)-1].Type
	if first == 'D' || first == 'N' {
		return fmt.Errorf("CIGAR cannot begin with %q", string(first))
	}
	if last == 'D' || last == 'N' {
		return fmt.Errorf("CIGAR cannot end with %q", string(last))
	}
	for i, op := range c {
		if op.Type == 'S' && i != 0 && i != len(c)-1 {
			return fmt.Errorf("soft clipping (S) may only appear at the first or last operation")
		}
		if op.Type == 'H' && i != 0 && i != len(c)-1 {
			return fmt.Errorf("hard clipping (H) may only appear at the first or last operation")
		}
	}
	return nil
}
