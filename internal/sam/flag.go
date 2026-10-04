package sam

import "strconv"

// Flag is the SAM FLAG field bitmask.
type Flag uint16

// SAM flag bits, see the SAMv1 specification.
const (
	FlagPaired        Flag = 0x001
	FlagProperPair    Flag = 0x002
	FlagUnmapped      Flag = 0x004
	FlagMateUnmapped  Flag = 0x008
	FlagReverse       Flag = 0x010
	FlagMateReverse   Flag = 0x020
	FlagRead1         Flag = 0x040
	FlagRead2         Flag = 0x080
	FlagSecondary     Flag = 0x100
	FlagQCfail        Flag = 0x200
	FlagDuplicate     Flag = 0x400
	FlagSupplementary Flag = 0x800
)

func (f Flag) Get(b Flag) bool     { return f&b != 0 }
func (f Flag) Paired() bool        { return f.Get(FlagPaired) }
func (f Flag) ProperPair() bool    { return f.Get(FlagProperPair) }
func (f Flag) Unmapped() bool      { return f.Get(FlagUnmapped) }
func (f Flag) MateUnmapped() bool  { return f.Get(FlagMateUnmapped) }
func (f Flag) Reverse() bool       { return f.Get(FlagReverse) }
func (f Flag) MateReverse() bool   { return f.Get(FlagMateReverse) }
func (f Flag) Read1() bool         { return f.Get(FlagRead1) }
func (f Flag) Read2() bool         { return f.Get(FlagRead2) }
func (f Flag) Secondary() bool     { return f.Get(FlagSecondary) }
func (f Flag) QCFail() bool        { return f.Get(FlagQCfail) }
func (f Flag) Duplicate() bool     { return f.Get(FlagDuplicate) }
func (f Flag) Supplementary() bool { return f.Get(FlagSupplementary) }

// String renders the numeric flag value.
func (f Flag) String() string { return strconv.Itoa(int(f)) }

// Bits returns the names of the flag bits that are set, in bit order.
func (f Flag) Bits() []string {
	table := []struct {
		bit  Flag
		name string
	}{
		{FlagPaired, "PAIRED"},
		{FlagProperPair, "PROPER_PAIR"},
		{FlagUnmapped, "UNMAP"},
		{FlagMateUnmapped, "MUNMAP"},
		{FlagReverse, "REVERSE"},
		{FlagMateReverse, "MREVERSE"},
		{FlagRead1, "READ1"},
		{FlagRead2, "READ2"},
		{FlagSecondary, "SECONDARY"},
		{FlagQCfail, "QCFAIL"},
		{FlagDuplicate, "DUP"},
		{FlagSupplementary, "SUPPLEMENTARY"},
	}
	var out []string
	for _, e := range table {
		if f.Get(e.bit) {
			out = append(out, e.name)
		}
	}
	return out
}

// Validate checks flag bit combinations for semantic contradictions.
// SAM permits the REVERSE bit on unmapped records; all other rules follow
// the SAMv1 "bitwise flag" constraints.
func (f Flag) Validate() error {
	if f.Read1() && f.Read2() {
		return newFlagError(f, "READ1(0x40) and READ2(0x80) cannot both be set")
	}
	if f.Secondary() && f.Supplementary() {
		return newFlagError(f, "SECONDARY(0x100) and SUPPLEMENTARY(0x800) cannot both be set")
	}
	if f.Unmapped() && (f.Secondary() || f.Supplementary()) {
		return newFlagError(f, "unmapped record cannot be SECONDARY(0x100) or SUPPLEMENTARY(0x800)")
	}
	if f.ProperPair() {
		if !f.Paired() {
			return newFlagError(f, "PROPER_PAIR(0x2) requires PAIRED(0x1)")
		}
		if f.Unmapped() || f.MateUnmapped() {
			return newFlagError(f, "PROPER_PAIR(0x2) requires both mates to be mapped")
		}
	}
	if !f.Paired() && (f.MateUnmapped() || f.MateReverse() || f.Read1() || f.Read2()) {
		return newFlagError(f, "mate/read bits require PAIRED(0x1) to be set")
	}
	return nil
}

type flagError struct {
	flag Flag
	msg  string
}

func (e *flagError) Error() string {
	return "invalid flag " + e.flag.String() + ": " + e.msg
}

func newFlagError(f Flag, msg string) error {
	return &flagError{flag: f, msg: msg}
}
